package client

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type outboundRejectingTransport struct {
	*fakeTransport
	failKey     string
	failPreview bool
	reject      bool
	known       bool
	attempts    []fakeTransportReply
}

func (p *outboundRejectingTransport) Reply(ctx context.Context, id string, pl ReplyPayload, key string) (string, error) {
	p.attempts = append(p.attempts, fakeTransportReply{MessageID: id, IdempotencyKey: key, Text: pl.Text, To: pl.To, CC: pl.CC})
	if p.reject && (key == p.failKey || p.failPreview && strings.HasPrefix(key, "dearmachine-outbound-preview-")) {
		err := errors.New("synthetic unavailable provider")
		if p.known {
			err = beforeReplySubmission(err)
		}
		return "", err
	}
	return p.fakeTransport.Reply(ctx, id, pl, key)
}
func makeOutboundRetryDue(t *testing.T, f *outboundFixture) {
	t.Helper()
	r := f.records(t)
	o := r[len(r)-1]
	o.PreviewAttempt.RetryAt = time.Now().Add(-time.Minute)
	o.SubmissionAttempt.RetryAt = time.Now().Add(-time.Minute)
	if err := f.rig.store.saveOutbound(o); err != nil {
		t.Fatal(err)
	}
}
func recoverOutboundForTest(t *testing.T, f *outboundFixture) {
	t.Helper()
	if err := f.rig.app.recoverOutbound(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func assertPrivateOutboundNotice(t *testing.T, r fakeTransportReply, owner string) {
	t.Helper()
	if !sameRecipientSet(r.To, []string{owner}) || len(r.CC) != 0 || len(r.BCC) != 0 || len(r.Files) != 0 || r.IncludeQuotedContent || strings.Contains(r.Text, outboundReferencePrefix) {
		t.Fatalf("notice not private: %+v", r)
	}
}
func TestOutboundRecoveryKnownPreviewFailureRetriesSameFrozenRequest(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	p := &outboundRejectingTransport{fakeTransport: f.raw, failPreview: true, reject: true, known: true}
	f.router.raw = p
	if _, err := f.endpoint.Reply(context.Background(), f.message.MessageID, f.payload, f.key); err == nil {
		t.Fatal("expected failure")
	}
	before := f.records(t)[0]
	f.rig.restartStore(t)
	p.reject = false
	makeOutboundRetryDue(t, f)
	recoverOutboundForTest(t, f)
	after := f.records(t)[0]
	if after.State != "pending" || len(f.raw.sentReplies()) != 1 || after.Token != before.Token || after.PreviewAttempt.Count != 2 || len(p.attempts) != 2 || p.attempts[0].IdempotencyKey != p.attempts[1].IdempotencyKey || p.attempts[0].Text != p.attempts[1].Text {
		t.Fatal("preview retry lost identity or duplicated")
	}
	recoverOutboundForTest(t, f)
	if len(f.raw.sentReplies()) != 1 {
		t.Fatal("preview repeated")
	}
}
func TestOutboundRecoveryKnownSubmissionFailureRetriesAndRechecksRevocation(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		name := "current"
		if revoke {
			name = "revoked"
		}
		t.Run(name, func(t *testing.T) {
			f := newOutboundFixture(t, "first@example.test", "second@example.test")
			f.queue(t)
			p := &outboundRejectingTransport{fakeTransport: f.raw, failKey: f.key, reject: true, known: true}
			f.router.raw = p
			f.decision(t, "yes", "yes", f.raw.sentReplies()[0].ReceiptID)
			if f.records(t)[0].State != "sending" || len(f.raw.sentReplies()) != 1 {
				t.Fatal("failed submission not retained")
			}
			if revoke {
				if err := f.router.RevokeGuest(f.pair.ID, "first@example.test", f.message.ThreadID, false); err != nil {
					t.Fatal(err)
				}
			}
			f.rig.restartStore(t)
			p.reject = false
			makeOutboundRetryDue(t, f)
			recoverOutboundForTest(t, f)
			replies := f.raw.sentReplies()
			if len(replies) != 2 {
				t.Fatalf("sends=%d", len(replies))
			}
			if revoke {
				assertOutboundPrivate(t, replies[1], f.pair.UserEmail)
				if len(f.records(t)) != 2 || f.records(t)[1].State != "pending" {
					t.Fatal("revocation did not require fresh preview")
				}
				f.decision(t, "fresh-yes", "yes", replies[1].ReceiptID)
				if got := f.raw.sentReplies(); len(got) != 3 || !sameRecipientSet(got[2].CC, []string{"second@example.test"}) {
					t.Fatal("revoked recipient reached by retry")
				}
			} else if f.records(t)[0].State != "sent" || p.attempts[0].IdempotencyKey != p.attempts[1].IdempotencyKey || !sameRecipientSet(replies[1].CC, f.payload.CC) {
				t.Fatal("submission retry changed identity")
			}
		})
	}
}
func TestOutboundRecoveryExhaustionAndUnknownAcceptanceNotifyOnce(t *testing.T) {
	for _, mode := range []string{"attempt-limit", "deadline", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			f := newOutboundFixture(t, "guest@example.test")
			p := &outboundRejectingTransport{fakeTransport: f.raw, failPreview: true, reject: true, known: mode != "unknown"}
			f.router.raw = p
			if _, err := f.endpoint.Reply(context.Background(), f.message.MessageID, f.payload, f.key); err == nil {
				t.Fatal("expected failure")
			}
			if mode == "deadline" {
				o := f.records(t)[0]
				o.PreviewAttempt.Started = time.Now().Add(-outboundRetryBudget - time.Minute)
				if err := f.rig.store.saveOutbound(o); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 5; i++ {
				makeOutboundRetryDue(t, f)
				recoverOutboundForTest(t, f)
			}
			replies := f.raw.sentReplies()
			if len(replies) != 1 || !strings.Contains(replies[0].Text, "could not be confirmed") || f.records(t)[0].HoldReason == "" {
				t.Fatal("missing unique hold notice")
			}
			assertPrivateOutboundNotice(t, replies[0], f.pair.UserEmail)
			want := 1
			if mode == "attempt-limit" {
				want = outboundMaxAttempts
			}
			if f.records(t)[0].PreviewAttempt.Count != want {
				t.Fatal("unbounded or unsafe retry")
			}
			f.rig.restartStore(t)
			recoverOutboundForTest(t, f)
			if len(f.raw.sentReplies()) != 1 {
				t.Fatal("notice duplicated after restart")
			}
		})
	}
}
func TestOutboundRecoveryRecordFailureDoesNotBlockPollingOrOtherRecords(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.queue(t)
	bad := f.records(t)[0]
	bad.Key = "0-bad-scope"
	bad.Token = strings.Repeat("e", 32)
	bad.Owner = "former-owner@example.test"
	bad.State = "prepared"
	bad.PreviewID = ""
	bad.PreviewRFCID = ""
	if err := f.rig.store.saveOutbound(bad); err != nil {
		t.Fatal(err)
	}
	f.decision(t, "yes-valid", "yes", f.raw.sentReplies()[0].ReceiptID)
	if len(f.raw.sentReplies()) != 2 || outboundRanAgent(f.rig) {
		t.Fatal("bad record blocked approval polling")
	}
	records := f.records(t)
	if records[0].HoldReason == "" || records[1].State != "sent" {
		t.Fatal("record failure not isolated and held")
	}
}
func TestOutboundRecoveryNoticeLostResponseIsNotRetried(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	u := &outboundUncertainTransport{fakeTransport: f.raw, fail: true}
	f.router.raw = u
	key := outboundNoticeKey("control", "synthetic-control")
	for i := 0; i < 3; i++ {
		if err := f.endpoint.sendOutboundNotice(context.Background(), f.rig.store, f.message, f.pair.UserEmail, key, "Synthetic private notice"); err != nil {
			t.Fatal(err)
		}
		f.rig.restartStore(t)
	}
	if len(f.raw.sentReplies()) != 1 {
		t.Fatal("uncertain notice repeated")
	}
	var state, reason string
	if err := f.rig.store.db.QueryRow(`SELECT state,hold_reason FROM outbound_notices WHERE notice_key=?`, key).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "held" || reason == "" {
		t.Fatal("notice uncertainty not visible")
	}
}

func TestOutboundRecoveryRedraftTransactionFailureCannotPublishPartialRevision(t *testing.T) {
	f := newOutboundFixture(t, "first@example.test", "second@example.test")
	f.queue(t)
	old := f.records(t)[0]
	if _, err := f.rig.store.db.Exec(`CREATE TRIGGER fail_redraft BEFORE UPDATE ON outbound_approvals WHEN NEW.state='superseded' BEGIN SELECT RAISE(ABORT,'synthetic transaction failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := f.router.RevokeGuest(f.pair.ID, "first@example.test", f.message.ThreadID, false); err != nil {
		t.Fatal(err)
	}
	recoverOutboundForTest(t, f)
	records := f.records(t)
	if len(records) != 1 || records[0].Revision != old.Revision || records[0].Token != old.Token || records[0].State != "pending" || !sameRecipientSet(records[0].Payload.CC, old.Payload.CC) {
		t.Fatal("failed transaction published a partial revision")
	}
	if len(f.contentReplies(t)) != 1 {
		t.Fatal("failed transaction sent a new preview")
	}
	if _, err := f.rig.store.db.Exec(`DROP TRIGGER fail_redraft`); err != nil {
		t.Fatal(err)
	}
	recoverOutboundForTest(t, f)
	records = f.records(t)
	if len(records) != 2 || records[0].State != "superseded" || records[1].State != "pending" || len(f.contentReplies(t)) != 2 {
		t.Fatal("transaction retry did not replace exactly once")
	}
}
