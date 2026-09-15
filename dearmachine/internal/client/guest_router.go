package client

import (
	"context"
	"errors"
	"fmt"
)

var ErrMessageUnauthenticated = errors.New("message sender or authorization headers could not be authenticated")

func (r *InboxRouter) authenticateMessage(ctx context.Context, message Message) (Message, error) {
	message.authenticated, message.riskAccepted = false, false
	message.fingerprint = messageFingerprint(message)
	var err error
	if containsFold(message.Labels, "unauthenticated") {
		err = ErrMessageUnauthenticated
	} else if auth, ok := r.raw.(MessageAuthenticator); ok {
		err = auth.AuthenticateMessage(ctx, message)
	} else {
		err = ErrSenderAttributionUnsupported
	}
	if err == nil {
		message.authenticated = true
		return message, nil
	}
	if !errors.Is(err, ErrMessageUnauthenticated) {
		return Message{}, err
	}
	pair, grant, eligible, lookupErr := r.unverifiedGuestScope(message)
	if lookupErr != nil {
		return Message{}, lookupErr
	}
	if eligible {
		accepted, lookupErr := r.guests.authenticationAccepted(grant)
		if lookupErr != nil {
			return Message{}, lookupErr
		}
		if guestSenderEligible(false, accepted, grant.Active, pair.ID == grant.PairID, true) {
			// Bind before exposing content, including on direct retrieval after restart.
			if err := r.guests.bindUnverifiedWork(grant.GuestKey, message.MessageID, message.fingerprint); err != nil {
				return Message{}, err
			}
			message.riskAccepted = true
			return message, nil
		}
	}
	return Message{}, err
}

var ErrSenderAttributionUnsupported = errors.New("inbound sender authentication is unsupported by this transport")

// ConfigureGuests includes all registered inbox pairs, even unselected workers.
func (r *InboxRouter) ConfigureGuests(store *GuestStore, allPairs []Pair) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if store == nil {
		return errors.New("guest store is required")
	}
	r.guests = store
	r.controllers = make(map[string]bool)
	for _, pair := range allPairs {
		if pair.InboxID != r.inbox.ID {
			continue
		}
		pair, err := normalizePair(pair)
		if err != nil {
			return err
		}
		r.controllers[pair.UserEmail] = true
		if err = store.PermanentReceive(guestInboxKey(r.inbox), pair.UserEmail); err != nil {
			return err
		}
	}
	for _, pair := range r.pairs {
		r.controllers[pair.UserEmail] = true
	}
	return nil
}

func visibleRecipient(message Message, address string) bool {
	return containsCanonicalAddress(message.To, address) || containsCanonicalAddress(message.CC, address)
}
func (r *InboxRouter) guestAllowed(pair Pair, message Message) error {
	if r.guests == nil {
		return ErrGuestUnauthorized
	}
	key, err := guestKey(pair, r.inbox, message)
	if err != nil {
		return ErrGuestUnauthorized
	}
	if r.controllers[key.Address] {
		return ErrGuestUnauthorized
	}
	g, err := r.guests.Grant(key)
	if err != nil {
		return err
	}
	if !guestDeliveryEligible(g.Active, guestSenderEligible(message.authenticated, message.riskAccepted, g.Active, true, true), r.deliveredToInbox(message), visibleRecipient(message, r.inbox.Address), visibleRecipient(message, pair.UserEmail)) {
		return ErrGuestUnauthorized
	}
	// Existing work is immutable across generations. History which was never
	// work has no binding; it still needs the exact active grant and recipients.
	var exists bool
	err = r.guests.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM guest_work WHERE pair_id=? AND inbox_id=? AND message_id=?)`, key.PairID, key.InboxID, message.MessageID).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		if err := r.guests.CheckWork(key, message.MessageID); err != nil {
			return err
		}
		return r.guests.checkFingerprint(key, message.MessageID, message.fingerprint)
	}
	return nil
}

// Invite validates provider-resolved outer headers. Explicit CLI invocation is
// an operator authorization of this exact visible invitation, not a claim that
// From was cryptographically authenticated. Automatic detection additionally
// requires the adapter's message-authentication contract.
func (r *InboxRouter) Invite(ctx context.Context, pairID, messageID, address string) (GuestGrant, error) {
	message, err := r.raw.Message(ctx, messageID)
	if err != nil {
		return GuestGrant{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if message.MessageID != messageID {
		return GuestGrant{}, errors.New("provider returned a different invitation message ID")
	}
	message, err = r.normalizeDelivery(message)
	if err != nil {
		return GuestGrant{}, err
	}
	return r.allowInvitation(ctx, pairID, message, address, true)
}
func (r *InboxRouter) allowInvitation(ctx context.Context, pairID string, message Message, address string, explicit bool) (GuestGrant, error) {
	pair, found := r.pairs[pairID]
	if !found || r.guests == nil {
		return GuestGrant{}, errors.New("selected pair has no guest authorization store")
	}
	guest, err := canonicalMessageAddress(address)
	if err != nil {
		return GuestGrant{}, fmt.Errorf("guest address: %w", err)
	}
	from, err := canonicalMessageAddress(message.From)
	if err != nil {
		return GuestGrant{}, fmt.Errorf("invitation sender: %w", err)
	}
	attributed := explicit
	if !explicit {
		attributed = message.authenticated
	}
	distinct := guest != r.inbox.Address && guest != pair.UserEmail && !r.controllers[guest]
	if !guestInvitationEligible(from == pair.UserEmail, attributed, visibleRecipient(message, r.inbox.Address), visibleRecipient(message, guest), distinct, r.deliveredToInbox(message)) {
		return GuestGrant{}, errors.New("invitation requires the selected controller as outer sender and both Dear Machine and a non-paired guest visibly in To or CC")
	}
	return r.guests.Allow(GuestKey{pair.ID, guestInboxKey(r.inbox), guest, message.ThreadID}, message.MessageID, explicit)
}
func (r *InboxRouter) observeInvitations(ctx context.Context, pair Pair, message Message) error {
	if r.guests == nil || !visibleRecipient(message, r.inbox.Address) {
		return nil
	}
	from, err := canonicalMessageAddress(message.From)
	if err != nil || from != pair.UserEmail {
		return nil
	}
	for _, candidate := range append(append([]string{}, message.To...), message.CC...) {
		address, err := canonicalMessageAddress(candidate)
		if err != nil || address == r.inbox.Address || r.controllers[address] {
			continue
		}
		if _, err = r.allowInvitation(ctx, pair.ID, message, address, false); errors.Is(err, ErrSenderAttributionUnsupported) {
			return nil
		} else if err != nil {
			return err
		}
	}
	return nil
}
func (r *InboxRouter) ReconcileGuests(ctx context.Context) error {
	if r.guests == nil {
		return nil
	}
	p, ok := r.raw.(ReceiveAuthorizer)
	if !ok {
		return errors.New("provider receive reconciliation is unsupported")
	}
	return r.guests.Reconcile(ctx, guestInboxKey(r.inbox), p)
}

func (e *pairEndpoint) checkGuestRequest(request ParticipantRequest) error {
	if e.router.guests == nil {
		return ErrGuestUnauthorized
	}
	var bound bool
	err := e.router.guests.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM guest_message_fingerprints WHERE pair_id=? AND inbox_id=? AND message_id=? AND fingerprint<>'')`, e.pairID, guestInboxKey(e.router.inbox), request.RequestMessageID).Scan(&bound)
	if err != nil {
		return err
	}
	if !bound {
		return ErrGuestUnauthorized // Pre-upgrade requests need a fresh approval episode.
	}
	return e.router.guests.CheckWork(GuestKey{e.pairID, guestInboxKey(e.router.inbox), request.ParticipantAddress, request.ExternalThreadID}, request.RequestMessageID)
}

type guestStartContextKey struct{}
type guestStartGuard func(func() error) error

func (e *pairEndpoint) guestExecutionContext(ctx context.Context, message Message, pending PendingMessage) (context.Context, error) {
	r := e.router
	if !message.authenticated && !message.riskAccepted {
		return ctx, ErrGuestUnauthorized
	}
	if e.isControllingParticipant(message) {
		if r.guests == nil {
			return ctx, nil
		}
		key, bound, err := r.guests.BoundWorkKey(e.pairID, guestInboxKey(r.inbox), message.MessageID)
		if err != nil {
			return ctx, err
		}
		if !bound {
			return ctx, nil
		}
		guard := guestStartGuard(func(start func() error) error { return r.guests.WithWorkStart(key, message.MessageID, start) })
		return context.WithValue(ctx, guestStartContextKey{}, guard), r.guests.CheckWork(key, message.MessageID)
	}
	r.mu.Lock()
	err := r.authorize(e.pairID, message)
	r.mu.Unlock()
	if err != nil {
		return ctx, ErrGuestUnauthorized
	}
	key, err := guestKey(r.pairs[e.pairID], r.inbox, message)
	if err != nil {
		return ctx, err
	}
	if err = r.guests.CheckWork(key, message.MessageID); err != nil {
		return ctx, err
	}
	store := r.stores[e.pairID]
	if store == nil {
		return ctx, ErrGuestUnauthorized
	}
	approved, err := store.guestInstructionApproved(message.MessageID, key.Address, key.ThreadID, r.pairs[e.pairID].UserEmail)
	if err != nil {
		return ctx, err
	}
	decided := pending.Authority == authorityParticipant && approved
	if !guestExecutionEligible(true, true, true, guestSenderEligible(message.authenticated, message.riskAccepted, true, true, true), decided, pending.ControllingParticipant == r.pairs[e.pairID].UserEmail) {
		return ctx, ErrGuestUnauthorized
	}
	guard := guestStartGuard(func(start func() error) error {
		return r.guests.WithWorkStart(key, message.MessageID, func() error {
			approved, err := store.guestInstructionApproved(message.MessageID, key.Address, key.ThreadID, r.pairs[e.pairID].UserEmail)
			if err != nil {
				return err
			}
			decided := pending.Authority == authorityParticipant && approved
			if !guestExecutionEligible(true, true, true, guestSenderEligible(message.authenticated, message.riskAccepted, true, true, true), decided, pending.ControllingParticipant == r.pairs[e.pairID].UserEmail) {
				return ErrGuestUnauthorized
			}
			return start()
		})
	})
	return context.WithValue(ctx, guestStartContextKey{}, guard), nil
}

func (r *InboxRouter) RevokeGuest(pairID, address, threadID string, all bool) error {
	pair, ok := r.pairs[pairID]
	if !ok || r.guests == nil {
		return errors.New("guest pair is not configured")
	}
	canonical, err := canonicalMessageAddress(address)
	if err != nil {
		return err
	}
	return r.guests.Revoke(GuestKey{pair.ID, guestInboxKey(r.inbox), canonical, threadID}, all)
}

// RevokeGuest commits without constructing a provider transport.
func (s *GuestStore) RevokeGuest(pair Pair, inbox Inbox, address, threadID string, all bool) error {
	canonical, err := canonicalMessageAddress(address)
	if err != nil {
		return err
	}
	if pair.InboxID != inbox.ID {
		return errors.New("pair does not belong to selected inbox")
	}
	return s.Revoke(GuestKey{pair.ID, guestInboxKey(inbox), canonical, threadID}, all)
}
func (s *GuestStore) InboxPermissionStatus(inbox Inbox) ([]GuestPermissionStatus, error) {
	return s.PermissionStatus(guestInboxKey(inbox))
}
func (e *pairEndpoint) syncGuestPermissions(ctx context.Context) error {
	return e.router.ReconcileGuests(ctx)
}

func (e *pairEndpoint) bindGuestReplacement(request ParticipantRequest, messageID string) error {
	if e.router.guests == nil {
		return ErrGuestUnauthorized
	}
	return e.router.guests.BindReplacement(GuestKey{e.pairID, guestInboxKey(e.router.inbox), request.ParticipantAddress, request.ExternalThreadID}, request.RequestMessageID, messageID)
}
