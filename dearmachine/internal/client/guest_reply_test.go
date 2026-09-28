package client

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestOwnerAnswersCopyOnlyVisibleGrantedGuests(t *testing.T) {
	for _, mode := range []string{"cc", "to", "private", "bcc", "revoked", "other-thread", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			rig, raw, router, pair, inbox := newParticipantTestRig(t)
			establishParticipantThread(t, rig, raw, router, pair, inbox, "thread")
			m := Message{MessageID: "owner-followup", ThreadID: "thread", From: pair.UserEmail, To: []string{inbox.Address}, Body: "answer this", Timestamp: time.Now().UTC()}
			switch mode {
			case "cc", "revoked", "other-thread":
				m.CC = []string{"Guest <guest@example.test>"}
			case "to":
				m.To = append(m.To, "guest@example.test")
			case "bcc":
				m.BCC = []string{"guest@example.test"}
			case "unknown":
				m.CC = []string{"stranger@example.test"}
			}
			if mode == "revoked" {
				if err := router.RevokeGuest(pair.ID, "guest@example.test", "thread", false); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "other-thread" {
				m.ThreadID = "different"
			}
			raw.setThread(m.ThreadID, append(raw.thread(m.ThreadID), m))
			raw.setPoll([]Message{m})
			router.lastPoll = time.Time{}
			mustProcess(t, rig)
			replies := raw.sentReplies()
			got := replies[len(replies)-1]
			want := []string(nil)
			if mode == "cc" || mode == "to" || mode == "other-thread" {
				want = []string{"guest@example.test"}
			}
			if mode == "unknown" {
				want = []string{"stranger@example.test"}
			}
			if len(want) > 0 {
				got = approveGuestOutboundPreview(t, rig, raw, router, pair, inbox, m, got)
			}
			if !equalFoldSlice(got.To, []string{pair.UserEmail}) || !equalFoldSlice(got.CC, want) || len(got.BCC) != 0 {
				t.Fatalf("answer envelope: To=%v CC=%v BCC=%v", got.To, got.CC, got.BCC)
			}
		})
	}
}
func TestResultEnvelopePersistsAcrossRestart(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread")
	m := Message{MessageID: "stable", ThreadID: "thread", From: pair.UserEmail, To: []string{inbox.Address}, CC: []string{"guest@example.test"}}
	snapshotReplyFixture(t, router, pair, m)
	first, err := rig.app.resultReplyPayload(m, ReplyPayload{Text: "answer"})
	if err != nil {
		t.Fatal(err)
	}
	rig.restartStore(t)
	// A changed provider refetch or later invite must not widen an already
	// submitted idempotent result's envelope on restart.
	m.CC = []string{"second@example.test"}
	second, err := rig.app.resultReplyPayload(m, ReplyPayload{Text: "answer"})
	if err != nil {
		t.Fatal(err)
	}
	if !equalFoldSlice(first.CC, []string{"guest@example.test"}) || !equalFoldSlice(second.CC, first.CC) {
		t.Fatalf("envelope changed: %+v -> %+v", first, second)
	}

}

func TestResultReceiptRequiresTheSubmittedRecipientEnvelope(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread")
	m := Message{MessageID: "approved-guest", ThreadID: "thread", From: "guest@example.test", To: []string{inbox.Address, pair.UserEmail}}
	snapshotReplyFixture(t, router, pair, m)
	if _, err := rig.app.resultReplyPayload(m, ReplyPayload{Text: "answer"}); err != nil {
		t.Fatal(err)
	}
	private := Message{MessageID: "admission", ThreadID: "thread", From: inbox.Address, To: []string{pair.UserEmail}, Labels: []string{"sent"}}
	raw.setThread("thread", []Message{m, private})
	if _, found, err := rig.app.resultReplyReceipt(context.Background(), m); err != nil || found {
		t.Fatalf("private prompt mistaken for answer: %v %v", found, err)
	}
	answer := private
	answer.MessageID = "answer"
	answer.CC = []string{"Guest <guest@example.test>"}
	raw.setThread("thread", []Message{m, private, answer})
	if id, found, err := rig.app.resultReplyReceipt(context.Background(), m); err != nil || !found || id != "answer" {
		t.Fatalf("ordered answer recovery: %s %v %v", id, found, err)
	}
	// AgentMail correlates by InReplyTo; its thread result need not be ordered.
	answer.InReplyTo = m.MessageID
	raw.setThread("thread", []Message{answer, m, private})
	rig.restartStore(t)
	if id, found, err := rig.app.resultReplyReceipt(context.Background(), m); err != nil || !found || id != "answer" {
		t.Fatalf("answer recovery: %s %v %v", id, found, err)
	}
	m.RFCMessageID = "<request@sender.test>"
	answer.InReplyTo = m.RFCMessageID
	raw.setThread("thread", []Message{answer, m, private})
	if id, found, err := rig.app.resultReplyReceipt(context.Background(), m); err != nil || !found || id != "answer" {
		t.Fatalf("Internet Message-ID answer recovery: %s %v %v", id, found, err)
	}
}

func snapshotReplyFixture(t *testing.T, router *InboxRouter, pair Pair, m Message) {
	t.Helper()
	m, err := router.authenticateMessage(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if err := router.snapshotGuestRecipients(pair.ID, m); err != nil {
		t.Fatal(err)
	}
}

func TestGuestReinvitationCannotSubscribeToAnOlderAnswer(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread")
	m := Message{MessageID: "accepted", ThreadID: "thread", From: pair.UserEmail, To: []string{inbox.Address}, CC: []string{"guest@example.test"}}
	snapshotReplyFixture(t, router, pair, m)
	e := rig.app.transport.(*pairEndpoint)
	before, err := e.resultRecipients(m)
	if err != nil || len(before.CC) != 1 {
		t.Fatalf("original grant not selected: %+v %v", before, err)
	}
	k := GuestKey{pair.ID, guestInboxKey(inbox), "guest@example.test", m.ThreadID}
	if err := router.guests.Revoke(k, false); err != nil {
		t.Fatal(err)
	}
	if _, err := router.guests.Allow(k, "explicit-reinvitation", true); err != nil {
		t.Fatal(err)
	}
	// Repeated acceptance cannot replace the stored generation snapshot.
	snapshotReplyFixture(t, router, pair, m)
	after, err := rig.app.resultReplyPayload(m, ReplyPayload{Text: "delayed answer"})
	if err != nil || len(after.CC) != 0 || !equalFoldSlice(after.To, []string{pair.UserEmail}) {
		t.Fatalf("new grant received an older answer: %+v %v", after, err)
	}
}

// approveGuestOutboundPreview is called only where a scenario expects a shared
// answer, after instruction approval (if needed) has already run the work.
func approveGuestOutboundPreview(t *testing.T, rig *testRig, raw *fakeTransport, router *InboxRouter, pair Pair, inbox Inbox, original Message, preview fakeTransportReply) fakeTransportReply {
	t.Helper()
	if !strings.HasPrefix(preview.Text, "PENDING APPROVAL — this reply has not been sent to guests.\n") || preview.MessageID != original.MessageID || preview.ReceiptID == "" {
		t.Fatalf("expected outbound preview for %s: %+v", original.MessageID, preview)
	}
	if !equalFoldSlice(preview.To, []string{pair.UserEmail}) || len(preview.CC) != 0 || len(preview.BCC) != 0 || preview.IncludeQuotedContent {
		t.Fatalf("outbound preview must remain private: %+v", preview)
	}
	before := len(raw.sentReplies())
	runs := rig.capture("count")
	yes := Message{MessageID: "outbound-yes-" + preview.ReceiptID, ThreadID: original.ThreadID, From: pair.UserEmail, To: []string{inbox.Address}, Body: "yes", InReplyTo: preview.ReceiptID, Timestamp: time.Now().UTC()}
	raw.setThread(yes.ThreadID, append(raw.thread(yes.ThreadID), yes))
	raw.setPoll([]Message{yes})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	replies := raw.sentReplies()
	if len(replies) != before+1 || rig.capture("count") != runs {
		t.Fatalf("outbound approval must send one answer without executing work: replies=%d want=%d runs=%s want=%s", len(replies), before+1, rig.capture("count"), runs)
	}
	answer := replies[before]
	if answer.MessageID != original.MessageID || strings.HasPrefix(answer.Text, "PENDING APPROVAL") {
		t.Fatalf("outbound approval did not release the original answer: %+v", answer)
	}
	return answer
}
