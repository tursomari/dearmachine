package client

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const authenticationWarningHeading = "Dear Machine sender authentication warning"
const authenticationNoticeHourlyLimit = 3

// Scope is only a candidate for a warning, never evidence of the sender's identity.
func (r *InboxRouter) unverifiedGuestScope(m Message) (Pair, GuestGrant, bool, error) {
	if r.guests == nil || !r.deliveredToInbox(m) || !visibleRecipient(m, r.inbox.Address) || m.MessageID == "" {
		return Pair{}, GuestGrant{}, false, nil
	}
	from, err := canonicalMessageAddress(m.From)
	if err != nil || r.controllers[from] || from == r.inbox.Address {
		return Pair{}, GuestGrant{}, false, nil
	}
	var selected Pair
	var grant GuestGrant
	for _, p := range r.pairs {
		if !visibleRecipient(m, p.UserEmail) {
			continue
		}
		g, err := r.guests.Grant(GuestKey{p.ID, guestInboxKey(r.inbox), from, m.ThreadID})
		if err != nil {
			return Pair{}, GuestGrant{}, false, err
		}
		if !g.Active {
			continue
		}
		if selected.ID != "" {
			return Pair{}, GuestGrant{}, false, nil
		} // Ambiguous owner: fail closed.
		selected, grant = p, g
	}
	return selected, grant, selected.ID != "", nil
}

func (s *GuestStore) authenticationAccepted(g GuestGrant) (bool, error) {
	var accepted bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM guest_auth_exceptions WHERE `+grantWhere+` AND generation=? AND accepted=1)`, append(g.GuestKey.args(), g.Generation)...).Scan(&accepted)
	return accepted && g.Active, err
}

func authenticationReason(m Message) string {
	if containsFold(m.Labels, "unauthenticated") {
		return "provider marked message unauthenticated; sender identity could not be verified"
	}
	return "local verification could not establish an exact sender-domain DKIM signature covering the message and required headers"
}

func (r *InboxRouter) logAuthentication(m Message, pair Pair, outcome string, references ...string) {
	reference := ""
	if len(references) > 0 {
		reference = references[0]
	}
	if r.logger != nil {
		r.logger.Printf("guest_authentication message=%q thread=%q claimed_sender=%q owner=%q pair=%q reason=%q notification=%s reference=%q", m.MessageID, m.ThreadID, m.From, pair.UserEmail, pair.ID, authenticationReason(m), outcome, reference)
	}
}

// Persist outcome changes so repeated polls don't flood the diagnostic log.
func (r *InboxRouter) authenticationOutcome(m Message, pair Pair, outcome string) error {
	result, err := r.guests.db.Exec(`UPDATE guest_auth_messages SET outcome=? WHERE pair_id=? AND inbox_id=? AND message_id=? AND outcome<>?`, outcome, pair.ID, guestInboxKey(r.inbox), m.MessageID, outcome)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		var reference string
		if err := r.guests.db.QueryRow(`SELECT e.token FROM guest_auth_exceptions e JOIN guest_auth_messages m USING(pair_id,inbox_id,address,thread_id,generation) WHERE m.pair_id=? AND m.inbox_id=? AND m.message_id=?`, pair.ID, guestInboxKey(r.inbox), m.MessageID).Scan(&reference); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		r.logAuthentication(m, pair, outcome, reference)
	}
	return nil
}

func (r *InboxRouter) holdUnauthenticated(ctx context.Context, m Message) error {
	pair, g, eligible, err := r.unverifiedGuestScope(m)
	if err != nil {
		return err
	}
	if !eligible {
		r.logAuthentication(m, pair, "no_eligible_owner")
		return r.raw.MarkProcessed(ctx, m.MessageID)
	}
	// Freeze content before the risk decision. Neither new provider content nor a
	// reinvitation may change which instruction an old message represents.
	if err := r.guests.bindWork(g.GuestKey, m.MessageID, messageFingerprint(m)); err != nil {
		if errors.Is(err, ErrGuestUnauthorized) {
			r.logAuthentication(m, pair, "stale_or_changed_message")
			return r.raw.MarkProcessed(ctx, m.MessageID)
		}
		return err
	}
	args := []any{pair.ID, g.InboxID, m.MessageID, g.Address, g.ThreadID, g.Generation}
	if _, err = r.guests.db.Exec(`INSERT OR IGNORE INTO guest_auth_messages(pair_id,inbox_id,message_id,address,thread_id,generation) VALUES(?,?,?,?,?,?)`, args...); err != nil {
		return err
	}
	if err = r.ensureAuthenticationNotice(ctx, m, pair, g); err != nil {
		return err
	}
	// A durable held-message reference allows recovery after acceptance without
	// repeatedly fetching skipped unread mail or retaining private message bodies.
	return r.raw.MarkProcessed(ctx, m.MessageID)
}

func (r *InboxRouter) ensureAuthenticationNotice(ctx context.Context, m Message, pair Pair, g GuestGrant) error {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return err
	}
	token := strings.ToUpper(hex.EncodeToString(entropy[:]))
	args := append(g.GuestKey.args(), g.Generation, token, m.MessageID)
	if _, err := r.guests.db.Exec(`INSERT OR IGNORE INTO guest_auth_exceptions(pair_id,inbox_id,address,thread_id,generation,token,message_id) VALUES(?,?,?,?,?,?,?)`, args...); err != nil {
		return err
	}
	var parent, state string
	if err := r.guests.db.QueryRow(`SELECT token,message_id,notice_state FROM guest_auth_exceptions WHERE `+grantWhere+` AND generation=?`, append(g.GuestKey.args(), g.Generation)...).Scan(&token, &parent, &state); err != nil {
		return err
	}
	if parent != m.MessageID || state == "sent" {
		return r.authenticationOutcome(m, pair, "deduplicated")
	}
	if state == "sending" {
		// A send might have succeeded before a lost response or process crash. Only
		// recover a matching private warning, never submit it a second time blindly.
		history, err := r.raw.Thread(ctx, m.ThreadID)
		if err != nil {
			return r.authenticationOutcome(m, pair, "failed_receipt_lookup")
		}
		for _, candidate := range history {
			if r.privateAuthenticationWarning(candidate, pair, m, token) {
				if _, err := r.guests.db.Exec(`UPDATE guest_auth_exceptions SET notice_state='sent',outbound_id=? WHERE token=?`, candidate.MessageID, token); err != nil {
					return err
				}
				return r.authenticationOutcome(m, pair, "sent_recovered")
			}
		}
		return r.authenticationOutcome(m, pair, "failed_delivery_uncertain")
	}
	// Reserve an attempt and its rate-limit slot atomically across processes.
	tx, err := r.guests.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := guestGrant(tx, g.GuestKey)
	if err != nil {
		return err
	}
	if !current.Active || current.Generation != g.Generation {
		return nil
	}
	var count int
	now := time.Now().Unix()
	if err := tx.QueryRow(`SELECT count(*) FROM guest_auth_exceptions WHERE pair_id=? AND attempted_at>?`, pair.ID, now-3600).Scan(&count); err != nil {
		return err
	}
	if count >= authenticationNoticeHourlyLimit {
		if err := tx.Commit(); err != nil {
			return err
		}
		return r.authenticationOutcome(m, pair, "rate_limited")
	}
	result, err := tx.Exec(`UPDATE guest_auth_exceptions SET notice_state='sending',attempted_at=? WHERE token=? AND notice_state='pending'`, now, token)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if changed == 0 {
		return r.authenticationOutcome(m, pair, "deduplicated")
	}
	body := authoredControlBody(m)
	if len(body) > 16000 {
		body = body[:16000] + "\n[Preview truncated; review the original email.]"
	}
	text := fmt.Sprintf(
		"%s\n\n"+
			"A message claiming to be from %s arrived in this thread. We could not verify that it came from that address. "+
			"Incorrect email settings can cause this, but someone could also be impersonating the sender.\n\n"+
			"Technical detail: %s.\n\n"+
			"Guest message (unverified):\n> %s\n\n"+
			"To accept this risk for this guest in this thread, reply with exactly:\nALLOW UNVERIFIED %s\n\n"+
			"This also covers future messages claiming this address until the guest is removed. "+
			"You must still approve every individual message with Yes, No, or Other, including this first message. "+
			"This command does not approve any instruction or verify the sender.\n\n"+
			"Otherwise, ask the sender to fix their email authentication settings and resend. "+
			"For help, launch dearmachine and ask about authentication reference %s.\n\nProvider message: %s",
		authenticationWarningHeading, g.Address, authenticationReason(m),
		strings.ReplaceAll(body, "\n", "\n> "), token, token, m.MessageID)

	receipt, err := r.raw.Reply(ctx, m.MessageID, participantPrivatePayload(text, pair.UserEmail), controlIdempotencyKey("guest-authentication", token))
	if err != nil || receipt == "" {
		return r.authenticationOutcome(m, pair, "failed_delivery_uncertain")
	}
	if _, err := r.guests.db.Exec(`UPDATE guest_auth_exceptions SET notice_state='sent',outbound_id=? WHERE token=?`, receipt, token); err != nil {
		return err
	}
	return r.authenticationOutcome(m, pair, "sent")
}

func isAuthenticationWarning(m Message) bool {
	return containsFold(m.Labels, "sent") && strings.HasPrefix(strings.TrimSpace(m.Body), authenticationWarningHeading+"\n")
}
func (r *InboxRouter) privateAuthenticationWarning(candidate Message, pair Pair, parent Message, token string) bool {
	from, err := canonicalMessageAddress(candidate.From)
	parentID := parent.MessageID
	if parent.RFCMessageID != "" {
		parentID = parent.RFCMessageID
	}
	return err == nil && from == r.inbox.Address && candidate.ThreadID == parent.ThreadID && candidate.InReplyTo == parentID &&
		isAuthenticationWarning(candidate) && sameRecipientSet(candidate.To, []string{pair.UserEmail}) && len(candidate.CC) == 0 && len(candidate.BCC) == 0 && strings.Contains(candidate.Body, "\nALLOW UNVERIFIED "+token+"\n")
}

// Only an authenticated owner and the exact current grant may consume a token.
// Acceptance remains distinct from per-message instruction approval.
func (r *InboxRouter) handleAuthenticationControl(ctx context.Context, pair Pair, m Message) (bool, error) {
	fields := strings.Fields(authoredControlBody(m))
	command := len(fields) >= 2 && strings.EqualFold(fields[0], "ALLOW") && strings.EqualFold(fields[1], "UNVERIFIED")
	if !command {
		// A simple Yes to the warning must not become an owner agent instruction
		// or accidentally approve another pending guest message via References.
		if r.guests != nil && m.authenticated && m.InReplyTo != "" && isParticipantDecision(authoredControlBody(m)) {
			parent := m.InReplyTo
			if m.RFCMessageID != "" {
				history, err := r.raw.Thread(ctx, m.ThreadID)
				if err != nil {
					return true, err
				}
				mapped, err := resolveControlReferences(m, history)
				if err != nil {
					return true, err
				}
				parent = mapped.InReplyTo
			}
			var warning bool
			if err := r.guests.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM guest_auth_exceptions WHERE pair_id=? AND inbox_id=? AND thread_id=? AND outbound_id=?)`, pair.ID, guestInboxKey(r.inbox), m.ThreadID, parent).Scan(&warning); err != nil {
				return true, err
			}
			if warning {
				if r.logger != nil {
					r.logger.Printf("guest_authentication message=%q thread=%q owner=%q authentication_exception=explicit_command_required", m.MessageID, m.ThreadID, pair.UserEmail)
				}
				return true, r.raw.MarkProcessed(ctx, m.MessageID)
			}
		}
		return false, nil
	}
	outcome := "rejected"
	if m.authenticated && r.guests != nil && len(fields) == 3 {
		from, _ := canonicalMessageAddress(m.From)
		tx, err := r.guests.db.Begin()
		if err != nil {
			return true, err
		}
		defer tx.Rollback()
		var k GuestKey
		var generation int64
		err = tx.QueryRow(`SELECT pair_id,inbox_id,address,thread_id,generation FROM guest_auth_exceptions WHERE token=?`, strings.ToUpper(fields[2])).Scan(&k.PairID, &k.InboxID, &k.Address, &k.ThreadID, &generation)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return true, err
		}
		if err == nil {
			g, err := guestGrant(tx, k)
			if err != nil {
				return true, err
			}
			if guestExceptionEligible(from == pair.UserEmail, g.Active, k.PairID == pair.ID && k.InboxID == guestInboxKey(r.inbox) && k.ThreadID == m.ThreadID, g.Generation == generation, true) {
				if _, err := tx.Exec(`UPDATE guest_auth_exceptions SET accepted=1 WHERE token=?`, strings.ToUpper(fields[2])); err != nil {
					return true, err
				}
				outcome = "accepted"
			}
		}
		if err := tx.Commit(); err != nil {
			return true, err
		}
	}
	if r.logger != nil {
		r.logger.Printf("guest_authentication message=%q thread=%q owner=%q pair=%q authentication_exception=%s", m.MessageID, m.ThreadID, pair.UserEmail, pair.ID, outcome)
	}
	return true, r.raw.MarkProcessed(ctx, m.MessageID)
}

func (r *InboxRouter) resumeAuthenticationMessages(ctx context.Context) ([]Message, error) {
	if r.guests == nil {
		return nil, nil
	}
	// Recover held read messages after acceptance; pending/sending notices are
	// revisited at most once per minute. The durable grant join excludes removals.
	rows, err := r.guests.db.Query(`SELECT m.pair_id,m.message_id,e.accepted FROM guest_auth_messages m
 JOIN guest_grants g ON g.pair_id=m.pair_id AND g.inbox_id=m.inbox_id AND g.address=m.address AND g.thread_id=m.thread_id AND g.generation=m.generation AND g.active=1
 JOIN guest_auth_exceptions e ON e.pair_id=m.pair_id AND e.inbox_id=m.inbox_id AND e.address=m.address AND e.thread_id=m.thread_id AND e.generation=m.generation
 WHERE m.inbox_id=? AND m.done=0 AND (e.accepted=1 OR (e.message_id=m.message_id AND e.notice_state<>'sent')) ORDER BY m.rowid LIMIT 100`, guestInboxKey(r.inbox))
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var pair, id string
		var accepted bool
		if err := rows.Scan(&pair, &id, &accepted); err != nil {
			rows.Close()
			return nil, err
		}
		if _, ok := r.pairs[pair]; !ok {
			continue
		}
		if accepted || time.Since(r.lastAuthRecovery) >= time.Minute {
			ids = append(ids, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if time.Since(r.lastAuthRecovery) >= time.Minute {
		r.lastAuthRecovery = time.Now()
	}
	messages := make([]Message, 0, len(ids))
	for _, id := range ids {
		m, err := r.raw.Message(ctx, id)
		if err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, nil
}

// The warning and ordinary approval can share a parent. Never recover the
// warning as the approval (or as an answer) after a crash.
func (e *pairEndpoint) replyReceiptWithoutAuthWarning(ctx context.Context, m Message, recipient string) (string, bool, error) {
	if e.router.guests == nil {
		return e.router.raw.ReplyReceipt(ctx, m, recipient)
	}
	var held bool
	if err := e.router.guests.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM guest_auth_messages WHERE pair_id=? AND inbox_id=? AND message_id=?)`, e.pairID, guestInboxKey(e.router.inbox), m.MessageID).Scan(&held); err != nil {
		return "", false, err
	}
	if !held {
		return e.router.raw.ReplyReceipt(ctx, m, recipient)
	}
	history, err := e.router.raw.Thread(ctx, m.ThreadID)
	if err != nil {
		return "", false, err
	}
	parent := m.MessageID
	if m.RFCMessageID != "" {
		parent = m.RFCMessageID
	}
	for _, candidate := range history {
		from, err := canonicalMessageAddress(candidate.From)
		if err == nil && from == e.router.inbox.Address && candidate.ThreadID == m.ThreadID && candidate.InReplyTo == parent && containsFold(candidate.Labels, "sent") && !isAuthenticationWarning(candidate) && sameRecipientSet(candidate.To, []string{replyReceiptRecipient(m, recipient)}) && len(candidate.CC) == 0 && len(candidate.BCC) == 0 {
			return candidate.MessageID, true, nil
		}
	}
	return "", false, nil
}

func isParticipantDecision(body string) bool {
	switch strings.ToLower(strings.TrimSpace(body)) {
	case "yes", "no", "other":
		return true
	}
	return false
}

func (r *InboxRouter) recordAcceptedAuthentication(m Message, pair Pair) error {
	k, err := guestKey(pair, r.inbox, m)
	if err != nil {
		return err
	}
	g, err := r.guests.Grant(k)
	if err != nil {
		return err
	}
	if _, err := r.guests.db.Exec(`INSERT OR IGNORE INTO guest_auth_messages(pair_id,inbox_id,message_id,address,thread_id,generation) VALUES(?,?,?,?,?,?)`, pair.ID, k.InboxID, m.MessageID, k.Address, k.ThreadID, g.Generation); err != nil {
		return err
	}
	return r.authenticationOutcome(m, pair, "exception_active")
}
