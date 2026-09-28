package client

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"
)

// replyNotSubmitted is authoritative local evidence: the adapter did not call
// a mutating provider endpoint. HTTP status codes and missing receipts alone
// are never sufficient evidence. Keep this type private to the adapters.
type replyNotSubmitted struct{ err error }

func (e *replyNotSubmitted) Error() string { return e.err.Error() }
func (e *replyNotSubmitted) Unwrap() error { return e.err }
func beforeReplySubmission(err error) error {
	if err == nil {
		return nil
	}
	return &replyNotSubmitted{err}
}

const outboundMaxAttempts = 3
const outboundRetryBudget = 15 * time.Minute

type outboundAttempt struct {
	Count        int
	Started      time.Time
	RetryAt      time.Time
	NotSubmitted bool
}

func (a outboundAttempt) retryable(now time.Time) bool {
	return a.NotSubmitted && a.Count > 0 && a.Count < outboundMaxAttempts && !a.Started.IsZero() && !now.Before(a.Started) && now.Before(a.Started.Add(outboundRetryBudget))
}
func (a *outboundAttempt) begin(now time.Time) {
	if a.Count == 0 {
		a.Started = now
	}
	a.Count++
	a.NotSubmitted = false
	a.RetryAt = time.Time{}
}
func (a *outboundAttempt) failed(err error) {
	var rejected *replyNotSubmitted
	a.NotSubmitted = errors.As(err, &rejected)
	if a.NotSubmitted {
		a.RetryAt = time.Now().UTC().Add(time.Duration(a.Count) * 5 * time.Second)
	}
}
func outboundNoticeKey(kind, identity string) string {
	return fmt.Sprintf("dearmachine-outbound-%s-%x", kind, sha256.Sum256([]byte(identity)))
}

// Intent is durable before I/O. Without an authoritative provider deduplication
// contract we attempt a notice once, including across restart. If acceptance
// is uncertain, native status reports it rather than risking a notice loop.
// No body, attachment, approval token or raw provider error enters this table.
func (e *pairEndpoint) sendOutboundNotice(ctx context.Context, s *Store, parent Message, owner, key, text string) error {
	if owner == "" || owner != e.controllingParticipant() {
		return nil
	}
	result, err := s.db.Exec(`INSERT INTO outbound_notices
 (notice_key,pair_id,inbox_id,owner,message_id,thread_id,state,hold_reason,receipt)
 VALUES(?,?,?,?,?,?,'sending','private notice delivery could not be confirmed','')
 ON CONFLICT(notice_key) DO NOTHING`, key, e.pairID, guestInboxKey(e.router.inbox), owner, parent.MessageID, parent.ThreadID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return err
	}
	id, sendErr := e.router.raw.Reply(ctx, parent.MessageID, ReplyPayload{To: []string{owner}, Text: text}, key)
	state, reason := "sent", ""
	if sendErr != nil || id == "" {
		state, reason = "held", "private notice delivery could not be confirmed"
	}
	_, err = s.db.Exec(`UPDATE outbound_notices SET state=?,hold_reason=?,receipt=? WHERE notice_key=?`, state, reason, id, key)
	return err
}

func (e *pairEndpoint) reportOutboundHold(ctx context.Context, s *Store, o outboundApproval) error {
	if o.HoldReason == "" || o.State == "sent" || o.State == "rejected" || o.State == "superseded" {
		return nil
	}
	attempt := o.SubmissionAttempt
	if o.State == "preview_sending" {
		attempt = o.PreviewAttempt
	}
	if attempt.retryable(time.Now().UTC()) && (o.HoldReason == "preview not attempted; retry scheduled" || o.HoldReason == "submission not attempted; retry scheduled") {
		return nil
	}
	// An old owner's outbox is visible in local status but must not disclose its
	// existence or content to a replacement owner or another inbox.
	if o.PairID != e.pairID || o.InboxID != guestInboxKey(e.router.inbox) || o.Owner != e.controllingParticipant() {
		return nil
	}
	text := "Delivery of the reply to this request could not be confirmed. The reply is held; check Dear Machine status."
	if o.State == "preview_sending" {
		text = "Delivery of the approval preview for this request could not be confirmed. The reply is held and has not been released to guests; check Dear Machine status."
	}
	return e.sendOutboundNotice(ctx, s, o.Message, o.Owner, outboundNoticeKey("hold", fmt.Sprintf("%s/%d", o.Key, o.Revision)), text)
}
