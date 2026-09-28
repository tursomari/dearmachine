package client

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"reflect"
	"strings"
	"time"

	htmlparser "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var errOutboundScope = errors.New("outbound scope mismatch")
var errOutboundAmbiguousReceipt = errors.New("ambiguous outbound receipt")

const outboundReferencePrefix = "Dear Machine outbound approval: "

// A local receipt means immutable mail was accepted into the durable approval
// outbox, not sent. Completed turns may release staging files: this owns bytes.
type outboundApproval struct {
	Key                               string
	Revision                          int
	Token                             string
	PairID, InboxID, Owner            string
	Message                           Message
	Payload                           ReplyPayload
	State                             string
	PreviewID, PreviewRFCID, SentID   string
	DecisionID, Decision              string
	PreviewPayload                    ReplyPayload
	PreviewGrants                     map[string]int64
	SubmissionGrants                  map[string]int64
	DecisionSender                    string
	DecisionAuthenticated             bool
	HoldReason                        string
	PreviewAttempt, SubmissionAttempt outboundAttempt
}

func (o outboundApproval) receipt() string {
	if o.SentID != "" {
		return o.SentID
	}
	return "outbound-pending:" + o.Token
}
func (s *Store) outboundRecords() ([]outboundApproval, error) {
	return s.queryOutbound(`SELECT record FROM outbound_approvals ORDER BY send_key, revision`)
}

func (s *Store) queryOutbound(query string, args ...any) ([]outboundApproval, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []outboundApproval
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return nil, err
		}
		var o outboundApproval
		if err := json.Unmarshal([]byte(encoded), &o); err != nil {
			return nil, err
		}
		result = append(result, o)
	}
	return result, rows.Err()
}

type outboundWriter interface {
	Exec(string, ...any) (sql.Result, error)
}

func writeOutbound(db outboundWriter, o outboundApproval) error {
	encoded, err := json.Marshal(o)
	if err != nil {
		return err
	}
	reference := o
	reference.Payload, reference.PreviewPayload = ReplyPayload{}, ReplyPayload{}
	reference.PreviewGrants, reference.SubmissionGrants = nil, nil
	metadata, err := json.Marshal(reference)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO outbound_approvals(send_key,revision,token,record,reference,state) VALUES(?,?,?,?,?,?)
 ON CONFLICT(send_key,revision) DO UPDATE SET record=excluded.record,reference=excluded.reference,state=excluded.state`, o.Key, o.Revision, o.Token, string(encoded), string(metadata), o.State)
	return err
}
func (s *Store) saveOutbound(o outboundApproval) error { return writeOutbound(s.db, o) }

func (s *Store) replaceOutbound(old, next outboundApproval) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old.State = "superseded"
	if err := writeOutbound(tx, old); err != nil {
		return err
	}
	if err := writeOutbound(tx, next); err != nil {
		return err
	}
	return tx.Commit()
}

func newOutboundToken() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
}
func (s *Store) outboundResultReceipt(messageID string) (string, bool, error) {
	pending, found, err := s.PendingByID(messageID)
	if err != nil || !found {
		return "", false, err
	}
	key := idempotencyKey(pending.Session.SessionID, messageID)
	s.outboundMu.Lock()
	defer s.outboundMu.Unlock()
	records, err := s.queryOutbound(`SELECT reference FROM outbound_approvals WHERE send_key=? ORDER BY revision`, key)
	if err != nil {
		return "", false, err
	}
	for i := len(records) - 1; i >= 0; i-- {
		o := records[i]
		if o.Message.MessageID == messageID && o.Key == key && o.State != "superseded" {
			return o.receipt(), true, nil
		}
	}
	return "", false, nil
}
func (e *pairEndpoint) outboundStore() *Store {
	e.router.mu.Lock()
	defer e.router.mu.Unlock()
	return e.router.stores[e.pairID]
}

// Every paired reply has an explicit envelope. Provider reply-all defaults and
// implicit reply-to must never disclose an unapproved answer.
func (e *pairEndpoint) approvedReply(ctx context.Context, messageID string, payload ReplyPayload, key string) (string, error) {
	e.router.mu.Lock()
	message := e.router.cached[messageID]
	e.router.mu.Unlock()
	owner := e.controllingParticipant()
	if len(payload.To) == 0 {
		selected, err := e.resultRecipients(message)
		if err != nil {
			return "", err
		}
		payload.To, payload.CC, payload.BCC = selected.To, selected.CC, nil
	}
	s := e.outboundStore()
	if s != nil {
		s.outboundMu.Lock()
		defer s.outboundMu.Unlock()
		records, err := s.queryOutbound(`SELECT record FROM outbound_approvals WHERE send_key=? ORDER BY revision`, key)
		if err != nil {
			return "", err
		}
		if len(records) > 0 {
			first, last := records[0], records[len(records)-1]
			if first.Message.MessageID != messageID || first.Message.ThreadID != message.ThreadID || first.PairID != e.pairID || first.InboxID != guestInboxKey(e.router.inbox) {
				return "", errors.New("outbound send key reused across requests")
			}
			// Compare the original complete request, before any internal reduction.
			// Returning a receipt never gives callers a way to replace its bytes or
			// bypass the outbox by changing a shared retry into a private send.
			if !reflect.DeepEqual(first.Payload, payload) {
				return "", errors.New("outbound payload changed for an existing send key")
			}
			return last.receipt(), nil
		}
	}
	if sameRecipientSet(payload.To, []string{owner}) && len(payload.CC) == 0 && len(payload.BCC) == 0 {
		return e.router.raw.Reply(ctx, messageID, payload, key)
	}
	if !sameRecipientSet(payload.To, []string{owner}) || len(payload.BCC) != 0 || payload.IncludeQuotedContent {
		return "", errors.New("outbound approval requires owner To, guest CC and no quoted content")
	}
	if s == nil || e.router.guests == nil || key == "" {
		return "", errors.New("shared reply requires a durable outbound approval store")
	}
	token, err := newOutboundToken()
	if err != nil {
		return "", err
	}
	// Only routing metadata from the instruction is needed for recovery.
	parent := Message{MessageID: message.MessageID, RFCMessageID: message.RFCMessageID, ThreadID: message.ThreadID}
	o := outboundApproval{Key: key, Revision: 1, Token: token, PairID: e.pairID, InboxID: guestInboxKey(e.router.inbox), Owner: owner, Message: parent, Payload: payload, State: "prepared"}
	if err := s.saveOutbound(o); err != nil {
		return "", err
	}
	if err := e.advanceOutbound(ctx, s, &o); err != nil {
		return "", err
	}
	return o.receipt(), nil
}
func outboundPreview(o outboundApproval) ReplyPayload {
	header := "PENDING APPROVAL — this reply has not been sent to guests.\n" +
		"To: " + strings.Join(o.Payload.To, ", ") + "\nCC: " + strings.Join(o.Payload.CC, ", ") +
		"\nReply with only yes or no.\n" + outboundReferencePrefix + o.Token + "\n\n"
	p := o.Payload
	p.To, p.CC, p.BCC = []string{o.Owner}, nil, nil
	p.Text = header + p.Text
	if p.HTML != "" {
		p.HTML = prependOutboundHTML(p.HTML, "<div><pre>"+html.EscapeString(header)+"</pre></div>")
	}
	return p
}

// BEGIN IMMEDIATE in the guest database orders revocation with first network
// attempts. The pair store commits intent BEFORE I/O. Uncertain sends only
// reconcile receipts. A retry requires authoritative adapter evidence that no
// mutating provider call was attempted, plus a fresh eligibility check.
func (e *pairEndpoint) advanceOutbound(ctx context.Context, s *Store, o *outboundApproval) error {
	if o.PairID != e.pairID || o.InboxID != guestInboxKey(e.router.inbox) || o.Owner != e.controllingParticipant() {
		return errOutboundScope
	}
	if o.State == "rejected" || o.State == "sent" || o.State == "superseded" {
		return nil
	}
	if o.State == "preview_sending" || o.State == "sending" {
		preview := o.State == "preview_sending"
		payload := o.Payload
		if preview {
			payload = o.PreviewPayload
		}
		id, rfc, err := e.outboundReceipt(ctx, *o, payload)
		if err != nil {
			return err
		}
		if id == "" {
			attempt := o.SubmissionAttempt
			if preview {
				attempt = o.PreviewAttempt
			}
			if !attempt.retryable(time.Now().UTC()) {
				if attempt.NotSubmitted {
					o.HoldReason = "send not submitted; automatic retry budget exhausted"
					return s.saveOutbound(*o)
				}
				return nil
			}
			if time.Now().UTC().Before(attempt.RetryAt) {
				return nil
			}
			// Only authoritative non-submission permits a new attempt. It must
			// pass the current grant check below; an unknown attempt cannot.
			if preview {
				o.State = "prepared"
			} else {
				o.State = "approved"
			}
		} else {
			if preview {
				o.HoldReason = ""
				o.State, o.PreviewID, o.PreviewRFCID = "pending", id, rfc
			} else {
				o.HoldReason = ""
				o.State, o.SentID = "sent", id
			}
			if err := s.saveOutbound(*o); err != nil {
				return err
			}
			if !preview {
				return nil
			}
		}
	}
	tx, err := e.router.guests.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var encoded string
	if err := tx.QueryRow(`SELECT generations FROM guest_recipient_grants WHERE pair_id=? AND inbox_id=? AND message_id=?`, e.pairID, o.InboxID, o.Message.MessageID).Scan(&encoded); err != nil {
		return err
	}
	var generations map[string]int64
	if err := json.Unmarshal([]byte(encoded), &generations); err != nil {
		return err
	}
	eligibleGrants := map[string]int64{}
	var eligible []string
	for _, address := range o.Payload.CC {
		g, err := guestGrant(tx, GuestKey{e.pairID, o.InboxID, address, o.Message.ThreadID})
		if err != nil {
			return err
		}
		if g.Active && generations[address] == g.Generation {
			eligible = append(eligible, address)
			eligibleGrants[address] = g.Generation
		}
	}
	if !sameRecipientSet(eligible, o.Payload.CC) {
		old := *o
		next := old
		token, err := newOutboundToken()
		if err != nil {
			return err
		}
		next.Revision++
		next.Token, next.State = token, "prepared"
		next.PreviewID, next.PreviewRFCID, next.DecisionID, next.Decision = "", "", "", ""
		next.PreviewPayload = ReplyPayload{}
		next.PreviewGrants, next.SubmissionGrants = nil, nil
		next.DecisionSender, next.DecisionAuthenticated = "", false
		next.PreviewAttempt, next.SubmissionAttempt = outboundAttempt{}, outboundAttempt{}
		next.HoldReason = ""
		next.Payload.CC = eligible
		if err := s.replaceOutbound(old, next); err != nil {
			return err
		}
		*o = next
	}
	if len(o.Payload.CC) == 0 || o.State == "approved" {
		if len(o.Payload.CC) > 0 && (o.PreviewID == "" || o.Decision != "yes" || o.DecisionID == "" || o.DecisionSender != o.Owner || !o.DecisionAuthenticated || !reflect.DeepEqual(o.PreviewPayload, outboundPreview(*o))) {
			return errors.New("outbound approval evidence missing")
		}
		if o.SubmissionAttempt.Count > 0 && !o.SubmissionAttempt.retryable(time.Now().UTC()) {
			o.HoldReason = "submission not attempted; automatic retry budget exhausted"
			return s.saveOutbound(*o)
		}
		o.SubmissionGrants = eligibleGrants
		o.HoldReason = "submission receipt reconciliation required"
		o.State = "sending"
		o.SubmissionAttempt.begin(time.Now().UTC())
		if err := s.saveOutbound(*o); err != nil {
			return err
		}
		id, err := e.router.raw.Reply(ctx, o.Message.MessageID, o.Payload, o.Key)
		if err != nil {
			o.SubmissionAttempt.failed(err)
			if o.SubmissionAttempt.NotSubmitted {
				o.HoldReason = "submission not attempted; retry scheduled"
			}
			if saveErr := s.saveOutbound(*o); saveErr != nil {
				return saveErr
			}
			return err
		}
		if id == "" {
			return errors.New("outbound submission returned no receipt")
		}
		o.HoldReason = ""
		o.State, o.SentID = "sent", id
		return s.saveOutbound(*o)
	}
	if o.State == "prepared" {
		if o.PreviewAttempt.Count > 0 && !o.PreviewAttempt.retryable(time.Now().UTC()) {
			o.HoldReason = "preview not attempted; automatic retry budget exhausted"
			return s.saveOutbound(*o)
		}
		o.PreviewPayload = outboundPreview(*o)
		o.PreviewGrants = eligibleGrants
		o.HoldReason = "preview receipt reconciliation required"
		o.State = "preview_sending"
		o.PreviewAttempt.begin(time.Now().UTC())
		if err := s.saveOutbound(*o); err != nil {
			return err
		}
		id, err := e.router.raw.Reply(ctx, o.Message.MessageID, o.PreviewPayload, "dearmachine-outbound-preview-"+o.Token)
		if err != nil {
			o.PreviewAttempt.failed(err)
			if o.PreviewAttempt.NotSubmitted {
				o.HoldReason = "preview not attempted; retry scheduled"
			}
			if saveErr := s.saveOutbound(*o); saveErr != nil {
				return saveErr
			}
			return err
		}
		if id == "" {
			return errors.New("outbound preview returned no receipt")
		}
		o.HoldReason = ""
		o.State, o.PreviewID = "pending", id
		return s.saveOutbound(*o)
	}
	if o.State == "pending" && o.HoldReason != "" {
		o.HoldReason = ""
		return s.saveOutbound(*o)
	}
	return nil
}

// Reconcile complete envelopes and content, never a generic reply to a thread.
func (e *pairEndpoint) outboundReceipt(ctx context.Context, o outboundApproval, payload ReplyPayload) (string, string, error) {
	messages, err := e.router.raw.Thread(ctx, o.Message.ThreadID)
	if err != nil {
		return "", "", err
	}
	var id, rfc string
	for _, m := range messages {
		body := m.RawBody
		if body == "" {
			body = m.Body
		}
		parent := o.Message.MessageID
		if o.Message.RFCMessageID != "" {
			parent = o.Message.RFCMessageID
		}
		if m.ThreadID != o.Message.ThreadID || !containsFold(m.Labels, "sent") || canonicalAddressOrLower(m.From) != e.router.inbox.Address || m.InReplyTo != parent || !sameRecipientSet(m.To, payload.To) || !sameRecipientSet(m.CC, payload.CC) || len(m.BCC) != 0 || normalizeOutboundBody(body) != normalizeOutboundBody(payload.Text) || m.RawHTML != payload.HTML || len(m.Attachments) != len(payload.Files) {
			continue
		}
		matches := true
		for i, f := range payload.Files {
			ref := m.Attachments[i]
			if ref.Filename != f.Filename || ref.ContentType != f.ContentType {
				matches = false
				break
			}
			data, err := e.router.raw.FetchAttachment(ctx, ref.AttachmentID, int64(len(f.Contents))+1)
			if err != nil {
				return "", "", err
			}
			if !reflect.DeepEqual(data, f.Contents) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		if id != "" && id != m.MessageID {
			return "", "", errOutboundAmbiguousReceipt
		}
		id, rfc = m.MessageID, m.RFCMessageID
	}
	return id, rfc, nil
}
func (a *App) recoverOutbound(ctx context.Context) error {
	e, ok := a.transport.(*pairEndpoint)
	if !ok || e.router.guests == nil {
		return nil
	}
	a.store.outboundMu.Lock()
	defer a.store.outboundMu.Unlock()
	records, err := a.store.queryOutbound(`SELECT record FROM outbound_approvals WHERE state IN ('prepared','preview_sending','pending','approved','sending') ORDER BY send_key, revision`)
	if err != nil {
		return err
	}
	for i := range records {
		o := &records[i]
		attemptsBefore := o.PreviewAttempt.Count + o.SubmissionAttempt.Count
		if err := e.advanceOutbound(ctx, a.store, o); err != nil {
			// Provider error strings can contain private content. Persist and
			// log only a local reason; each record remains independently held.
			switch {
			case errors.Is(err, errOutboundScope):
				o.HoldReason = "outbound scope mismatch; reply held"
			case errors.Is(err, errOutboundAmbiguousReceipt):
				o.HoldReason = "ambiguous outbound receipt; reply held"
			case o.PreviewAttempt.Count+o.SubmissionAttempt.Count == attemptsBefore:
				o.HoldReason = "outbound recovery failed; reply held"
			}
			if err := a.store.saveOutbound(*o); err != nil {
				return err
			}
			a.logger.Printf("outbound recovery held revision=%d state=%q", o.Revision, o.State)
			// Give receipt reconciliation the next pass before notifying about
			// a newly uncertain attempt; acceptance may already be observable.
			if o.PreviewAttempt.Count+o.SubmissionAttempt.Count > attemptsBefore {
				continue
			}
		}
		if err := e.reportOutboundHold(ctx, a.store, *o); err != nil {
			return err
		}
	}
	return nil
}

// MIME transports may normalize line endings; all other body bytes matter.
func normalizeOutboundBody(body string) string { return strings.ReplaceAll(body, "\r\n", "\n") }

// Insert inside a full HTML document without rewriting the approved markup.
// Fragment payloads have no body element, so their banner is simply prepended.
func prependOutboundHTML(body, header string) string {
	tokenizer := htmlparser.NewTokenizer(strings.NewReader(body))
	offset := 0
	for {
		kind := tokenizer.Next()
		if kind == htmlparser.ErrorToken {
			return header + body
		}
		offset += len(tokenizer.Raw())
		if kind == htmlparser.StartTagToken && tokenizer.Token().DataAtom == atom.Body {
			return body[:offset] + header + body[offset:]
		}
	}
}

// Keep routine polling independent of historical attachment volume. The
// reference projection is updated atomically with the full immutable payload.
// Backfill older local outboxes once, including pending drafts across upgrades.
func (s *Store) migrateOutbound() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS outbound_notices (
 notice_key TEXT PRIMARY KEY, pair_id TEXT NOT NULL, inbox_id TEXT NOT NULL,
 owner TEXT NOT NULL, message_id TEXT NOT NULL, thread_id TEXT NOT NULL,
 state TEXT NOT NULL, hold_reason TEXT NOT NULL, receipt TEXT NOT NULL
)`); err != nil {
		return err
	}
	for _, column := range []string{"reference", "state"} {
		exists, err := sqliteTableHasColumn(s.db, "outbound_approvals", column)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := s.db.Exec("ALTER TABLE outbound_approvals ADD COLUMN " + column + " TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
		}
	}
	records, err := s.queryOutbound(`SELECT record FROM outbound_approvals WHERE reference='' OR state=''`)
	if err != nil {
		return err
	}
	for _, o := range records {
		if err := s.saveOutbound(o); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS outbound_approval_state ON outbound_approvals(state)`)
	return err
}
