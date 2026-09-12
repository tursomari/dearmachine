package client

import (
	"context"
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
			if mode == "cc" || mode == "to" {
				want = []string{"guest@example.test"}
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
	rig.restartStore(t)
	if id, found, err := rig.app.resultReplyReceipt(context.Background(), m); err != nil || !found || id != "answer" {
		t.Fatalf("answer recovery: %s %v %v", id, found, err)
	}
}
