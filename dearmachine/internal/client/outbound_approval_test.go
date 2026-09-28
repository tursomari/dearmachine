package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type outboundFixture struct {
	rig      *testRig
	raw      *fakeTransport
	router   *InboxRouter
	endpoint *pairEndpoint
	pair     Pair
	inbox    Inbox
	message  Message
	payload  ReplyPayload
	key      string
}

func newOutboundFixture(t *testing.T, guests ...string) *outboundFixture {
	t.Helper()
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	m := Message{MessageID: "outbound-request", ThreadID: "outbound-thread", From: pair.UserEmail, To: []string{inbox.Address}, CC: guests, Body: "Synthetic shared request", Timestamp: time.Now().UTC()}
	raw.setThread(m.ThreadID, []Message{m})
	raw.setPoll([]Message{m})
	e := rig.app.transport.(*pairEndpoint)
	messages, err := e.Poll(context.Background())
	if err != nil || len(messages) != 1 {
		t.Fatalf("accept request: %v %v", messages, err)
	}
	raw.setPoll(nil)
	return &outboundFixture{rig, raw, router, e, pair, inbox, m, ReplyPayload{Text: "Exact answer\n\nsecond paragraph", HTML: "<p>Exact <b>answer</b></p>", Files: []OutboundFile{{Filename: "answer.txt", ContentType: "text/plain", Contents: []byte("attachment bytes")}}, To: []string{pair.UserEmail}, CC: guests}, idempotencyKey("fixture", m.MessageID)}
}
func (f *outboundFixture) queue(t *testing.T) {
	t.Helper()
	id, err := f.endpoint.Reply(context.Background(), f.message.MessageID, f.payload, f.key)
	if err != nil || !strings.HasPrefix(id, "outbound-pending:") {
		t.Fatalf("queue: %q %v", id, err)
	}
}
func (f *outboundFixture) decision(t *testing.T, id, body, parent string) {
	t.Helper()
	m := Message{MessageID: id, ThreadID: f.message.ThreadID, From: f.pair.UserEmail, To: []string{f.inbox.Address}, Body: body, InReplyTo: parent, Timestamp: time.Now().UTC()}
	f.raw.setThread(m.ThreadID, append(f.raw.thread(m.ThreadID), m))
	f.raw.setPoll([]Message{m})
	f.router.lastPoll = time.Time{}
	mustProcess(t, f.rig)
}
func (f *outboundFixture) records(t *testing.T) []outboundApproval {
	t.Helper()
	r, err := f.rig.store.outboundRecords()
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func assertOutboundPrivate(t *testing.T, r fakeTransportReply, owner string) {
	t.Helper()
	if !sameRecipientSet(r.To, []string{owner}) || len(r.CC) != 0 || len(r.BCC) != 0 || r.IncludeQuotedContent || !strings.HasPrefix(r.Text, "PENDING APPROVAL") {
		t.Fatalf("not a private preview: %+v", r)
	}
}
func TestOutboundApprovalExactFrozenPayloadAndRestart(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.queue(t)
	replies := f.contentReplies(t)
	if len(replies) != 1 {
		t.Fatal("unexpected send")
	}
	preview := replies[0]
	assertOutboundPrivate(t, preview, f.pair.UserEmail)
	if !strings.HasSuffix(preview.Text, f.payload.Text) || !strings.HasSuffix(preview.HTML, f.payload.HTML) || !reflect.DeepEqual(preview.Files, f.payload.Files) {
		t.Fatal("preview is not exact")
	}
	f.rig.restartStore(t)
	f.decision(t, "approve", "YES\n\nOn Mon, Jan 2, 2006 at 1:29 AM Machine <machine@example.test> wrote:\n> pending", preview.ReceiptID)
	replies = f.contentReplies(t)
	if len(replies) != 2 {
		t.Fatalf("sends=%d", len(replies))
	}
	answer := replies[1]
	if answer.Text != f.payload.Text || answer.HTML != f.payload.HTML || !reflect.DeepEqual(answer.Files, f.payload.Files) || !sameRecipientSet(answer.CC, f.payload.CC) || len(answer.BCC) != 0 {
		t.Fatalf("approved answer changed: %+v", answer)
	}
	r := f.records(t)[0]
	if r.State != "sent" || r.PreviewID == "" || !r.DecisionAuthenticated || r.DecisionSender != f.pair.UserEmail || r.PreviewGrants["guest@example.test"] != 1 || r.SubmissionGrants["guest@example.test"] != 1 {
		t.Fatalf("missing durable evidence: %+v", r)
	}
	f.decision(t, "approve", "yes", preview.ReceiptID)
	if len(f.contentReplies(t)) != 2 || outboundRanAgent(f.rig) {
		t.Fatal("approval replay sent or executed work")
	}
}
func TestOutboundApprovalOnlyYesOrNoAndRejectionTerminal(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.queue(t)
	preview := f.contentReplies(t)[0]
	for i, body := range []string{"Other", "yes please", "yes!", "no thanks", "yes\nrun something"} {
		f.decision(t, body, body, preview.ReceiptID)
		if len(f.contentReplies(t)) != 1 || f.records(t)[0].State != "pending" {
			t.Fatalf("invalid decision %d released or rejected", i)
		}
	}
	f.decision(t, "reject", "No", preview.ReceiptID)
	f.decision(t, "late-yes", "yes", preview.ReceiptID)
	if f.records(t)[0].State != "rejected" || len(f.contentReplies(t)) != 1 || outboundRanAgent(f.rig) {
		t.Fatal("rejection did not terminate draft")
	}
}
func TestOutboundApprovalScopeAndAuthentication(t *testing.T) {
	for _, mode := range []string{"guest", "unauthenticated", "other-thread", "unknown-token", "instruction-reference"} {
		t.Run(mode, func(t *testing.T) {
			f := newOutboundFixture(t, "guest@example.test")
			f.queue(t)
			r := f.records(t)[0]
			m := Message{MessageID: "forged-decision", ThreadID: f.message.ThreadID, From: f.pair.UserEmail, To: []string{f.inbox.Address}, InReplyTo: r.PreviewID, Body: "yes", authenticated: true}
			switch mode {
			case "guest":
				m.From = "guest@example.test"
			case "unauthenticated":
				m.authenticated = false
			case "other-thread":
				m.ThreadID = "other-thread"
			case "unknown-token":
				m.InReplyTo = ""
				m.RawBody = "yes\n\nOn Mon, Jan 2, 2006 at 1:29 AM Machine <machine@example.test> wrote:\n> " + outboundReferencePrefix + strings.Repeat("0", 32)
			case "instruction-reference":
				request, _, err := f.rig.store.BeginParticipantRequest(Message{MessageID: "guest-instruction", ThreadID: m.ThreadID, From: "guest@example.test"}, f.pair.UserEmail)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.rig.store.SetParticipantPrompt(request.RequestMessageID, participantAwaitingDecision, "instruction-prompt"); err != nil {
					t.Fatal(err)
				}
				m.InReplyTo = "instruction-prompt"
				m.RawBody = "yes\n\nOn Mon, Jan 2, 2006 at 1:29 AM Machine <machine@example.test> wrote:\n> " + outboundReferencePrefix + r.Token
			}
			// Cache synthetic routing only for MarkProcessed, preserving authentication
			// as assigned above to test the decision boundary independently.
			f.raw.setThread(m.ThreadID, append(f.raw.thread(m.ThreadID), m))
			m.Delivery = MessageDelivery{InboxID: f.inbox.ProviderID, Recipient: f.inbox.Address}
			m.To = []string{f.inbox.Address}
			f.router.mu.Lock()
			f.router.remember(f.pair.ID, m)
			f.router.mu.Unlock()
			_, err := f.rig.app.handleOutboundDecision(context.Background(), m)
			if err != nil && !errors.Is(err, ErrGuestUnauthorized) {
				t.Fatal(err)
			}
			if f.records(t)[0].State != "pending" || len(f.contentReplies(t)) != 1 {
				t.Fatal("untrusted decision authorized output")
			}
		})
	}
}
func TestOutboundApprovalRecipientReductionRequiresNewPreview(t *testing.T) {
	f := newOutboundFixture(t, "first@example.test", "second@example.test")
	f.queue(t)
	old := f.contentReplies(t)[0]
	if err := f.router.RevokeGuest(f.pair.ID, "first@example.test", f.message.ThreadID, false); err != nil {
		t.Fatal(err)
	}
	f.decision(t, "old-yes", "yes", old.ReceiptID)
	replies := f.contentReplies(t)
	if len(replies) != 2 {
		t.Fatalf("sends=%d", len(replies))
	}
	fresh := replies[1]
	assertOutboundPrivate(t, fresh, f.pair.UserEmail)
	if strings.Contains(fresh.Text, "first@example.test") || !strings.Contains(fresh.Text, "second@example.test") {
		t.Fatal("stale recipients in replacement preview")
	}
	if records := f.records(t); len(records) != 2 || records[0].State != "superseded" || records[1].State != "pending" || records[1].Decision != "" {
		t.Fatalf("revision evidence: %+v", records)
	}
	f.decision(t, "fresh-yes", "yes", fresh.ReceiptID)
	replies = f.contentReplies(t)
	if len(replies) != 3 || !sameRecipientSet(replies[2].CC, []string{"second@example.test"}) {
		t.Fatal("reapproved envelope not used")
	}
}
func TestOutboundApprovalRevokedReinvitedGuestCannotReceiveOldAnswer(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.queue(t)
	k := GuestKey{f.pair.ID, guestInboxKey(f.inbox), "guest@example.test", f.message.ThreadID}
	if err := f.router.guests.Revoke(k, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.guests.Allow(k, "new-invitation", true); err != nil {
		t.Fatal(err)
	}
	if err := f.rig.app.recoverOutbound(context.Background()); err != nil {
		t.Fatal(err)
	}
	replies := f.contentReplies(t)
	if len(replies) != 2 || len(replies[1].CC) != 0 || !sameRecipientSet(replies[1].To, []string{f.pair.UserEmail}) {
		t.Fatal("reinvited generation received old answer")
	}
}
func TestOutboundApprovalRemovalReplyToPreview(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.queue(t)
	preview := f.contentReplies(t)[0]
	k := GuestKey{f.pair.ID, guestInboxKey(f.inbox), "guest@example.test", f.message.ThreadID}
	token, err := f.router.guests.RemovalToken(k)
	if err != nil {
		t.Fatal(err)
	}
	f.decision(t, "remove", "REMOVE GUEST "+strings.ToUpper(token)+"\n\nOn Mon, Jan 2, 2006 at 1:29 AM Machine <machine@example.test> wrote:\n> "+outboundReferencePrefix+f.records(t)[0].Token, preview.ReceiptID)
	g, err := f.router.guests.Grant(k)
	if err != nil || g.Active {
		t.Fatal("outbound handler swallowed removal")
	}
	for _, r := range f.contentReplies(t) {
		if len(r.CC) > 0 {
			t.Fatal("removed guest received an answer")
		}
	}
}
func TestOutboundApprovalPayloadSubstitutionAndRequestReuse(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.queue(t)
	changed := f.payload
	changed.Files = cloneOutboundFiles(changed.Files)
	changed.Files[0].Contents = []byte("replacement")
	if _, err := f.endpoint.Reply(context.Background(), f.message.MessageID, changed, f.key); err == nil {
		t.Fatal("mutable file reused approved key")
	}
	changed = f.payload
	changed.CC = []string{"second@example.test"}
	if _, err := f.endpoint.Reply(context.Background(), f.message.MessageID, changed, f.key); err == nil {
		t.Fatal("changed recipients reused approved key")
	}
	changed.CC = nil
	if _, err := f.endpoint.Reply(context.Background(), f.message.MessageID, changed, f.key); err == nil {
		t.Fatal("private retry bypassed the outbox")
	}
	changed = f.payload
	changed.HTML = "<p>different</p>"
	if _, err := f.endpoint.Reply(context.Background(), f.message.MessageID, changed, f.key); err == nil {
		t.Fatal("changed HTML reused approved key")
	}
	other := f.message
	other.MessageID = "other-request"
	f.raw.setThread(other.ThreadID, append(f.raw.thread(other.ThreadID), other))
	f.raw.setPoll([]Message{other})
	f.router.lastPoll = time.Time{}
	if _, err := f.endpoint.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.endpoint.Reply(context.Background(), other.MessageID, f.payload, f.key); err == nil {
		t.Fatal("send key reused for another request")
	}
}

type outboundUncertainTransport struct {
	*fakeTransport
	fail bool
	hide bool
}

func (u *outboundUncertainTransport) Reply(ctx context.Context, id string, p ReplyPayload, key string) (string, error) {
	receipt, err := u.fakeTransport.Reply(ctx, id, p, key)
	if err == nil && u.fail {
		return "", errors.New("simulated lost response")
	}
	return receipt, err
}
func (u *outboundUncertainTransport) Thread(ctx context.Context, id string) ([]Message, error) {
	if u.hide {
		return nil, nil
	}
	return u.fakeTransport.Thread(ctx, id)
}
func TestOutboundApprovalLostPreviewResponseAndDelayedReceipt(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.payload.HTML = ""
	f.payload.Files = nil
	u := &outboundUncertainTransport{fakeTransport: f.raw, fail: true}
	f.router.raw = u
	if _, err := f.endpoint.Reply(context.Background(), f.message.MessageID, f.payload, f.key); err == nil {
		t.Fatal("expected uncertain response")
	}
	r := f.records(t)[0]
	if r.State != "preview_sending" || r.PreviewID != "" {
		t.Fatal("uncertain preview considered issued")
	}
	u.fail = false
	u.hide = true
	body := "yes\n\nOn Mon, Jan 2, 2006 at 1:29 AM Machine <machine@example.test> wrote:\n> " + outboundReferencePrefix + r.Token
	f.decision(t, "delayed-yes", body, "")
	if seen, err := f.rig.store.Seen("delayed-yes"); err != nil || seen {
		t.Fatal("decision discarded before receipt became visible")
	}
	f.rig.restartStore(t)
	u.hide = false
	f.decision(t, "delayed-yes", body, "")
	if len(f.contentReplies(t)) != 2 || f.records(t)[0].State != "sent" {
		t.Fatal("preview recovery failed or duplicated")
	}
}
func TestOutboundApprovalLostSubmissionResponseNeverResends(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.payload.HTML = ""
	f.payload.Files = nil
	f.queue(t)
	u := &outboundUncertainTransport{fakeTransport: f.raw, fail: true}
	f.router.raw = u
	r := f.records(t)[0]
	m := Message{MessageID: "yes", ThreadID: f.message.ThreadID, From: f.pair.UserEmail, Body: "yes", InReplyTo: r.PreviewID, authenticated: true}
	f.raw.setThread(m.ThreadID, append(f.raw.thread(m.ThreadID), m))
	m.Delivery = MessageDelivery{InboxID: f.inbox.ProviderID, Recipient: f.inbox.Address}
	m.To = []string{f.inbox.Address}
	f.router.mu.Lock()
	f.router.remember(f.pair.ID, m)
	f.router.mu.Unlock()
	if _, err := f.rig.app.handleOutboundDecision(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if err := f.rig.app.recoverOutbound(context.Background()); err != nil {
		t.Fatal("uncertain submission blocked independent recovery", err)
	}
	if f.records(t)[0].State != "sending" {
		t.Fatal("uncertain submission not held")
	}
	if err := f.router.RevokeGuest(f.pair.ID, "guest@example.test", f.message.ThreadID, false); err != nil {
		t.Fatal(err)
	}
	f.rig.restartStore(t)
	u.fail = false
	if err := f.rig.app.recoverOutbound(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.contentReplies(t)) != 2 || f.records(t)[0].State != "sent" || f.records(t)[0].SubmissionGrants["guest@example.test"] != 1 {
		t.Fatal("submission recovery changed history or resent")
	}
}
func TestOutboundApprovalUnseenPreviewAndHTMLMismatchStayHeld(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.payload.Files = nil
	u := &outboundUncertainTransport{fakeTransport: f.raw, fail: true}
	f.router.raw = u
	if _, err := f.endpoint.Reply(context.Background(), f.message.MessageID, f.payload, f.key); err == nil {
		t.Fatal("expected failure")
	}
	r := f.records(t)[0]
	thread := f.raw.thread(f.message.ThreadID)
	for i := range thread {
		if containsFold(thread[i].Labels, "sent") {
			thread[i].RawHTML = "<p>tampered HTML</p>"
		}
	}
	f.raw.setThread(f.message.ThreadID, thread)
	u.fail = false
	f.decision(t, "unseen-yes", "yes\n\nOn Mon, Jan 2, 2006 at 1:29 AM Machine <machine@example.test> wrote:\n> "+outboundReferencePrefix+r.Token, "")
	if f.records(t)[0].State != "preview_sending" || len(f.contentReplies(t)) != 1 {
		t.Fatal("lossy receipt matched an unseen preview")
	}
}

func outboundRanAgent(r *testRig) bool {
	_, err := os.Stat(filepath.Join(r.captureDir, "count"))
	return !os.IsNotExist(err)
}

func TestOutboundApprovalDelayedReceiptWithHeaderOnlyDecision(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.payload.HTML = ""
	f.payload.Files = nil
	u := &outboundUncertainTransport{fakeTransport: f.raw, fail: true}
	f.router.raw = u
	if _, err := f.endpoint.Reply(context.Background(), f.message.MessageID, f.payload, f.key); err == nil {
		t.Fatal("expected uncertain response")
	}
	preview := f.contentReplies(t)[0]
	u.fail = false
	u.hide = true
	f.decision(t, "header-yes", "yes", preview.ReceiptID)
	if outboundRanAgent(f.rig) {
		t.Fatal("unreconciled decision became an agent instruction")
	}
	if seen, _ := f.rig.store.Seen("header-yes"); seen {
		t.Fatal("decision prematurely consumed")
	}
	f.rig.restartStore(t)
	u.hide = false
	f.decision(t, "header-yes", "yes", preview.ReceiptID)
	if len(f.contentReplies(t)) != 2 || f.records(t)[0].State != "sent" {
		t.Fatal("header-only decision was not recovered")
	}
}
func TestOutboundApprovalEverySharedReplyAndPrivateExemption(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.queue(t)
	f.decision(t, "yes-first", "yes", f.contentReplies(t)[0].ReceiptID)
	payload := f.payload
	payload.Text = "An error/status answer"
	payload.HTML = ""
	payload.Files = nil
	if _, err := f.endpoint.Reply(context.Background(), f.message.MessageID, payload, controlIdempotencyKey("status", f.message.MessageID)); err != nil {
		t.Fatal(err)
	}
	replies := f.contentReplies(t)
	if len(replies) != 3 {
		t.Fatal("second reply bypassed its independent approval")
	}
	assertOutboundPrivate(t, replies[2], f.pair.UserEmail)
	payload.CC = nil
	if _, err := f.endpoint.Reply(context.Background(), f.message.MessageID, payload, "private-answer"); err != nil {
		t.Fatal(err)
	}
	replies = f.contentReplies(t)
	if len(replies) != 4 || replies[3].Text != payload.Text || len(replies[3].CC) != 0 {
		t.Fatal("owner-only exemption failed")
	}
}

type outboundBlockingTransport struct {
	*fakeTransport
	key              string
	entered, release chan struct{}
}

func (b *outboundBlockingTransport) Reply(ctx context.Context, id string, p ReplyPayload, key string) (string, error) {
	if key == b.key {
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return b.fakeTransport.Reply(ctx, id, p, key)
}
func TestOutboundApprovalSubmissionOrdersConcurrentRevocation(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.queue(t)
	preview := f.contentReplies(t)[0]
	m := Message{MessageID: "owner-yes", ThreadID: f.message.ThreadID, From: f.pair.UserEmail, To: []string{f.inbox.Address}, Body: "yes", InReplyTo: preview.ReceiptID}
	f.raw.setThread(m.ThreadID, append(f.raw.thread(m.ThreadID), m))
	authenticated, err := f.endpoint.Message(context.Background(), m.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.rig.app.handleOutboundDecision(context.Background(), authenticated); err != nil {
		t.Fatal(err)
	}
	other, err := OpenGuestStore(f.router.guests.path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	b := &outboundBlockingTransport{f.raw, f.key, make(chan struct{}), make(chan struct{})}
	f.router.raw = b
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sent := make(chan error, 1)
	go func() { sent <- f.rig.app.recoverOutbound(ctx) }()
	select {
	case <-b.entered:
	case <-ctx.Done():
		t.Fatal("submission never started")
	}
	revoked := make(chan error, 1)
	go func() {
		revoked <- other.Revoke(GuestKey{f.pair.ID, guestInboxKey(f.inbox), "guest@example.test", f.message.ThreadID}, false)
	}()
	select {
	case err := <-revoked:
		t.Fatalf("revocation interleaved before submission finished: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(b.release)
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	if f.records(t)[0].SubmissionGrants["guest@example.test"] != 1 || len(f.contentReplies(t)) != 2 {
		t.Fatal("missing pre-revocation submission evidence")
	}
}

func TestOutboundApprovalHTMLDocumentKeepsBannerInsideBody(t *testing.T) {
	body := replyHTML("Exact approved answer")
	preview := outboundPreview(outboundApproval{Owner: "owner@example.test", Payload: ReplyPayload{Text: "Exact approved answer", HTML: body}})
	if !strings.HasPrefix(preview.HTML, "<!DOCTYPE html>\n<html><body><div><pre>PENDING APPROVAL") || !strings.HasSuffix(preview.HTML, "<p style=\"white-space: pre-wrap\">Exact approved answer</p></body></html>\n") {
		t.Fatal("preview damaged the HTML document")
	}
}

func TestOutboundApprovalReferencesOnlyDecision(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.queue(t)
	m := Message{MessageID: "references-yes", ThreadID: f.message.ThreadID, From: f.pair.UserEmail, To: []string{f.inbox.Address}, Body: "yes", References: []string{f.contentReplies(t)[0].ReceiptID}, Timestamp: time.Now().UTC()}
	f.raw.setThread(m.ThreadID, append(f.raw.thread(m.ThreadID), m))
	f.raw.setPoll([]Message{m})
	f.router.lastPoll = time.Time{}
	mustProcess(t, f.rig)
	if len(f.contentReplies(t)) != 2 || f.records(t)[0].State != "sent" || outboundRanAgent(f.rig) {
		t.Fatal("References-only decision did not resolve exact outbound preview")
	}
}

func TestOutboundApprovalUnknownReplyReferenceIsNotAgentWork(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.queue(t)
	f.decision(t, "unknown-reference", "yes", "guessed-or-wrong-request")
	f.decision(t, "wrong-case-reference", "yes", strings.ToUpper(f.contentReplies(t)[0].ReceiptID))
	if outboundRanAgent(f.rig) || len(f.contentReplies(t)) != 1 || f.records(t)[0].State != "pending" {
		t.Fatal("unknown approval reference executed work or released answer")
	}
}

func TestOutboundApprovalMetadataMigrationPreservesPendingPayload(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.queue(t)
	before := f.records(t)[0]
	// Recreate the pre-projection schema with the exact durable pending record.
	_, err := f.rig.store.db.Exec(`ALTER TABLE outbound_approvals RENAME TO outbound_approvals_old;
 CREATE TABLE outbound_approvals(send_key TEXT NOT NULL,revision INTEGER NOT NULL,token TEXT NOT NULL UNIQUE,record TEXT NOT NULL,PRIMARY KEY(send_key,revision));
 INSERT INTO outbound_approvals SELECT send_key,revision,token,record FROM outbound_approvals_old;
 DROP TABLE outbound_approvals_old;`)
	if err != nil {
		t.Fatal(err)
	}
	f.rig.restartStore(t)
	after := f.records(t)[0]
	if !reflect.DeepEqual(before, after) {
		t.Fatal("migration changed pending approval")
	}
	references, err := f.rig.store.queryOutbound(`SELECT reference FROM outbound_approvals`)
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 1 || references[0].Payload.Text != "" || len(references[0].Payload.Files) != 0 || references[0].PreviewPayload.Text != "" || len(references[0].PreviewPayload.Files) != 0 || references[0].PreviewID != before.PreviewID {
		t.Fatal("routine reference projection contains payload data or lost issuance")
	}
	f.decision(t, "post-upgrade-yes", "yes", before.PreviewID)
	if len(f.contentReplies(t)) != 2 || !reflect.DeepEqual(f.contentReplies(t)[1].Files, f.payload.Files) {
		t.Fatal("upgrade did not release the frozen files")
	}
	var state string
	if err := f.rig.store.db.QueryRow(`SELECT state FROM outbound_approvals`).Scan(&state); err != nil || state != "sent" {
		t.Fatal("state projection not updated atomically")
	}
}

// Control/hold feedback is checked separately from approved content. Every
// excluded notice must still be strictly private and free of payload/files.
func (f *outboundFixture) contentReplies(t *testing.T) []fakeTransportReply {
	t.Helper()
	var result []fakeTransportReply
	for _, r := range f.raw.sentReplies() {
		if strings.HasPrefix(r.IdempotencyKey, "dearmachine-outbound-control-") || strings.HasPrefix(r.IdempotencyKey, "dearmachine-outbound-hold-") {
			assertPrivateOutboundNotice(t, r, f.pair.UserEmail)
			if strings.Contains(r.Text, f.payload.Text) || r.HTML != "" {
				t.Fatal("notice leaked reply content")
			}
			continue
		}
		result = append(result, r)
	}
	return result
}
