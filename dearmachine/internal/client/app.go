package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dearmachine/dearmachine/internal/synctrigger"
)

type App struct {
	transport        Transport
	store            *Store
	runner           *AgentRunner
	syncOrchestrator *synctrigger.Orchestrator
	concurrency      int
	pollInterval     time.Duration
	logger           *log.Logger
	verbose          bool
	pidfile          string
	responseTier     ResponseTier
	minimalFooter    bool
	preemptionNow    func() time.Time
	preemptionAfter  func(time.Duration) <-chan time.Time
	statsMu          sync.Mutex
	processed        int
	threads          map[string]struct{}
	workerGate       chan struct{}
}

// SetMinimalFooter keeps the session reference while suppressing DearMachine
// branding and the optional Magnifica Humanitas quote.
func (a *App) SetMinimalFooter(enabled bool) {
	a.minimalFooter = enabled
}

func New(
	transport Transport,
	store *Store,
	runner *AgentRunner,
	syncOrchestrator *synctrigger.Orchestrator,
	concurrency int,
	pollInterval time.Duration,
	logger *log.Logger,
	verbose bool,
	pidfile string,
	responseTier ResponseTier,
) (*App, error) {
	if transport == nil {
		return nil, fmt.Errorf("transport is required")
	}
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if runner == nil {
		return nil, fmt.Errorf("agent runner is required")
	}
	if concurrency < 1 {
		return nil, fmt.Errorf("concurrency must be at least 1")
	}
	if pollInterval <= 0 {
		return nil, fmt.Errorf("poll interval must be positive")
	}
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}
	if responseTier == "" {
		responseTier = TierPlain
	}
	if _, err := ParseResponseTier(string(responseTier)); err != nil {
		return nil, err
	}
	store.warnings = logger
	return &App{
		transport:        transport,
		store:            store,
		runner:           runner,
		syncOrchestrator: syncOrchestrator,
		concurrency:      concurrency,
		pollInterval:     pollInterval,
		logger:           logger,
		verbose:          verbose,
		pidfile:          pidfile,
		responseTier:     responseTier,
		preemptionNow:    time.Now,
		preemptionAfter:  time.After,
		threads:          make(map[string]struct{}),
	}, nil
}

func (a *App) Run(ctx context.Context) (runErr error) {
	cleanup, err := a.start(ctx)
	if err != nil {
		return err
	}
	defer func() {
		runErr = errors.Join(runErr, cleanup())
	}()
	return a.runLoop(ctx)
}

func (a *App) runLoop(ctx context.Context) error {
	a.logger.Printf(
		"DearMachine Client started. Polling %s every %s. Project: %s.",
		a.pollTarget(),
		a.pollInterval,
		a.runner.projectDir,
	)

	for {
		if err := a.ProcessOnce(ctx); err != nil {
			if ctx.Err() != nil {
				a.logShutdown()
				return nil
			}
			return err
		}
		if a.syncOrchestrator != nil {
			if err := a.orchestrateWhilePolling(ctx); err != nil {
				if ctx.Err() != nil {
					a.logShutdown()
					return nil
				}
				return err
			}
		}

		timer := time.NewTimer(a.pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			a.logShutdown()
			return nil
		case <-timer.C:
		}
	}
}

func (a *App) orchestrateWhilePolling(ctx context.Context) error {
	maintenanceContext, cancelMaintenance := context.WithCancel(ctx)
	defer cancelMaintenance()
	maintenanceDone := make(chan error, 1)
	go func() {
		maintenanceDone <- a.syncOrchestrator.OrchestrateSync(maintenanceContext)
	}()

	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case err := <-maintenanceDone:
			if err != nil && ctx.Err() == nil {
				a.logger.Printf("sync trigger: %v", err)
			}
			return nil
		case <-ticker.C:
			// Maintenance owns the repository execution lane, but polling and
			// BeginMessage remain safe. Discarding this in-memory queue leaves each
			// claim durable for recovery and dispatch after maintenance completes.
			if err := a.pollAndClaim(ctx, newThreadWorkQueue()); err != nil {
				cancelMaintenance()
				maintenanceErr := <-maintenanceDone
				if maintenanceErr != nil && !errors.Is(maintenanceErr, context.Canceled) {
					a.logger.Printf("sync trigger: %v", maintenanceErr)
				}
				return err
			}
		case <-ctx.Done():
			cancelMaintenance()
			<-maintenanceDone
			return ctx.Err()
		}
	}
}

func (a *App) RunOnce(ctx context.Context) (runErr error) {
	cleanup, err := a.start(ctx)
	if err != nil {
		return err
	}
	defer func() {
		runErr = errors.Join(runErr, cleanup())
	}()
	return a.ProcessOnce(ctx)
}

func (a *App) start(ctx context.Context) (func() error, error) {
	if err := a.runner.Sync(ctx); err != nil {
		return nil, err
	}
	cleanup, err := createPIDFile(a.pidfile)
	if err != nil {
		return nil, err
	}
	return cleanup, nil
}

func (a *App) ProcessOnce(ctx context.Context) error {
	work := newThreadWorkQueue()
	if err := a.recoverPending(ctx, work); err != nil {
		return err
	}
	if err := a.pollAndClaim(ctx, work); err != nil {
		return err
	}
	return a.dispatch(ctx, work)
}

func (a *App) pollAndClaim(ctx context.Context, work *threadWorkQueue) error {
	messages, err := a.transport.Poll(ctx)
	if err != nil {
		return err
	}
	if a.verbose {
		a.logger.Printf("poll: %d unread messages", len(messages))
	}
	for _, message := range messages {
		original := message
		preserveForward := false
		message, conversationReference := prepareInboundMessage(message)
		if containsFold(message.Labels, "sent") {
			if a.verbose {
				a.logger.Printf("poll: locally skipped own outbound message=%s thread=%s", message.MessageID, message.ThreadID)
			}
			if err := a.transport.MarkProcessed(ctx, message.MessageID); err != nil {
				a.logger.Printf("poll: clear own outbound message=%s thread=%s: %v", message.MessageID, message.ThreadID, err)
			}
			continue
		}
		forwardRequest, hasForwardRequest, err := a.store.ForwardRequestForThread(message.ThreadID)
		if err != nil {
			return err
		}
		if hasForwardRequest {
			if err := a.handleForwardRequest(ctx, original, forwardRequest, work); err != nil {
				return err
			}
			continue
		}
		skipped, err := a.store.IsSkipped(message.MessageID)
		if err != nil {
			return err
		}
		if skipped {
			if a.verbose {
				a.logger.Printf(
					"poll: locally skipped message=%s thread=%s",
					message.MessageID,
					message.ThreadID,
				)
			}
			continue
		}
		seen, err := a.store.Seen(message.MessageID)
		if err != nil {
			return err
		}
		if seen {
			if err := a.transport.MarkProcessed(ctx, message.MessageID); err != nil {
				return err
			}
			continue
		}
		knownThread, err := a.store.KnownThread(message.ThreadID)
		if err != nil {
			return err
		}
		topLevelForward := hasTopLevelForwardStructure(original)
		if topLevelForward || (!knownThread && strings.TrimSpace(original.InReplyTo) == "" && len(original.References) == 0) {
			forwardReferences, detectionErr := forwardedConversationReferences(ctx, a.transport, original)
			if detectionErr != nil {
				a.logger.Printf("forward detection skipped message=%s: %v", message.MessageID, detectionErr)
			} else if len(forwardReferences) > 0 {
				preserveForward = true
				candidates, err := a.store.ResolveConversationReferences(forwardReferences)
				if err != nil {
					return err
				}
				if len(candidates) > 0 {
					request, _, err := a.store.BeginForwardRequest(
						message.MessageID,
						message.ThreadID,
						candidates,
					)
					if err != nil {
						return err
					}
					if err := a.ensureForwardPrompt(ctx, original, request); err != nil {
						return err
					}
					continue
				}
			}
			preserveForward = preserveForward || topLevelForward
		}
		if preserveForward {
			conversationReference = ""
			message.ConversationReferences = nil
			if message.RawBody != "" {
				message.Body = message.RawBody
			}
		}
		pending, existed, err := a.store.BeginMessageWithReference(
			message.MessageID,
			message.ThreadID,
			conversationReference,
			a.responseTier,
		)
		if errors.Is(err, errMessageSkipped) {
			if a.verbose {
				a.logger.Printf(
					"poll: locally skipped message=%s thread=%s during claim",
					message.MessageID,
					message.ThreadID,
				)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("claim message %s: %w", message.MessageID, err)
		}
		if preserveForward {
			if err := a.store.PreservePendingOriginalBody(message.MessageID); err != nil {
				return err
			}
			pending.PreserveOriginalBody = true
		}
		work.enqueue(messageWork{message: message, pending: pending, recovering: existed})
	}
	return nil
}

func (a *App) ensureForwardPrompt(ctx context.Context, message Message, request ForwardRequest) error {
	if request.PromptMessageID == "" {
		outboundMessageID, found, err := a.transport.ReplyReceipt(ctx, message)
		if err != nil {
			return err
		}
		if !found {
			outboundMessageID, err = a.transport.Reply(
				ctx,
				message.MessageID,
				ReplyPayload{Text: initialForwardPrompt(request)},
				controlIdempotencyKey("forward-prompt", message.MessageID),
			)
			if err != nil {
				return err
			}
		}
		if err := a.store.SetForwardPromptReceipt(request.RequestMessageID, outboundMessageID); err != nil {
			return err
		}
	}
	return a.transport.MarkProcessed(ctx, message.MessageID)
}

func (a *App) handleForwardRequest(
	ctx context.Context,
	message Message,
	request ForwardRequest,
	work *threadWorkQueue,
) error {
	if message.MessageID == request.RequestMessageID {
		return a.ensureForwardPrompt(ctx, message, request)
	}
	seen, err := a.store.Seen(message.MessageID)
	if err != nil {
		return err
	}
	if seen {
		return a.transport.MarkProcessed(ctx, message.MessageID)
	}
	control := parseForwardControl(authoredControlBody(message))
	if request.State == forwardAwaitingSelection {
		switch control.Kind {
		case forwardControlNo:
			return a.resolveForwardRequest(ctx, message, request, false, work)
		case forwardControlCancel:
			if err := a.store.CancelForwardRequest(request.ExternalThreadID, message.MessageID); err != nil {
				return err
			}
			return a.transport.MarkProcessed(ctx, message.MessageID)
		case forwardControlSelection:
			if control.Selection <= len(request.CandidateIDs) {
				selected := request.CandidateIDs[control.Selection-1]
				outbound, err := a.replyForwardControl(
					ctx,
					message,
					forwardConfirmationPrompt(selected),
					"forward-selection",
				)
				if err != nil {
					return err
				}
				if err := a.store.SelectForwardCandidate(
					request.RequestMessageID,
					request.ExternalThreadID,
					message.MessageID,
					selected,
					outbound,
				); err != nil {
					return err
				}
				return a.transport.MarkProcessed(ctx, message.MessageID)
			}
		}
		return a.repeatForwardPrompt(ctx, message, request)
	}

	switch control.Kind {
	case forwardControlYes:
		return a.resolveForwardRequest(ctx, message, request, true, work)
	case forwardControlNo:
		return a.resolveForwardRequest(ctx, message, request, false, work)
	case forwardControlCancel:
		if err := a.store.CancelForwardRequest(request.ExternalThreadID, message.MessageID); err != nil {
			return err
		}
		return a.transport.MarkProcessed(ctx, message.MessageID)
	default:
		return a.repeatForwardPrompt(ctx, message, request)
	}
}

func (a *App) repeatForwardPrompt(ctx context.Context, message Message, request ForwardRequest) error {
	outbound, err := a.replyForwardControl(
		ctx,
		message,
		invalidForwardPrompt(request),
		"forward-invalid",
	)
	if err != nil {
		return err
	}
	if err := a.store.RecordControlMessage(message.MessageID, message.ThreadID, outbound); err != nil {
		return err
	}
	return a.transport.MarkProcessed(ctx, message.MessageID)
}

func (a *App) replyForwardControl(
	ctx context.Context,
	message Message,
	text, kind string,
) (string, error) {
	outbound, found, err := a.transport.ReplyReceipt(ctx, message)
	if err != nil {
		return "", err
	}
	if found {
		return outbound, nil
	}
	return a.transport.Reply(
		ctx,
		message.MessageID,
		ReplyPayload{Text: text},
		controlIdempotencyKey(kind, message.MessageID),
	)
}

func (a *App) resolveForwardRequest(
	ctx context.Context,
	control Message,
	request ForwardRequest,
	fork bool,
	work *threadWorkQueue,
) error {
	pending, err := a.store.MaterializeForwardRequest(
		request.ExternalThreadID,
		control.MessageID,
		fork,
		a.responseTier,
	)
	if err != nil {
		return err
	}
	original, err := a.transport.Message(ctx, request.RequestMessageID)
	if err != nil {
		return err
	}
	if pending.PreserveOriginalBody {
		if original.RawBody != "" {
			original.Body = original.RawBody
		}
		original.ConversationReferences = nil
	} else {
		original = prepareForwardForkMessage(original)
	}
	work.enqueuePriority(messageWork{message: original, pending: pending})
	return a.transport.MarkProcessed(ctx, control.MessageID)
}

func controlIdempotencyKey(kind, messageID string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + messageID))
	return fmt.Sprintf("dearmachine-control-%x", sum[:])
}

func (a *App) recoverPending(ctx context.Context, work *threadWorkQueue) error {
	pendingMessages, err := a.store.Pending()
	if err != nil {
		return err
	}
	grouped := make(map[string][]PendingMessage)
	for _, pending := range pendingMessages {
		grouped[pending.ThreadID] = append(grouped[pending.ThreadID], pending)
	}
	for threadID := range grouped {
		sort.SliceStable(grouped[threadID], func(left, right int) bool {
			return grouped[threadID][left].Session.Sequence <
				grouped[threadID][right].Session.Sequence
		})
	}
	groupIndexes := make(map[string]int, len(grouped))
	// Preserve the store's global recovery order while replacing each thread's
	// slots with its sequence-sorted items. The dispatcher admits only the first
	// eligible item from any one of these thread groups at a time.
	for _, slot := range pendingMessages {
		index := groupIndexes[slot.ThreadID]
		pending := grouped[slot.ThreadID][index]
		groupIndexes[slot.ThreadID] = index + 1
		message, err := a.transport.Message(ctx, pending.MessageID)
		if err != nil {
			return err
		}
		message = prepareMessageForPending(message, pending)
		work.enqueue(messageWork{message: message, pending: pending, recovering: true})
	}
	return nil
}

func (a *App) processWork(ctx context.Context, work messageWork, started func()) error {
	pending, found, err := a.store.PendingByID(work.pending.MessageID)
	if err != nil {
		return err
	}
	if !found {
		skipped, err := a.store.IsSkipped(work.pending.MessageID)
		if err != nil {
			return err
		}
		if skipped {
			return nil
		}
		seen, err := a.store.Seen(work.pending.MessageID)
		if err != nil {
			return err
		}
		if seen {
			return a.transport.MarkProcessed(ctx, work.pending.MessageID)
		}
		return fmt.Errorf("pending message disappeared before dispatch")
	}
	err = a.processPending(ctx, work.message, pending, work.recovering, started)
	if errors.Is(err, ErrGracefullyStopped) {
		return nil
	}
	return err
}

func (a *App) processPending(
	ctx context.Context,
	message Message,
	pending PendingMessage,
	recovering bool,
	started func(),
) error {
	message = prepareMessageForPending(message, pending)
	if recovering {
		outboundMessageID, found, err := a.transport.ReplyReceipt(ctx, message)
		if err != nil {
			return err
		}
		if found {
			if err := a.store.Complete(
				message.MessageID,
				sessionStatus(pending.ResultKind),
				outboundMessageID,
			); err != nil {
				return err
			}
			a.recordProcessed(message.ThreadID)
			if err := a.transport.MarkProcessed(ctx, message.MessageID); err != nil {
				return err
			}
			a.logger.Printf(
				"recovered receipt message=%s thread=%s session=%s outbound=%s",
				message.MessageID,
				message.ThreadID,
				pending.Session.SessionID,
				outboundMessageID,
			)
			if err := a.cleanupStagingDirs(pending); err != nil {
				a.logger.Printf(
					"staging cleanup failed message=%s thread=%s error=%v",
					message.MessageID,
					message.ThreadID,
					err,
				)
			}
			return nil
		}
	}

	if pending.State == messageReceived && pending.ForkedFromSessionID != "" {
		if err := a.runner.EnsureForkSession(
			ctx,
			pending.ForkedFromSessionID,
			pending.Session.SessionID,
		); err != nil {
			return fmt.Errorf("fork referenced session: %w", err)
		}
		pending.Session.IsNew = false
	}
	prompt := formatPromptMode(message, pending.Session, pending.PreserveOriginalBody)
	finalPath := recoveryResultPath(pending.Session.SessionID, message.MessageID)
	tc, err := a.turnContext(ctx, pending)
	if err != nil {
		return err
	}
	if pending.State == messageReceived {
		if err := a.resetTurnDirs(pending); err != nil {
			return err
		}
		tier := a.tierFor(pending)
		if tier != TierPlain && len(message.Attachments) > 0 {
			if _, _, err := StageInbox(
				ctx,
				a.transport,
				a.runner.projectDir,
				TurnKey(pending.Session.Sequence, message.MessageID),
				message.Attachments,
				DefaultAttachmentLimits(),
			); err != nil {
				return fmt.Errorf("stage inbound attachments: %w", err)
			}
		}
	}
	var result RunResult
	switch pending.State {
	case messageReceived:
		checkpointSessionID := ""
		if !pending.Session.IsNew && pending.ForkedFromSessionID == "" {
			checkpointSessionID, err = a.runner.ForkSession(ctx, pending.Session.SessionID, "")
			if err != nil {
				return fmt.Errorf("checkpoint committed agent session before follow-up: %w", err)
			}
		}
		if err := a.store.MarkRunningWithCheckpoint(
			message.MessageID,
			prompt,
			checkpointSessionID,
		); err != nil {
			if checkpointSessionID != "" {
				cleanupErr := a.runner.DeleteSession(ctx, checkpointSessionID)
				if cleanupErr != nil {
					cleanupErr = fmt.Errorf(
						"clean unused agent checkpoint %s: %w",
						checkpointSessionID,
						cleanupErr,
					)
				}
				return errors.Join(err, cleanupErr)
			}
			return err
		}
		pending.CheckpointSessionID = checkpointSessionID
		result, err = a.runner.runObserved(ctx, pending.Session, prompt, finalPath, tc, started)
	case messageRunning:
		result, err = a.runner.recoverObserved(
			ctx,
			pending.Session,
			pending.Prompt,
			finalPath,
			tc,
			started,
		)
	case messageResultReady:
		result = RunResult{
			Kind: pending.ResultKind,
			Text: pending.ResultText,
		}
	default:
		return fmt.Errorf("unsupported pending message state %q", pending.State)
	}
	if err != nil {
		return err
	}
	outboundFiles, outboundManifest, manifestJSON, err := a.outboundForState(ctx, pending, tc)
	if err != nil {
		return err
	}
	if pending.State != messageResultReady {
		if err := a.store.StoreResultWithManifest(message.MessageID, result, manifestJSON); err != nil {
			return err
		}
		if err := os.Remove(finalPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove durable agent result: %w", err)
		}
	}

	footerQuote := result.MagnificaHumanitas
	if footerQuote == nil {
		footerQuote = pending.MagnificaHumanitas
	}
	replyText := appendConversationFooterMode(
		result.Text,
		pending.Session.SessionID,
		a.minimalFooter,
		footerQuote,
	)
	payload := ReplyPayload{Text: replyText}
	tier := a.tierFor(pending)
	if tier != TierPlain {
		payload.HTML = replyHTML(replyText)
	}
	switch tier {
	case TierFormatted:
		payload.Files = outboundFiles
	case TierComplete:
		if outboundManifest != nil && len(outboundFiles) > 0 {
			archive, err := BuildArchive(*outboundManifest, outboundFiles)
			if err != nil {
				return fmt.Errorf("build attachment archive: %w", err)
			}
			payload.Files = []OutboundFile{{
				Filename:    AttachmentArchiveName(),
				ContentType: "application/zip",
				Contents:    archive,
			}}
		}
	}
	key := idempotencyKey(pending.Session.SessionID, message.MessageID)
	outboundMessageID, err := a.transport.Reply(ctx, message.MessageID, payload, key)
	if err != nil {
		return err
	}

	if err := a.store.Complete(
		message.MessageID,
		sessionStatus(result.Kind),
		outboundMessageID,
	); err != nil {
		return err
	}
	if err := a.cleanupStagingDirs(pending); err != nil {
		a.logger.Printf(
			"staging cleanup failed message=%s thread=%s error=%v",
			message.MessageID,
			message.ThreadID,
			err,
		)
	}
	a.recordProcessed(message.ThreadID)
	if err := os.Remove(finalPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove durable agent result: %w", err)
	}
	if err := a.transport.MarkProcessed(ctx, message.MessageID); err != nil {
		return err
	}
	if pending.CheckpointSessionID != "" {
		if err := a.runner.DeleteSession(ctx, pending.CheckpointSessionID); err != nil {
			a.logger.Printf(
				"checkpoint cleanup failed message=%s session=%s checkpoint=%s error=%v",
				message.MessageID,
				pending.Session.SessionID,
				pending.CheckpointSessionID,
				err,
			)
		}
	}

	a.logger.Printf(
		"processed message=%s thread=%s session=%s result=%s",
		message.MessageID,
		message.ThreadID,
		pending.Session.SessionID,
		result.Kind,
	)
	return nil
}

func (a *App) turnContext(ctx context.Context, pending PendingMessage) (TurnContext, error) {
	turnKey := TurnKey(pending.Session.Sequence, pending.MessageID)
	dirs, err := stagePaths(a.runner.projectDir, turnKey)
	if err != nil {
		return TurnContext{}, fmt.Errorf("prepare attachment staging: %w", err)
	}
	return TurnContext{
		InboxPath:    filepath.Join(dirs.Inbox, InboundManifestName()),
		OutboxPath:   dirs.Outbox,
		ManifestPath: filepath.Join(dirs.Inbox, InboundManifestName()),
		Tier:         string(a.tierFor(pending)),
	}, nil
}

func (a *App) tierFor(pending PendingMessage) ResponseTier {
	tier := pending.Session.ResponseTier
	if tier == "" {
		tier = a.responseTier
	}
	if tier == "" {
		tier = TierPlain
	}
	return tier
}

func (a *App) resetTurnDirs(pending PendingMessage) error {
	turnKey := TurnKey(pending.Session.Sequence, pending.MessageID)
	inbox := filepath.Join(a.runner.projectDir, ".attachments-inbox", turnKey)
	outbox := filepath.Join(a.runner.projectDir, ".attachments-outbox", turnKey)
	if err := os.RemoveAll(inbox); err != nil {
		return fmt.Errorf("reset inbound staging: %w", err)
	}
	if err := os.RemoveAll(outbox); err != nil {
		return fmt.Errorf("reset outbound staging: %w", err)
	}
	_, err := stagePaths(a.runner.projectDir, turnKey)
	return err
}

func (a *App) outboundForState(
	ctx context.Context,
	pending PendingMessage,
	tc TurnContext,
) ([]OutboundFile, *OutboundManifest, string, error) {
	tier := a.tierFor(pending)
	if tier == TierPlain {
		return nil, nil, "", nil
	}
	switch pending.State {
	case messageReceived, messageRunning:
		files, manifest, err := CollectOutbox(
			ctx,
			StagingDirs{Outbox: tc.OutboxPath},
			DefaultAttachmentLimits(),
		)
		if err != nil {
			return nil, nil, "", fmt.Errorf("collect outbox: %w", err)
		}
		payload, err := json.Marshal(manifest)
		if err != nil {
			return nil, nil, "", fmt.Errorf("encode outbound manifest: %w", err)
		}
		return files, manifest, string(payload), nil
	case messageResultReady:
		if strings.TrimSpace(pending.ResultManifest) == "" {
			return nil, nil, "", nil
		}
		var manifest OutboundManifest
		if err := json.Unmarshal([]byte(pending.ResultManifest), &manifest); err != nil {
			return nil, nil, "", fmt.Errorf("parse persisted outbound manifest: %w", err)
		}
		files, err := VerifyAndLoadOutbox(ctx, StagingDirs{Outbox: tc.OutboxPath}, manifest)
		if err != nil {
			return nil, nil, "", fmt.Errorf("verify outbound files: %w", err)
		}
		return files, &manifest, pending.ResultManifest, nil
	default:
		return nil, nil, "", fmt.Errorf("unsupported pending message state %q", pending.State)
	}
}

func (a *App) cleanupStagingDirs(pending PendingMessage) error {
	turnKey := TurnKey(pending.Session.Sequence, pending.MessageID)
	inbox := filepath.Join(a.runner.projectDir, ".attachments-inbox", turnKey)
	outbox := filepath.Join(a.runner.projectDir, ".attachments-outbox", turnKey)
	var first error
	for _, path := range []string{inbox, outbox} {
		if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
			first = errors.Join(first, err)
		}
	}
	return first
}

func replyHTML(text string) string {
	return "<!DOCTYPE html>\n<html><body><p style=\"white-space: pre-wrap\">" +
		html.EscapeString(text) + "</p></body></html>\n"
}

func (a *App) recordProcessed(threadID string) {
	a.statsMu.Lock()
	defer a.statsMu.Unlock()
	a.processed++
	a.threads[threadID] = struct{}{}
}

func (a *App) logShutdown() {
	a.statsMu.Lock()
	processed := a.processed
	threads := len(a.threads)
	a.statsMu.Unlock()
	a.logger.Printf(
		"DearMachine Client shutting down. Processed %d messages across %d threads.",
		processed,
		threads,
	)
}

func sessionStatus(kind ResultKind) string {
	if kind == ResultQuestion {
		return "suspended_user_input"
	}
	return "completed"
}

func recoveryResultPath(sessionID, messageID string) string {
	return filepath.Join(
		os.TempDir(),
		"dearmachine-machtiani-results",
		idempotencyKey(sessionID, messageID)+".md",
	)
}

func formatPrompt(
	message Message,
	session Session,
) string {
	return formatPromptMode(message, session, false)
}

func formatPromptMode(message Message, session Session, preserveOriginalBody bool) string {
	if preserveOriginalBody {
		if message.RawBody != "" {
			message.Body = message.RawBody
		}
	} else {
		message, _ = prepareInboundMessage(message)
	}
	var prompt strings.Builder
	fmt.Fprintf(
		&prompt,
		"[Dear Machine, email from %s received at %s]\n",
		message.From,
		messageTime(message).UTC().Format(time.RFC3339),
	)
	fmt.Fprintf(
		&prompt,
		"[Thread: %s | Session: %s | Sequence: %d]\n",
		message.ThreadID,
		session.SessionID,
		session.Sequence,
	)
	tier := session.ResponseTier
	if tier == "" {
		tier = TierPlain
	}
	fmt.Fprintf(&prompt, "[Response tier: %s]\n", tier)
	if len(message.Attachments) > 0 {
		for _, ref := range message.Attachments {
			fmt.Fprintf(
				&prompt,
				"[Attachment: %s (%s, %d bytes)]\n",
				ref.Filename,
				ref.ContentType,
				ref.SizeBytes,
			)
		}
		if tier == TierPlain {
			fmt.Fprintln(&prompt, "[Attachment bodies are not staged for the plain response tier]")
		}
	}
	prompt.WriteString("\n")
	prompt.WriteString(messageBody(message))
	prompt.WriteString("\n\n[End of email]")
	return prompt.String()
}

func prepareMessageForPending(message Message, pending PendingMessage) Message {
	if pending.PreserveOriginalBody {
		if message.RawBody != "" {
			message.Body = message.RawBody
		}
		message.ConversationReferences = nil
		return message
	}
	if pending.ForkedFromSessionID != "" {
		return prepareForwardForkMessage(message)
	}
	message, _ = prepareInboundMessage(message)
	return message
}

func (a *App) pollTarget() string {
	if target, ok := a.transport.(interface{ pollTarget() string }); ok {
		return target.pollTarget()
	}
	return "email"
}

func idempotencyKey(sessionID, messageID string) string {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + messageID))
	return fmt.Sprintf("dearmachine-%x", sum[:])
}
