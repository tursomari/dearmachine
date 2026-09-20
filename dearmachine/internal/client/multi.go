package client

import (
	"context"
	"errors"
	"fmt"
	"github.com/dearmachine/dearmachine/internal/hostos"
	"log"
	"net/mail"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func DefaultDaemonLockPath(userHomeDir func() (string, error)) (string, error) {
	home, err := resolveDeviceHome(userHomeDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".dearmachine", "run", "dearmachine.pid"), nil
}

// InboxRouter gives every pair a private Transport view while retaining one
// poller and one provider adapter for the shared inbox.
type InboxRouter struct {
	mu               sync.Mutex
	logger           *log.Logger
	lastAuthRecovery time.Time
	raw              Transport
	inbox            Inbox
	pairs            map[string]Pair
	interval         time.Duration
	lastPoll         time.Time
	pending          map[string][]Message
	known            map[string]map[string]struct{}
	stores           map[string]*Store
	guests           *GuestStore
	controllers      map[string]bool
	cached           map[string]Message
}

var errNoPairRoute = errors.New("message has no pair route")

func NewInboxRouter(raw Transport, inbox Inbox, pairs []Pair, interval time.Duration) (*InboxRouter, error) {
	if raw == nil {
		return nil, errors.New("raw inbox transport is required")
	}
	inbox, err := normalizeInbox(inbox)
	if err != nil {
		return nil, err
	}
	if interval <= 0 {
		return nil, errors.New("poll interval must be positive")
	}
	router := &InboxRouter{
		raw: raw, inbox: inbox, interval: interval,
		controllers: make(map[string]bool), cached: make(map[string]Message),
		pairs: make(map[string]Pair, len(pairs)), pending: make(map[string][]Message), known: make(map[string]map[string]struct{}), stores: make(map[string]*Store),
	}
	routes := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		pair, err = normalizePair(pair)
		if err != nil {
			return nil, err
		}
		if pair.InboxID != inbox.ID {
			return nil, fmt.Errorf("pair %q does not reference inbox %q", pair.ID, inbox.ID)
		}
		if _, found := router.pairs[pair.ID]; found {
			return nil, fmt.Errorf("duplicate pair %q in inbox router", pair.ID)
		}
		if previous, found := routes[pair.UserEmail]; found {
			return nil, fmt.Errorf("ambiguous inbox route for %s: pairs %s and %s", pair.UserEmail, previous, pair.ID)
		}
		router.pairs[pair.ID] = pair
		router.controllers[pair.UserEmail] = true
		routes[pair.UserEmail] = pair.ID
		router.known[pair.ID] = make(map[string]struct{})
	}
	if len(router.pairs) == 0 {
		return nil, errors.New("inbox router requires at least one pair")
	}
	return router, nil
}

func (router *InboxRouter) Endpoint(pairID string) (Transport, error) {
	if _, found := router.pairs[pairID]; !found {
		return nil, fmt.Errorf("pair %q is not registered on inbox %q", pairID, router.inbox.ID)
	}
	return &pairEndpoint{router: router, pairID: pairID}, nil
}

func (router *InboxRouter) poll(ctx context.Context, pairID string) ([]Message, error) {
	router.mu.Lock()
	defer router.mu.Unlock()
	if time.Since(router.lastPoll) >= router.interval || router.lastPoll.IsZero() {
		messages, err := router.raw.Poll(ctx)
		if err != nil {
			return nil, err
		}
		held, err := router.resumeAuthenticationMessages(ctx)
		if err != nil {
			return nil, err
		}
		seenIDs := map[string]bool{}
		for _, m := range messages {
			seenIDs[m.MessageID] = true
		}
		for _, m := range held {
			if !seenIDs[m.MessageID] {
				messages = append(messages, m)
			}
		}
		distributed := make(map[string][]Message)
		for _, message := range messages {
			message, err = router.normalizeDelivery(message)
			if err != nil {
				return nil, err
			}
			if containsFold(message.Labels, "sent") {
				if err := router.raw.MarkProcessed(ctx, message.MessageID); err != nil {
					return nil, fmt.Errorf("clear own outbound message %s: %w", message.MessageID, err)
				}
				continue
			}
			original := message
			message, err = router.authenticateMessage(ctx, message)
			if errors.Is(err, ErrMessageUnauthenticated) {
				if err := router.holdUnauthenticated(ctx, original); err != nil {
					return nil, err
				}
				continue
			} else if errors.Is(err, ErrGuestUnauthorized) {
				router.logAuthentication(original, Pair{}, "stale_or_changed_message")
				if router.guests != nil {
					if _, err := router.guests.db.Exec(`UPDATE guest_auth_messages SET done=1 WHERE inbox_id=? AND message_id=?`, guestInboxKey(router.inbox), original.MessageID); err != nil {
						return nil, err
					}
				}
				if err := router.raw.MarkProcessed(ctx, original.MessageID); err != nil {
					return nil, err
				}
				continue
			} else if err != nil {
				return nil, err
			}
			routed, err := router.routeInbound(message)
			if errors.Is(err, errNoPairRoute) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if message.riskAccepted {
				if err := router.recordAcceptedAuthentication(message, routed); err != nil {
					return nil, err
				}
			}
			if handled, err := router.handleAuthenticationControl(ctx, routed, message); handled || err != nil {
				if err != nil {
					return nil, err
				}
				continue
			}
			if err := router.observeInvitations(ctx, routed, message); err != nil {
				return nil, err
			}
			from, _ := canonicalMessageAddress(message.From)
			if from != routed.UserEmail {
				key, _ := guestKey(routed, router.inbox, message)
				if err := router.guests.bindWork(key, message.MessageID, message.fingerprint); errors.Is(err, ErrGuestUnauthorized) {
					continue
				} else if err != nil {
					return nil, err
				}
			}
			if err := router.snapshotGuestRecipients(routed.ID, message); err != nil {
				return nil, err
			}
			distributed[routed.ID] = append(distributed[routed.ID], message)
		}
		for id, batch := range distributed {
			router.pending[id] = append(router.pending[id], batch...)
			for _, message := range batch {
				router.remember(id, message)
			}
		}
		router.lastPoll = time.Now()
	}
	batch := append([]Message(nil), router.pending[pairID]...)
	delete(router.pending, pairID)
	allowed := batch[:0]
	for _, message := range batch {
		if err := router.authorize(pairID, message); errors.Is(err, ErrGuestUnauthorized) {
			continue
		} else if err != nil {
			return nil, err
		}
		allowed = append(allowed, message)
	}
	return allowed, nil
}

func (router *InboxRouter) routeInbound(message Message) (Pair, error) {
	if !message.authenticated && !message.riskAccepted {
		return Pair{}, errNoPairRoute
	}
	from, err := canonicalMessageAddress(message.From)
	if err != nil {
		return Pair{}, fmt.Errorf("%w: message %s has invalid sender", errNoPairRoute, message.MessageID)
	}
	if !router.deliveredToInbox(message) {
		return Pair{}, fmt.Errorf("%w: message %s recipient does not match inbox %s", errNoPairRoute, message.MessageID, router.inbox.Address)
	}
	var matches []Pair
	for _, pair := range router.pairs {
		if message.authenticated && from == pair.UserEmail {
			matches = append(matches, pair)
		}
	}
	if len(matches) == 0 {
		for _, pair := range router.pairs {
			if err := router.guestAllowed(pair, message); err == nil {
				matches = append(matches, pair)
			} else if !errors.Is(err, ErrGuestUnauthorized) {
				return Pair{}, err
			}
		}
		if len(matches) == 0 {
			return Pair{}, fmt.Errorf("%w: message %s sender %s", errNoPairRoute, message.MessageID, from)
		}
	}
	if len(matches) > 1 {
		return Pair{}, fmt.Errorf("route message %s: sender %s is ambiguous on inbox %s", message.MessageID, from, router.inbox.ID)
	}
	return matches[0], nil
}

func (router *InboxRouter) authorize(pairID string, message Message) error {
	pair := router.pairs[pairID]
	from, err := canonicalMessageAddress(message.From)
	if err != nil {
		return err
	}
	inboundRecipient := router.deliveredToInbox(message)
	inbound := message.authenticated && from == pair.UserEmail && inboundRecipient
	if !inbound && inboundRecipient {
		routed, routeErr := router.routeInbound(message)
		inbound = routeErr == nil && routed.ID == pairID
		if routeErr != nil && !errors.Is(routeErr, errNoPairRoute) && !errors.Is(routeErr, ErrGuestUnauthorized) {
			return routeErr
		}
	}
	outbound := containsFold(message.Labels, "sent") && from == router.inbox.Address && containsCanonicalAddress(message.To, pair.UserEmail)
	if !inbound && !outbound {
		return fmt.Errorf("%w: message %s is not routed to pair %s", ErrGuestUnauthorized, message.MessageID, pairID)
	}
	return nil
}

func (router *InboxRouter) normalizeDelivery(message Message) (Message, error) {
	if strings.TrimSpace(message.Delivery.InboxID) == "" {
		// The router constructs one raw transport for this exact registered
		// provider inbox, so stamping legacy/test transports here is trusted
		// transport configuration rather than a message-header inference.
		message.Delivery.InboxID = router.inbox.ProviderID
	} else if !strings.EqualFold(strings.TrimSpace(message.Delivery.InboxID), router.inbox.ProviderID) {
		return Message{}, fmt.Errorf(
			"message %s was delivered through provider inbox %s, not %s",
			message.MessageID, message.Delivery.InboxID, router.inbox.ProviderID,
		)
	}
	if strings.TrimSpace(message.Delivery.Recipient) == "" {
		message.Delivery.Recipient = router.inbox.Address
	}
	if message.Delivery.Role == DeliveryRoleUnknown {
		message.Delivery.Role = normalizeMessageDelivery(
			message.Delivery.InboxID, message.Delivery.Recipient,
			message.To, message.CC, message.BCC, "",
			message.Delivery.ReadState,
		).Role
	}
	if message.Delivery.ReadState == MessageReadStateUnknown {
		switch {
		case containsFold(message.Labels, "unread"):
			message.Delivery.ReadState = MessageReadStateUnread
		case containsFold(message.Labels, "read"):
			message.Delivery.ReadState = MessageReadStateRead
		}
	}
	return message, nil
}

func (router *InboxRouter) deliveredToInbox(message Message) bool {
	return strings.EqualFold(strings.TrimSpace(message.Delivery.InboxID), router.inbox.ProviderID)
}

func (router *InboxRouter) remember(pairID string, message Message) {
	router.known[pairID][message.MessageID] = struct{}{}
	router.cached[message.MessageID] = message
	for _, attachment := range message.Attachments {
		router.known[pairID]["attachment:"+attachment.AttachmentID] = struct{}{}
		router.cached["attachment:"+attachment.AttachmentID] = message
	}
}

func canonicalMessageAddress(value string) (string, error) {
	parsed, err := mail.ParseAddress(strings.TrimSpace(value))
	if err != nil || parsed.Address == "" {
		return "", errors.New("must be an RFC 5322 address")
	}
	return strings.ToLower(strings.TrimSpace(parsed.Address)), nil
}

func containsCanonicalAddress(values []string, target string) bool {
	for _, value := range values {
		if canonical, err := canonicalMessageAddress(value); err == nil && canonical == target {
			return true
		}
	}
	return false
}

type pairEndpoint struct {
	router *InboxRouter
	pairID string
}

func (endpoint *pairEndpoint) bindStore(store *Store) {
	endpoint.router.mu.Lock()
	endpoint.router.stores[endpoint.pairID] = store
	endpoint.router.logger = store.warnings
	endpoint.router.mu.Unlock()
}

func (endpoint *pairEndpoint) controllingParticipant() string {
	return endpoint.router.pairs[endpoint.pairID].UserEmail
}

func (endpoint *pairEndpoint) isControllingParticipant(message Message) bool {
	if !message.authenticated {
		return false
	}
	from, err := canonicalMessageAddress(message.From)
	return err == nil && from == endpoint.controllingParticipant()
}

func (endpoint *pairEndpoint) pollTarget() string {
	pair := endpoint.router.pairs[endpoint.pairID]
	return fmt.Sprintf("inbox %s for pair %s", endpoint.router.inbox.Address, pair.ID)
}

func (endpoint *pairEndpoint) Poll(ctx context.Context) ([]Message, error) {
	return endpoint.router.poll(ctx, endpoint.pairID)
}

func (endpoint *pairEndpoint) Thread(ctx context.Context, threadID string) ([]Message, error) {
	messages, err := endpoint.router.raw.Thread(ctx, threadID)
	if err != nil {
		return nil, err
	}
	endpoint.router.mu.Lock()
	defer endpoint.router.mu.Unlock()
	allowed := make([]Message, 0, len(messages))
	for _, message := range messages {
		message, err = endpoint.router.normalizeDelivery(message)
		if err != nil {
			return nil, err
		}
		if !containsFold(message.Labels, "sent") {
			message, err = endpoint.router.authenticateMessage(ctx, message)
			if errors.Is(err, ErrMessageUnauthenticated) {
				continue
			} else if err != nil {
				return nil, err
			}
		}
		if err := endpoint.router.authorize(endpoint.pairID, message); err != nil {
			return nil, err
		}
		endpoint.router.remember(endpoint.pairID, message)
		allowed = append(allowed, message)
	}
	return allowed, nil
}

func (endpoint *pairEndpoint) Message(ctx context.Context, messageID string) (Message, error) {
	message, err := endpoint.router.raw.Message(ctx, messageID)
	if err != nil {
		return Message{}, err
	}
	endpoint.router.mu.Lock()
	defer endpoint.router.mu.Unlock()
	message, err = endpoint.router.normalizeDelivery(message)
	if err != nil {
		return Message{}, err
	}
	if !containsFold(message.Labels, "sent") {
		message, err = endpoint.router.authenticateMessage(ctx, message)
		if err != nil {
			return Message{}, err
		}
	}
	if err := endpoint.router.authorize(endpoint.pairID, message); err != nil {
		return Message{}, err
	}
	endpoint.router.remember(endpoint.pairID, message)
	return message, nil
}

func (endpoint *pairEndpoint) Reply(ctx context.Context, messageID string, payload ReplyPayload, key string) (string, error) {
	if err := endpoint.ensureMessage(ctx, messageID); err != nil {
		return "", err
	}
	return endpoint.router.raw.Reply(ctx, messageID, payload, key)
}

func (endpoint *pairEndpoint) ReplyReceipt(ctx context.Context, message Message, recipient string) (string, bool, error) {
	if !message.authenticated && !containsFold(message.Labels, "sent") {
		var err error
		message, err = endpoint.Message(ctx, message.MessageID)
		if err != nil {
			return "", false, err
		}
	}
	endpoint.router.mu.Lock()
	err := endpoint.router.authorize(endpoint.pairID, message)
	if err == nil {
		endpoint.router.remember(endpoint.pairID, message)
	}
	endpoint.router.mu.Unlock()
	if err != nil {
		return "", false, err
	}
	return endpoint.replyReceiptWithoutAuthWarning(ctx, message, recipient)
}

func (endpoint *pairEndpoint) MarkProcessed(ctx context.Context, messageID string) error {
	if err := endpoint.ensureMessage(ctx, messageID); err != nil {
		return err
	}
	if err := endpoint.router.raw.MarkProcessed(ctx, messageID); err != nil {
		return err
	}
	if endpoint.router.guests != nil {
		_, err := endpoint.router.guests.db.Exec(`UPDATE guest_auth_messages SET done=1 WHERE pair_id=? AND inbox_id=? AND message_id=?`, endpoint.pairID, guestInboxKey(endpoint.router.inbox), messageID)
		return err
	}
	return nil
}

func (endpoint *pairEndpoint) FetchAttachment(ctx context.Context, attachmentID string, maxBytes int64) ([]byte, error) {
	endpoint.router.mu.Lock()
	_, allowed := endpoint.router.known[endpoint.pairID]["attachment:"+attachmentID]
	if allowed {
		allowed = endpoint.router.authorize(endpoint.pairID, endpoint.router.cached["attachment:"+attachmentID]) == nil
	}
	endpoint.router.mu.Unlock()
	if !allowed {
		return nil, fmt.Errorf("attachment %s is not routed to pair %s", attachmentID, endpoint.pairID)
	}
	return endpoint.router.raw.FetchAttachment(ctx, attachmentID, maxBytes)
}

func (endpoint *pairEndpoint) ensureMessage(ctx context.Context, messageID string) error {
	endpoint.router.mu.Lock()
	_, known := endpoint.router.known[endpoint.pairID][messageID]
	if known {
		err := endpoint.router.authorize(endpoint.pairID, endpoint.router.cached[messageID])
		endpoint.router.mu.Unlock()
		return err
	}
	endpoint.router.mu.Unlock()
	_, err := endpoint.Message(ctx, messageID)
	return err
}

// MultiDaemon supervises pair apps under one exclusive daemon lock and one
// shared worker-capacity gate.
type MultiDaemon struct {
	apps        []*App
	concurrency int
	lockPath    string
}

func NewMultiDaemon(apps []*App, concurrency int, lockPath string) (*MultiDaemon, error) {
	if len(apps) == 0 {
		return nil, errors.New("multi-pair daemon requires at least one pair")
	}
	if concurrency < 1 {
		return nil, errors.New("concurrency must be at least 1")
	}
	gate := make(chan struct{}, concurrency)
	for _, app := range apps {
		if app == nil {
			return nil, errors.New("multi-pair daemon contains a nil app")
		}
		app.workerGate = gate
		app.concurrency = concurrency
		app.pidfile = ""
	}
	return &MultiDaemon{apps: apps, concurrency: concurrency, lockPath: lockPath}, nil
}

func (daemon *MultiDaemon) Run(ctx context.Context) error {
	return daemon.run(ctx, false)
}

func (daemon *MultiDaemon) RunOnce(ctx context.Context) error {
	return daemon.run(ctx, true)
}

func (daemon *MultiDaemon) run(ctx context.Context, once bool) (runErr error) {
	unlock, err := CreateDaemonLock(daemon.lockPath)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, unlock()) }()
	appCleanup, err := daemon.apps[0].start(ctx)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, appCleanup()) }()
	readyPath := daemonReadyPath(daemon.lockPath)
	if err := os.WriteFile(readyPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return fmt.Errorf("publish daemon readiness: %w", err)
	}
	defer func() { runErr = errors.Join(runErr, removeOwnedDaemonFile(readyPath, os.Getpid())) }()
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	errorsOut := make(chan error, len(daemon.apps))
	for _, app := range daemon.apps {
		go func(app *App) {
			if once {
				errorsOut <- app.ProcessOnce(runContext)
			} else {
				errorsOut <- app.runLoop(runContext)
			}
		}(app)
	}
	var first error
	for range daemon.apps {
		if err := <-errorsOut; err != nil && !errors.Is(err, context.Canceled) && first == nil {
			first = err
			cancel()
		}
	}
	return first
}

func removeOwnedDaemonFile(path string, pid int) error {
	content, err := readDaemonFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(content)) != strconv.Itoa(pid) {
		return fmt.Errorf("refuse to remove daemon file now owned by PID %s", strings.TrimSpace(string(content)))
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func CreateDaemonLock(path string) (func() error, error) {
	if strings.TrimSpace(path) == "" {
		return func() error { return nil }, nil
	}
	if err := os.MkdirAll(filepathDir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create daemon lock directory: %w", err)
	}
	for attempts := 0; attempts < 2; attempts++ {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, err := fmt.Fprintf(file, "%d\n", os.Getpid()); err != nil {
				file.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("write daemon lock: %w", err)
			}
			if err := file.Close(); err != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("close daemon lock: %w", err)
			}
			owner := strconv.Itoa(os.Getpid())
			return func() error {
				contents, err := readDaemonFile(path)
				if os.IsNotExist(err) {
					return nil
				}
				if err != nil {
					return fmt.Errorf("read daemon lock during release: %w", err)
				}
				if strings.TrimSpace(string(contents)) != owner {
					return fmt.Errorf("refuse to remove daemon lock now owned by PID %s", strings.TrimSpace(string(contents)))
				}
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("remove daemon lock: %w", err)
				}
				return nil
			}, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("create daemon lock: %w", err)
		}
		contents, readErr := readDaemonFile(path)
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(contents)))
		if readErr != nil || parseErr != nil || pid <= 0 {
			return nil, fmt.Errorf("DearMachine daemon lock %s is invalid; inspect it before retrying", path)
		}
		signalErr := hostos.Signal(pid, 0)
		if signalErr == nil || errors.Is(signalErr, syscall.EPERM) {
			return nil, fmt.Errorf("DearMachine daemon PID %d is already running", pid)
		}
		if !errors.Is(signalErr, syscall.ESRCH) {
			return nil, fmt.Errorf("check DearMachine daemon PID %d: %w", pid, signalErr)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove stale daemon lock: %w", err)
		}
	}
	return nil, errors.New("could not acquire DearMachine daemon lock")
}

// filepathDir is intentionally narrow so lock creation never treats an empty
// parent as a broad filesystem target.
func filepathDir(path string) string {
	index := strings.LastIndex(path, string(os.PathSeparator))
	if index <= 0 {
		return "."
	}
	return path[:index]
}
