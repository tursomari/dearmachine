package client

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Use the real router, control correlation and SQLite transitions through Other.
func pendingReplacementRig(t *testing.T) (*testRig, *fakeTransport, *InboxRouter, Pair, Message) {
	t.Helper()
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread-replacement-recovery")
	seedAdmittedParticipants(t, rig.store, "guest@example.test")
	const secret = "REJECTED_GUEST_BODY"
	participant := Message{
		MessageID: "guest-recovery-other", ThreadID: "thread-replacement-recovery",
		From: "guest@example.test", To: []string{inbox.Address, pair.UserEmail}, Body: secret,
		Timestamp: time.Now().UTC(),
	}
	raw.setThread(participant.ThreadID, append(raw.thread(participant.ThreadID), participant))
	raw.setPoll([]Message{participant})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	approval := raw.sentReplies()[1]
	other := Message{
		MessageID: "controller-recovery-other", ThreadID: participant.ThreadID,
		From: pair.UserEmail, To: []string{inbox.Address}, Body: "Other",
		InReplyTo: approval.ReceiptID, Timestamp: participant.Timestamp.Add(time.Minute),
	}
	raw.setThread(other.ThreadID, append(raw.thread(other.ThreadID), other))
	raw.setPoll([]Message{other})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	replacementPrompt := raw.sentReplies()[2]
	replacement := Message{
		MessageID: "controller-recovery-replacement", ThreadID: participant.ThreadID,
		From: pair.UserEmail, To: []string{inbox.Address},
		Body:      "OWNER_REPLACEMENT\n\n> " + secret,
		InReplyTo: replacementPrompt.ReceiptID, Timestamp: other.Timestamp.Add(time.Minute),
	}
	raw.setThread(replacement.ThreadID, append(raw.thread(replacement.ThreadID), replacement))
	raw.setPoll([]Message{replacement})
	router.lastPoll = time.Time{}
	if err := rig.app.pollAndClaim(context.Background(), newThreadWorkQueue()); err != nil {
		t.Fatal(err)
	}
	return rig, raw, router, pair, replacement
}

func TestParticipantReplacementRepollDuringExecution(t *testing.T) {
	rig, raw, router, _, replacement := pendingReplacementRig(t)
	// Recover the durable claim exactly as ProcessOnce does, then force two
	// polls at the real subprocess start boundary before it can be completed.
	work := newThreadWorkQueue()
	if err := rig.app.recoverPending(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	item, found := work.take()
	if !found {
		t.Fatal("replacement was not recovered")
	}
	repolls := 0
	err := rig.app.processWork(context.Background(), item, func() {
		pending, found, err := rig.store.PendingByID(replacement.MessageID)
		if err != nil || !found || pending.State != messageRunning {
			t.Errorf("expected running replacement: %v, %v", found, err)
			return
		}
		for i := 0; i < 2; i++ {
			router.lastPoll = time.Time{}
			if err := rig.app.pollAndClaim(context.Background(), newThreadWorkQueue()); err != nil {
				t.Error(err)
				return
			}
			repolls++
		}
		var count int
		if err := rig.store.db.QueryRow(`SELECT count(*) FROM processed_messages WHERE message_id=?`, replacement.MessageID).Scan(&count); err != nil {
			t.Error(err)
			return
		}
		if count != 0 {
			t.Error("repoll recorded running replacement as processed")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if repolls != 2 {
		t.Fatalf("running repolls=%d, want 2", repolls)
	}
	if got := rig.capture("count"); got != "2" {
		t.Fatalf("agent turns=%q, want owner and one replacement", got)
	}
	replies := raw.sentReplies()
	if len(replies) != 4 {
		t.Fatalf("replies=%d, want owner, approval, replacement prompt, answer", len(replies))
	}
	assertReplacementCompleted(t, rig, replacement, replies[3].ReceiptID)
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	if rig.capture("count") != "2" || len(raw.sentReplies()) != 4 {
		t.Fatal("completed replacement replayed")
	}
}

func TestParticipantReplacementRecoveryRepairsLegacyControlRecord(t *testing.T) {
	for _, sent := range []bool{false, true} {
		t.Run(fmt.Sprintf("answer_already_sent=%v", sent), func(t *testing.T) {
			rig, raw, router, pair, replacement := pendingReplacementRig(t)
			if err := rig.store.MarkRunning(replacement.MessageID, "frozen owner replacement"); err != nil {
				t.Fatal(err)
			}
			// Reproduce the old poller's empty control row before result persistence.
			if err := rig.store.RecordControlMessage(replacement.MessageID, replacement.ThreadID, ""); err != nil {
				t.Fatal(err)
			}
			if err := rig.store.StoreResult(replacement.MessageID, RunResult{Kind: ResultAnswer, Text: "saved replacement answer"}); err != nil {
				t.Fatal(err)
			}
			receipt := ""
			if sent {
				var err error
				receipt, err = raw.Reply(context.Background(), replacement.MessageID,
					participantPrivatePayload("saved replacement answer", pair.UserEmail), "fixture-sent-answer")
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := rig.store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := OpenStore(rig.dbPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			rig.store, rig.app.store = store, store
			rig.app.transport.(interface{ bindStore(*Store) }).bindStore(store)
			router.lastPoll = time.Time{}
			mustProcess(t, rig)
			if rig.capture("count") != "1" {
				t.Fatal("recovery executed the agent despite a saved result")
			}
			replies := raw.sentReplies()
			if len(replies) != 4 {
				t.Fatalf("replies=%d, want exactly one replacement answer", len(replies))
			}
			if sent && replies[3].ReceiptID != receipt {
				t.Fatal("recovery replaced the sent receipt")
			}
			if !equalFoldSlice(replies[3].To, []string{pair.UserEmail}) || len(replies[3].CC) != 0 {
				t.Fatal("replacement answer was not owner-only")
			}
			assertReplacementCompleted(t, rig, replacement, replies[3].ReceiptID)
			router.lastPoll = time.Time{}
			mustProcess(t, rig)
			if rig.capture("count") != "1" || len(raw.sentReplies()) != 4 {
				t.Fatal("second recovery repeated work or delivery")
			}
		})
	}
}

func assertReplacementCompleted(t *testing.T, rig *testRig, message Message, receipt string) {
	t.Helper()
	if _, found, err := rig.store.PendingByID(message.MessageID); err != nil || found {
		t.Fatalf("pending after completion: %v, %v", found, err)
	}
	var got string
	if err := rig.store.db.QueryRow(`SELECT outbound_message_id FROM processed_messages WHERE message_id=?`, message.MessageID).Scan(&got); err != nil || got != receipt {
		t.Fatalf("receipt=%q, want %q: %v", got, receipt, err)
	}
	session, err := rig.store.Session(message.ThreadID)
	if err != nil || session.Sequence != 2 {
		t.Fatalf("session sequence=%d, want 2: %v", session.Sequence, err)
	}
}

func TestStoreReplacementRepairPreservesUnrelatedRecords(t *testing.T) {
	for _, scenario := range []string{"ordinary", "participant", "unsanitized", "different-thread", "existing-receipt", "no-new-receipt", "not-ready", "sequence-conflict", "thread-alias"} {
		t.Run(scenario, func(t *testing.T) {
			store := openTestStore(t)
			pending, _, err := store.BeginMessage("replacement", "thread", TierPlain)
			if err != nil {
				t.Fatal(err)
			}
			authority, sanitize := authorityController, 1
			if scenario == "ordinary" {
				authority, sanitize = "", 0
			}
			if scenario == "participant" {
				authority = authorityParticipant
			}
			if scenario == "unsanitized" {
				sanitize = 0
			}
			if _, err := store.db.Exec(`UPDATE pending_messages SET authority=?, sanitize_control_body=? WHERE message_id=?`, authority, sanitize, pending.MessageID); err != nil {
				t.Fatal(err)
			}
			if err := store.MarkRunning(pending.MessageID, "frozen instruction"); err != nil {
				t.Fatal(err)
			}
			if scenario != "not-ready" {
				if err := store.StoreResult(pending.MessageID, RunResult{Kind: ResultAnswer, Text: "saved answer"}); err != nil {
					t.Fatal(err)
				}
			}
			thread, oldReceipt, newReceipt := "thread", "", "answer-receipt"
			if scenario == "different-thread" {
				thread = "unrelated"
			}
			if scenario == "existing-receipt" {
				oldReceipt = "previous-receipt"
			}
			if scenario == "no-new-receipt" {
				newReceipt = ""
			}
			if scenario == "thread-alias" {
				thread = "external-thread"
				if _, err := store.db.Exec(`INSERT INTO thread_aliases VALUES (?, ?)`, thread, pending.ThreadID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "sequence-conflict" {
				if _, err := store.db.Exec(`UPDATE thread_sessions SET sequence=5 WHERE thread_id=?`, pending.ThreadID); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.RecordControlMessage(pending.MessageID, thread, oldReceipt); err != nil {
				t.Fatal(err)
			}
			err = store.Complete(pending.MessageID, "completed", newReceipt)
			if scenario == "thread-alias" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("completion accepted unrelated or inconsistent state")
			}
			var gotReceipt, gotThread string
			if err := store.db.QueryRow(`SELECT thread_id, outbound_message_id FROM processed_messages WHERE message_id=?`, pending.MessageID).Scan(&gotThread, &gotReceipt); err != nil {
				t.Fatal(err)
			}
			if gotThread != thread || gotReceipt != oldReceipt {
				t.Fatal("failed completion changed original record")
			}
			var count int
			if err := store.db.QueryRow(`SELECT count(*) FROM pending_messages WHERE message_id=?`, pending.MessageID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("failed completion lost pending result: %d, %v", count, err)
			}
		})
	}
}
