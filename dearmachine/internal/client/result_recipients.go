package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
)

// Freeze eligible generations at authenticated instruction acceptance, before
// execution or attachment preparation. A later reinvitation must not subscribe
// that new grant to an older instruction's answer.
func (r *InboxRouter) snapshotGuestRecipients(pairID string, message Message) error {
	if r.guests == nil {
		return nil
	}
	if !message.authenticated {
		return ErrMessageUnauthenticated
	}
	grants, err := r.guests.List(pairID)
	if err != nil {
		return err
	}
	generations := map[string]int64{}
	sender, _ := canonicalMessageAddress(message.From)
	for _, g := range grants {
		if g.Active && g.InboxID == guestInboxKey(r.inbox) && g.ThreadID == message.ThreadID &&
			g.Address != r.inbox.Address && !r.controllers[g.Address] && (g.Address == sender || visibleRecipient(message, g.Address)) {
			generations[g.Address] = g.Generation
		}
	}
	encoded, err := json.Marshal(generations)
	if err != nil {
		return err
	}
	_, err = r.guests.db.Exec(`INSERT OR IGNORE INTO guest_recipient_grants VALUES(?,?,?,?)`, pairID, guestInboxKey(r.inbox), message.MessageID, string(encoded))
	return err
}

// Result recipients come from the instruction being answered, never from its
// private approval message or the accumulated membership of a conversation.
func (e *pairEndpoint) resultRecipients(message Message) (ReplyPayload, error) {
	r := e.router
	r.mu.Lock()
	defer r.mu.Unlock()
	owner := r.pairs[e.pairID].UserEmail
	payload := ReplyPayload{To: []string{owner}}
	if r.guests == nil {
		return payload, nil
	}
	grants, err := r.guests.List(e.pairID)
	if err != nil {
		return payload, err
	}
	var encoded string
	err = r.guests.db.QueryRow(`SELECT generations FROM guest_recipient_grants WHERE pair_id=? AND inbox_id=? AND message_id=?`, e.pairID, guestInboxKey(r.inbox), message.MessageID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return payload, nil // Pre-upgrade work has no safe recipient snapshot.
	}
	if err != nil {
		return payload, err
	}
	var generations map[string]int64
	if err := json.Unmarshal([]byte(encoded), &generations); err != nil {
		return payload, err
	}
	sender, _ := canonicalMessageAddress(message.From)
	for _, g := range grants {
		if guestRecipientEligible(g.Active, g.InboxID == guestInboxKey(r.inbox) && g.ThreadID == message.ThreadID,
			g.Address == sender || visibleRecipient(message, g.Address), generations[g.Address] == g.Generation) &&
			g.Address != r.inbox.Address && !r.controllers[g.Address] {
			payload.CC = append(payload.CC, g.Address)
		}
	}
	sort.Strings(payload.CC)
	return payload, nil
}

type resultEnvelope struct{ To, CC []string }

func (s *Store) resultEnvelope(messageID string) (resultEnvelope, bool, error) {
	var encoded string
	err := s.db.QueryRow(`SELECT envelope FROM result_recipients WHERE message_id=?`, messageID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return resultEnvelope{}, false, nil
	}
	if err != nil {
		return resultEnvelope{}, false, err
	}
	var envelope resultEnvelope
	err = json.Unmarshal([]byte(encoded), &envelope)
	return envelope, err == nil, err
}
func (a *App) resultReplyPayload(message Message, payload ReplyPayload) (ReplyPayload, error) {
	selector, ok := a.transport.(interface {
		resultRecipients(Message) (ReplyPayload, error)
	})
	if !ok {
		return payload, nil
	}
	envelope, found, err := a.store.resultEnvelope(message.MessageID)
	if err != nil {
		return payload, err
	}
	if !found {
		selected, err := selector.resultRecipients(message)
		if err != nil {
			return payload, err
		}
		encoded, err := json.Marshal(resultEnvelope{selected.To, selected.CC})
		if err != nil {
			return payload, err
		}
		// Persist only the envelope immediately before the first submission. An
		// uncertain send is retried with this same envelope and idempotency key.
		if _, err = a.store.db.Exec(`INSERT OR IGNORE INTO result_recipients(message_id,envelope) VALUES(?,?)`, message.MessageID, string(encoded)); err != nil {
			return payload, err
		}
		envelope, _, err = a.store.resultEnvelope(message.MessageID)
		if err != nil {
			return payload, err
		}
	}
	payload.To = envelope.To
	payload.CC = envelope.CC
	payload.BCC = nil
	if e, ok := a.transport.(*pairEndpoint); ok && len(payload.CC) == 0 {
		footer, err := e.guestRemovalFooter(message.ThreadID, "")
		if err != nil {
			return payload, err
		}
		payload.Text += footer
		if payload.HTML != "" {
			payload.HTML = replyHTML(payload.Text)
		}
	}
	return payload, nil
}

func (a *App) resultReplyReceipt(ctx context.Context, message Message) (string, bool, error) {
	envelope, found, err := a.store.resultEnvelope(message.MessageID)
	if err != nil {
		return "", false, err
	}
	if e, ok := a.transport.(*pairEndpoint); ok && found {
		messages, err := e.router.raw.Thread(ctx, message.ThreadID)
		if err != nil {
			return "", false, err
		}
		seen := false
		for _, candidate := range messages {
			if candidate.MessageID == message.MessageID {
				seen = true
				continue
			}
			repliesToOriginal := candidate.InReplyTo == message.MessageID || (candidate.InReplyTo == "" && seen)
			sender, senderErr := canonicalMessageAddress(candidate.From)
			if senderErr == nil && sender == e.router.inbox.Address && repliesToOriginal && candidate.ThreadID == message.ThreadID && containsFold(candidate.Labels, "sent") && sameRecipientSet(candidate.To, envelope.To) && sameRecipientSet(candidate.CC, envelope.CC) && len(candidate.BCC) == 0 {
				return candidate.MessageID, true, nil
			}
		}
		return "", false, nil
	}
	return a.transport.ReplyReceipt(ctx, message, "")
}
func sameRecipientSet(a, b []string) bool {
	canonical := func(values []string) map[string]bool {
		out := map[string]bool{}
		for _, v := range values {
			x, err := canonicalMessageAddress(v)
			if err != nil {
				return nil
			}
			out[x] = true
		}
		return out
	}
	aa, bb := canonical(a), canonical(b)
	if aa == nil || bb == nil || len(aa) != len(bb) {
		return false
	}
	for v := range aa {
		if !bb[v] {
			return false
		}
	}
	return true
}
