package client

import (
	"bytes"
	"database/sql"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreCountProcessedSince(t *testing.T) {
	store := openTestStore(t)
	since := time.Date(2026, 8, 5, 13, 0, 0, 0, time.UTC)
	for _, row := range []struct {
		messageID   string
		processedAt time.Time
	}{
		{messageID: "before", processedAt: since.Add(-time.Second)},
		{messageID: "boundary", processedAt: since},
		{messageID: "after-1", processedAt: since.Add(time.Second)},
		{messageID: "after-2", processedAt: since.Add(time.Hour)},
	} {
		if _, err := store.db.Exec(
			`INSERT INTO processed_messages
			     (message_id, thread_id, outbound_message_id, processed_at)
			 VALUES (?, ?, ?, ?)`,
			row.messageID,
			"thread-"+row.messageID,
			"",
			row.processedAt.Format(time.RFC3339Nano),
		); err != nil {
			t.Fatalf("insert processed message %s: %v", row.messageID, err)
		}
	}

	count, err := store.CountProcessedSince(since)
	if err != nil {
		t.Fatalf("CountProcessedSince: %v", err)
	}
	if count != 2 {
		t.Fatalf("CountProcessedSince = %d, want 2", count)
	}
}

func TestOpenStoreCreatesPrivateStateAndTightensExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	for checkedPath, want := range map[string]os.FileMode{
		filepath.Dir(path): 0o700,
		path:               0o600,
	} {
		info, err := os.Stat(checkedPath)
		if err != nil {
			t.Fatalf("stat %s: %v", checkedPath, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("mode for %s = %o, want %o", checkedPath, got, want)
		}
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("loosen store mode: %v", err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat reopened store: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("reopened store mode = %o, want 600", got)
	}
}

func TestFreshStoreUsesCanonicalSessionIDSchema(t *testing.T) {
	store := openTestStore(t)

	var schema string
	if err := store.db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'thread_sessions'`,
	).Scan(&schema); err != nil {
		t.Fatalf("query thread_sessions schema: %v", err)
	}
	normalizedSchema := strings.Join(strings.Fields(schema), " ")
	if !strings.Contains(normalizedSchema, "session_id TEXT NOT NULL UNIQUE") {
		t.Fatalf("thread_sessions session_id is not TEXT NOT NULL UNIQUE:\n%s", schema)
	}
	if strings.Contains(normalizedSchema, "conversation_ref") {
		t.Fatalf("thread_sessions retained conversation_ref:\n%s", schema)
	}

	pending, existed, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil || existed {
		t.Fatalf("BeginMessage = %+v, %v, %v", pending, existed, err)
	}
	if !validConversationReference(pending.Session.SessionID) {
		t.Fatalf("SessionID = %q, want short canonical conversation reference", pending.Session.SessionID)
	}
	var storedSessionID string
	if err := store.db.QueryRow(
		`SELECT session_id FROM thread_sessions WHERE thread_id = ?`,
		pending.ThreadID,
	).Scan(&storedSessionID); err != nil {
		t.Fatalf("query stored session ID: %v", err)
	}
	if storedSessionID != pending.Session.SessionID {
		t.Fatalf("stored session_id = %q, want %q", storedSessionID, pending.Session.SessionID)
	}
}

func TestStoreStateMachineGuards(t *testing.T) {
	store := openTestStore(t)
	pending, existed, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil || existed {
		t.Fatalf("BeginMessage = %+v, %v, %v", pending, existed, err)
	}

	if err := store.StoreResult("message-1", RunResult{Kind: ResultAnswer, Text: "too soon"}); err == nil || !strings.Contains(err.Error(), "message is not running") {
		t.Fatalf("StoreResult before running error = %v", err)
	}
	if err := store.Complete("message-1", "completed", "reply-too-soon"); err == nil || !strings.Contains(err.Error(), "message is not result ready") {
		t.Fatalf("Complete before result error = %v", err)
	}
	if err := store.MarkRunning("message-1", "prompt"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := store.MarkRunning("message-1", "second prompt"); err == nil || !strings.Contains(err.Error(), "message is not received") {
		t.Fatalf("second MarkRunning error = %v", err)
	}
	if err := store.StoreResult("message-1", RunResult{Kind: ResultAnswer, Text: "answer"}); err != nil {
		t.Fatalf("StoreResult: %v", err)
	}
	if err := store.StoreResult("message-1", RunResult{Kind: ResultAnswer, Text: "second"}); err == nil || !strings.Contains(err.Error(), "message is not running") {
		t.Fatalf("second StoreResult error = %v", err)
	}
	if err := store.Complete("message-1", "completed", "reply-1"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := store.Complete("message-1", "completed", "reply-1"); err == nil {
		t.Fatal("second Complete succeeded")
	}
	if seen, err := store.Seen("message-1"); err != nil || !seen {
		t.Fatalf("Seen completed message = %v, %v", seen, err)
	}
}

func TestStoreReturnsExistingPendingMessageForDuplicate(t *testing.T) {
	store := openTestStore(t)
	first, existed, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil || existed {
		t.Fatalf("first BeginMessage = %+v, %v, %v", first, existed, err)
	}
	if err := store.MarkRunning(first.MessageID, "durable prompt"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	duplicate, existed, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil || !existed {
		t.Fatalf("duplicate BeginMessage = %+v, %v, %v", duplicate, existed, err)
	}
	if duplicate.MessageID != first.MessageID || duplicate.ThreadID != first.ThreadID ||
		duplicate.Session.SessionID != first.Session.SessionID ||
		duplicate.Session.Sequence != first.Session.Sequence ||
		duplicate.State != messageRunning || duplicate.Prompt != "durable prompt" {
		t.Fatalf("duplicate changed pending message: first=%+v duplicate=%+v", first, duplicate)
	}
	if pending, err := store.Pending(); err != nil || len(pending) != 1 {
		t.Fatalf("Pending after duplicate = %+v, %v", pending, err)
	}
}

func TestStoreReferenceAssociatesChangedThreadWithExistingSessionAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "continuity.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	first, existed, err := store.BeginMessageWithReference(
		"message-1", "provider-thread-a", "", TierPlain,
	)
	if err != nil || existed {
		t.Fatalf("first BeginMessageWithReference = %+v, %v, %v", first, existed, err)
	}
	if !validConversationReference(first.Session.SessionID) {
		t.Fatalf("canonical session ID = %q", first.Session.SessionID)
	}
	if err := store.MarkRunning(first.MessageID, "first prompt"); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreResult(first.MessageID, RunResult{Kind: ResultAnswer, Text: "first answer"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(first.MessageID, "completed", "outbound-1"); err != nil {
		t.Fatal(err)
	}
	reference := first.Session.SessionID
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	store, err = OpenStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store.Close()
	second, existed, err := store.BeginMessageWithReference(
		"message-2", "provider-thread-b", reference, TierPlain,
	)
	if err != nil || existed {
		t.Fatalf("second BeginMessageWithReference = %+v, %v, %v", second, existed, err)
	}
	if second.ThreadID != first.ThreadID || second.Session.SessionID != first.Session.SessionID {
		t.Fatalf("changed thread created another session: first=%+v second=%+v", first, second)
	}
	if second.Session.Sequence != 2 || second.Session.SessionID != reference {
		t.Fatalf("changed-thread continuation = %+v", second.Session)
	}
	aliased, err := store.Session("provider-thread-b")
	if err != nil {
		t.Fatalf("Session through learned alias: %v", err)
	}
	if aliased.SessionID != first.Session.SessionID {
		t.Fatalf("alias session = %+v, want %s", aliased, first.Session.SessionID)
	}
}

func TestStoreKnownThreadWinsOverForeignFooterReference(t *testing.T) {
	store := openTestStore(t)
	first, _, err := store.BeginMessageWithReference("message-a", "thread-a", "", TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.BeginMessageWithReference("message-b", "thread-b", "", TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	pending, _, err := store.BeginMessageWithReference(
		"message-a-2", "thread-a", second.Session.SessionID, TierPlain,
	)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Session.SessionID != first.Session.SessionID || pending.ThreadID != first.ThreadID {
		t.Fatalf("foreign footer displaced known thread: first=%+v pending=%+v", first, pending)
	}
}

func TestStoreUnknownValidReferenceCannotAttachToExistingSession(t *testing.T) {
	store := openTestStore(t)
	var warnings bytes.Buffer
	store.warnings = log.New(&warnings, "", 0)
	first, _, err := store.BeginMessageWithReference("message-a", "thread-a", "", TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	unknownReference := newConversationReference()
	second, _, err := store.BeginMessageWithReference(
		"message-b", "thread-b", unknownReference, TierPlain,
	)
	if err != nil {
		t.Fatal(err)
	}
	if second.Session.SessionID == first.Session.SessionID || second.ThreadID == first.ThreadID {
		t.Fatalf("unknown reference attached to existing session: first=%+v second=%+v", first, second)
	}
	if second.Session.SessionID == first.Session.SessionID {
		t.Fatalf("new conversation reused canonical ID %q", first.Session.SessionID)
	}
	if !validConversationReference(second.Session.SessionID) || second.Session.SessionID == unknownReference {
		t.Fatalf("unknown reference did not create a distinct canonical session: %+v", second.Session)
	}
	if output := warnings.String(); !strings.Contains(output, "unknown conversation reference") ||
		!strings.Contains(output, unknownReference) || strings.Contains(output, "collision") {
		t.Fatalf("unknown-reference warning = %q", output)
	}
}

func TestStoreEmptyReferenceStartsFreshSessionWithoutWarning(t *testing.T) {
	store := openTestStore(t)
	var warnings bytes.Buffer
	store.warnings = log.New(&warnings, "", 0)

	pending, existed, err := store.BeginMessageWithReference(
		"message-new", "thread-new", "", TierPlain,
	)
	if err != nil || existed {
		t.Fatalf("BeginMessageWithReference = %+v, %v, %v", pending, existed, err)
	}
	if !pending.Session.IsNew || !validConversationReference(pending.Session.SessionID) {
		t.Fatalf("plain inbound message session = %+v", pending.Session)
	}
	if output := warnings.String(); output != "" {
		t.Fatalf("empty reference logged warning %q", output)
	}
}

func TestStoreExactReferenceLookupContinuesExistingSession(t *testing.T) {
	store := openTestStore(t)
	first, existed, err := store.BeginMessageWithReference("message-1", "thread-a", "", TierPlain)
	if err != nil || existed {
		t.Fatalf("first BeginMessageWithReference = %+v, %v, %v", first, existed, err)
	}
	if err := store.MarkRunning(first.MessageID, "first prompt"); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreResult(first.MessageID, RunResult{Kind: ResultAnswer, Text: "first answer"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(first.MessageID, "completed", "outbound-1"); err != nil {
		t.Fatal(err)
	}

	second, existed, err := store.BeginMessageWithReference(
		"message-2", "thread-b", first.Session.SessionID, TierPlain,
	)
	if err != nil || existed {
		t.Fatalf("exact BeginMessageWithReference = %+v, %v, %v", second, existed, err)
	}
	if second.ThreadID != first.ThreadID || second.Session.SessionID != first.Session.SessionID {
		t.Fatalf("exact reference created another session: first=%+v second=%+v", first, second)
	}
	if second.Session.Sequence != 2 || second.Session.IsNew {
		t.Fatalf("exact-reference continuation = %+v, want sequence 2 existing", second.Session)
	}
}

func TestStoreUnknownShortReferenceStartsFreshSession(t *testing.T) {
	store := openTestStore(t)
	var warnings bytes.Buffer
	store.warnings = log.New(&warnings, "", 0)
	pending, existed, err := store.BeginMessageWithReference(
		"message-new", "thread-new", "dm1-kyf1e-4cze7x", TierPlain,
	)
	if err != nil || existed {
		t.Fatalf("BeginMessageWithReference = %+v, %v, %v", pending, existed, err)
	}
	if !pending.Session.IsNew || pending.Session.Sequence != 1 {
		t.Fatalf("unknown short reference session = %+v, want fresh session", pending.Session)
	}
	if !validConversationReference(pending.Session.SessionID) {
		t.Fatalf("fresh canonical session ID = %q", pending.Session.SessionID)
	}
	if pending.Session.SessionID == testCanonicalConversationReference {
		t.Fatal("fresh session reused the unknown inbound reference")
	}
	if output := warnings.String(); !strings.Contains(output, "unknown conversation reference") ||
		!strings.Contains(output, testCanonicalConversationReference) || strings.Contains(output, "collision") {
		t.Fatalf("unknown-reference warning = %q", output)
	}
}

func TestStoreRetriesGeneratedSessionIDCollision(t *testing.T) {
	store := openTestStore(t)
	generated := []string{
		"dm1-01234-56789a",
		"dm1-01234-56789a",
		"dm1-abcde-fghjkm",
	}
	calls := 0
	store.referenceGenerator = func() string {
		reference := generated[calls]
		calls++
		return reference
	}

	first, existed, err := store.BeginMessage("message-first", "thread-first", TierPlain)
	if err != nil || existed {
		t.Fatalf("first BeginMessage = %+v, %v, %v", first, existed, err)
	}
	second, existed, err := store.BeginMessage("message-second", "thread-second", TierPlain)
	if err != nil || existed {
		t.Fatalf("second BeginMessage = %+v, %v, %v", second, existed, err)
	}
	if first.Session.SessionID != generated[0] {
		t.Fatalf("first session ID = %q, want %q", first.Session.SessionID, generated[0])
	}
	if second.Session.SessionID != generated[2] || calls != 3 {
		t.Fatalf("retried session = %+v after %d generator calls", second.Session, calls)
	}
}

func TestStoreSkipAcceptsLearnedExternalThreadAlias(t *testing.T) {
	store := openTestStore(t)
	first, _, err := store.BeginMessageWithReference("message-1", "thread-a", "", TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(first.MessageID, "prompt"); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreResult(first.MessageID, RunResult{Kind: ResultAnswer, Text: "answer"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(first.MessageID, "completed", "outbound-1"); err != nil {
		t.Fatal(err)
	}
	second, _, err := store.BeginMessageWithReference(
		"message-2", "thread-b", first.Session.SessionID, TierPlain,
	)
	if err != nil {
		t.Fatal(err)
	}
	abandoned, err := store.SkipMessages(
		[]MessageRef{{MessageID: second.MessageID, ThreadID: "thread-b"}},
		"operator skipped alias",
	)
	if err != nil {
		t.Fatalf("SkipMessages through alias: %v", err)
	}
	if len(abandoned) != 1 || abandoned[0].SessionID != first.Session.SessionID {
		t.Fatalf("abandoned = %+v", abandoned)
	}
	skipped, err := store.SkippedMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || skipped[0].ThreadID != "thread-b" {
		t.Fatalf("skipped = %+v, want provider alias thread-b", skipped)
	}
	if session, err := store.Session("thread-b"); err != nil || session.SessionID != first.Session.SessionID {
		t.Fatalf("committed session after skip = %+v, %v", session, err)
	}
}

func TestStoreBeginMessageAtomicallyClaimsDuplicate(t *testing.T) {
	store := openTestStore(t)
	start := make(chan struct{})
	type result struct {
		pending PendingMessage
		existed bool
		err     error
	}
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			pending, existed, err := store.BeginMessage("message-1", "thread-1", TierPlain)
			results <- result{pending: pending, existed: existed, err: err}
		}()
	}
	ready.Wait()
	close(start)

	var created, existing result
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatalf("BeginMessage: %v", got.err)
		}
		if got.existed {
			existing = got
		} else {
			created = got
		}
	}
	if created.pending.MessageID == "" || existing.pending.MessageID == "" {
		t.Fatalf("claims = created %+v, existing %+v", created, existing)
	}
	if created.pending != existing.pending {
		t.Fatalf("claims differ: created %+v, existing %+v", created.pending, existing.pending)
	}
	if created.pending.Session.ResponseTier != TierPlain ||
		existing.pending.Session.ResponseTier != TierPlain {
		t.Fatalf("claims carry unexpected tier: created %+v, existing %+v", created, existing)
	}
}

func TestStorePendingOrdersCreationThenMessageID(t *testing.T) {
	store := openTestStore(t)
	for _, id := range []string{"message-b", "message-a", "message-c"} {
		if _, _, err := store.BeginMessage(id, "thread-"+id, TierPlain); err != nil {
			t.Fatalf("BeginMessage(%s): %v", id, err)
		}
	}
	if _, err := store.db.Exec(
		`UPDATE pending_messages SET created_at = CASE message_id
             WHEN 'message-c' THEN '2026-08-01T00:00:01Z'
             ELSE '2026-08-01T00:00:00Z' END`,
	); err != nil {
		t.Fatalf("set deterministic timestamps: %v", err)
	}

	pending, err := store.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	var got []string
	for _, message := range pending {
		got = append(got, message.MessageID)
	}
	want := []string{"message-a", "message-b", "message-c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("pending order = %v, want %v", got, want)
	}
}

func TestStoreSequenceConflictRollsBackCompletion(t *testing.T) {
	store := openTestStore(t)
	pending, _, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil {
		t.Fatalf("BeginMessage: %v", err)
	}
	if err := store.MarkRunning(pending.MessageID, "prompt"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := store.StoreResult(pending.MessageID, RunResult{Kind: ResultAnswer, Text: "answer"}); err != nil {
		t.Fatalf("StoreResult: %v", err)
	}
	if _, err := store.db.Exec(
		`UPDATE thread_sessions SET sequence = 7 WHERE thread_id = ?`,
		pending.ThreadID,
	); err != nil {
		t.Fatalf("inject sequence conflict: %v", err)
	}

	err = store.Complete(pending.MessageID, "completed", "reply-1")
	if err == nil || !strings.Contains(err.Error(), "prior sequence is not 0") {
		t.Fatalf("Complete sequence conflict error = %v", err)
	}
	if seen, err := store.Seen(pending.MessageID); err != nil || seen {
		t.Fatalf("failed completion Seen = %v, %v", seen, err)
	}
	remaining, err := store.Pending()
	if err != nil || len(remaining) != 1 || remaining[0].State != messageResultReady {
		t.Fatalf("pending after rollback = %+v, %v", remaining, err)
	}
}

func TestStoreReopenPreservesRecoveryStates(t *testing.T) {
	tests := []struct {
		name       string
		advance    func(*testing.T, *Store, string)
		wantState  string
		wantPrompt string
		wantKind   ResultKind
		wantText   string
	}{
		{name: "received", wantState: messageReceived},
		{
			name: "running",
			advance: func(t *testing.T, store *Store, id string) {
				t.Helper()
				if err := store.MarkRunning(id, "saved prompt"); err != nil {
					t.Fatalf("MarkRunning: %v", err)
				}
			},
			wantState:  messageRunning,
			wantPrompt: "saved prompt",
		},
		{
			name: "result ready",
			advance: func(t *testing.T, store *Store, id string) {
				t.Helper()
				if err := store.MarkRunning(id, "saved prompt"); err != nil {
					t.Fatalf("MarkRunning: %v", err)
				}
				if err := store.StoreResult(id, RunResult{Kind: ResultQuestion, Text: "saved question"}); err != nil {
					t.Fatalf("StoreResult: %v", err)
				}
			},
			wantState:  messageResultReady,
			wantPrompt: "saved prompt",
			wantKind:   ResultQuestion,
			wantText:   "saved question",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			store, err := OpenStore(path)
			if err != nil {
				t.Fatalf("OpenStore: %v", err)
			}
			created, _, err := store.BeginMessage("message-1", "thread-1", TierPlain)
			if err != nil {
				t.Fatalf("BeginMessage: %v", err)
			}
			if test.advance != nil {
				test.advance(t, store, created.MessageID)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			reopened, err := OpenStore(path)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			defer reopened.Close()
			pending, err := reopened.Pending()
			if err != nil || len(pending) != 1 {
				t.Fatalf("Pending after reopen = %+v, %v", pending, err)
			}
			got := pending[0]
			if got.State != test.wantState || got.Prompt != test.wantPrompt ||
				got.ResultKind != test.wantKind || got.ResultText != test.wantText ||
				got.Session.SessionID != created.Session.SessionID ||
				got.Session.ResponseTier != TierPlain ||
				got.Session.Sequence != 1 || !got.Session.IsNew {
				t.Fatalf("reopened pending = %+v", got)
			}
		})
	}
}

func TestStoreCompletedSessionResumesAtNextSequenceAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	first, _, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil {
		t.Fatalf("BeginMessage first: %v", err)
	}
	if err := store.MarkRunning(first.MessageID, "first prompt"); err != nil {
		t.Fatalf("MarkRunning first: %v", err)
	}
	if err := store.StoreResult(first.MessageID, RunResult{Kind: ResultAnswer, Text: "first answer"}); err != nil {
		t.Fatalf("StoreResult first: %v", err)
	}
	if err := store.Complete(first.MessageID, "completed", "reply-1"); err != nil {
		t.Fatalf("Complete first: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	second, existed, err := reopened.BeginMessage("message-2", "thread-1", TierPlain)
	if err != nil || existed {
		t.Fatalf("BeginMessage second = %+v, %v, %v", second, existed, err)
	}
	if second.Session.SessionID != first.Session.SessionID || second.Session.Sequence != 2 ||
		second.Session.ResponseTier != TierPlain || second.Session.IsNew || second.Session.Status != "completed" {
		t.Fatalf("resumed session = %+v, first = %+v", second.Session, first.Session)
	}
}

func TestStoreSkipAndUnskipPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	ref := MessageRef{MessageID: "message-1", ThreadID: "thread-1"}
	abandoned, err := store.SkipMessages([]MessageRef{ref}, "stale request")
	if err != nil || len(abandoned) != 0 {
		t.Fatalf("SkipMessages = %+v, %v", abandoned, err)
	}
	if skipped, err := store.IsSkipped(ref.MessageID); err != nil || !skipped {
		t.Fatalf("IsSkipped = %v, %v", skipped, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store, err = OpenStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()
	messages, err := store.SkippedMessages()
	if err != nil || len(messages) != 1 {
		t.Fatalf("SkippedMessages = %+v, %v", messages, err)
	}
	if got := messages[0]; got.MessageID != ref.MessageID || got.ThreadID != ref.ThreadID ||
		got.Reason != "stale request" || got.SkippedAt == "" {
		t.Fatalf("skipped record = %+v", got)
	}
	if err := store.UnskipMessages([]string{ref.MessageID}); err != nil {
		t.Fatalf("UnskipMessages: %v", err)
	}
	if skipped, err := store.IsSkipped(ref.MessageID); err != nil || skipped {
		t.Fatalf("IsSkipped after unskip = %v, %v", skipped, err)
	}
	if err := store.UnskipMessages([]string{ref.MessageID}); err == nil {
		t.Fatal("second UnskipMessages succeeded")
	}
}

func TestStoreSkipAbandonsInitialPendingSession(t *testing.T) {
	store := openTestStore(t)
	pending, _, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil {
		t.Fatalf("BeginMessage: %v", err)
	}
	if err := store.MarkRunning(pending.MessageID, "prompt"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	abandoned, err := store.SkipMessages(
		[]MessageRef{{MessageID: pending.MessageID, ThreadID: pending.ThreadID}},
		"cancel interrupted request",
	)
	if err != nil || len(abandoned) != 1 {
		t.Fatalf("SkipMessages = %+v, %v", abandoned, err)
	}
	if got := abandoned[0]; got.SessionID != pending.Session.SessionID || !got.CleanupSession {
		t.Fatalf("abandoned message = %+v", got)
	}
	if messages, err := store.Pending(); err != nil || len(messages) != 0 {
		t.Fatalf("Pending = %+v, %v", messages, err)
	}
	if _, err := store.Session(pending.ThreadID); err == nil {
		t.Fatal("provisional thread session remains")
	}
	if err := store.UnskipMessages([]string{pending.MessageID}); err != nil {
		t.Fatalf("UnskipMessages: %v", err)
	}
	restarted, existed, err := store.BeginMessage(pending.MessageID, pending.ThreadID, TierPlain)
	if err != nil || existed {
		t.Fatalf("BeginMessage after unskip = %+v, %v, %v", restarted, existed, err)
	}
	if restarted.Session.SessionID == pending.Session.SessionID || !restarted.Session.IsNew {
		t.Fatalf("session was not restarted cleanly: old=%+v new=%+v", pending.Session, restarted.Session)
	}
}

func TestStoreSkipReceivedFollowupPreservesCommittedSession(t *testing.T) {
	store := openTestStore(t)
	first, _, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil {
		t.Fatalf("BeginMessage first: %v", err)
	}
	if err := store.MarkRunning(first.MessageID, "prompt"); err != nil {
		t.Fatalf("MarkRunning first: %v", err)
	}
	if err := store.StoreResult(first.MessageID, RunResult{Kind: ResultAnswer, Text: "done"}); err != nil {
		t.Fatalf("StoreResult first: %v", err)
	}
	if err := store.Complete(first.MessageID, "completed", "reply-1"); err != nil {
		t.Fatalf("Complete first: %v", err)
	}
	second, _, err := store.BeginMessage("message-2", "thread-1", TierPlain)
	if err != nil {
		t.Fatalf("BeginMessage second: %v", err)
	}
	abandoned, err := store.SkipMessages(
		[]MessageRef{{MessageID: second.MessageID, ThreadID: second.ThreadID}},
		"skip unread follow-up",
	)
	if err != nil || len(abandoned) != 1 || abandoned[0].CleanupSession {
		t.Fatalf("SkipMessages = %+v, %v", abandoned, err)
	}
	session, err := store.Session(second.ThreadID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if session.SessionID != first.Session.SessionID || session.Sequence != 1 {
		t.Fatalf("preserved session = %+v", session)
	}
}

func TestStoreRejectsRunningFollowupSkipAtomically(t *testing.T) {
	store := openTestStore(t)
	first, _, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil {
		t.Fatalf("BeginMessage first: %v", err)
	}
	if err := store.MarkRunning(first.MessageID, "prompt"); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreResult(first.MessageID, RunResult{Kind: ResultAnswer, Text: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(first.MessageID, "completed", "reply-1"); err != nil {
		t.Fatal(err)
	}
	second, _, err := store.BeginMessage("message-2", "thread-1", TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(second.MessageID, "follow-up prompt"); err != nil {
		t.Fatal(err)
	}
	_, err = store.SkipMessages(
		[]MessageRef{{MessageID: second.MessageID, ThreadID: second.ThreadID}},
		"unsafe cancellation",
	)
	if err == nil || !strings.Contains(err.Error(), "cannot safely skip in-progress follow-up") {
		t.Fatalf("SkipMessages error = %v", err)
	}
	if skipped, err := store.IsSkipped(second.MessageID); err != nil || skipped {
		t.Fatalf("IsSkipped after rejected transaction = %v, %v", skipped, err)
	}
	pending, err := store.Pending()
	if err != nil || len(pending) != 1 || pending[0].MessageID != second.MessageID {
		t.Fatalf("Pending after rejected transaction = %+v, %v", pending, err)
	}
}

func TestStoreAbandonRunningFollowupKeepsCanonicalSessionID(t *testing.T) {
	store := openTestStore(t)
	const checkpointSessionID = "agent-20260821T141425-8795"
	first, _, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(first.MessageID, "first prompt"); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreResult(first.MessageID, RunResult{Kind: ResultAnswer, Text: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(first.MessageID, "completed", "reply-1"); err != nil {
		t.Fatal(err)
	}
	second, _, err := store.BeginMessage("message-2", "thread-1", TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunningWithCheckpoint(
		second.MessageID,
		"partial follow-up",
		checkpointSessionID,
	); err != nil {
		t.Fatal(err)
	}

	plan, err := store.PrepareAbandon(second.MessageID)
	if err != nil {
		t.Fatalf("PrepareAbandon: %v", err)
	}
	if plan.SessionID != first.Session.SessionID || plan.PendingSequence != 2 ||
		plan.CommittedSequence != 1 {
		t.Fatalf("abandon plan = %+v", plan)
	}
	if plan.CheckpointSessionID != checkpointSessionID {
		t.Fatalf("checkpoint session = %q", plan.CheckpointSessionID)
	}
	if err := store.CommitAbandon(plan, "stuck test client"); err != nil {
		t.Fatalf("CommitAbandon: %v", err)
	}
	if pending, err := store.Pending(); err != nil || len(pending) != 0 {
		t.Fatalf("Pending = %+v, %v", pending, err)
	}
	if skipped, err := store.IsSkipped(second.MessageID); err != nil || !skipped {
		t.Fatalf("IsSkipped = %v, %v", skipped, err)
	}
	session, err := store.Session(second.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionID != first.Session.SessionID || session.Sequence != 1 {
		t.Fatalf("stable session after abandon = %+v", session)
	}

	if err := store.UnskipMessages([]string{second.MessageID}); err != nil {
		t.Fatal(err)
	}
	restarted, existed, err := store.BeginMessage(second.MessageID, second.ThreadID, TierPlain)
	if err != nil || existed {
		t.Fatalf("BeginMessage after unskip = %+v, %v, %v", restarted, existed, err)
	}
	if restarted.Session.SessionID != first.Session.SessionID || restarted.Session.Sequence != 2 ||
		restarted.Session.IsNew {
		t.Fatalf("clean continuation = %+v", restarted.Session)
	}
}

func TestStoreAbandonRejectsNonRunningOrProvisionalMessage(t *testing.T) {
	store := openTestStore(t)
	first, _, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareAbandon(first.MessageID); err == nil ||
		!strings.Contains(err.Error(), "not a running follow-up") {
		t.Fatalf("PrepareAbandon received error = %v", err)
	}
	if err := store.MarkRunning(first.MessageID, "prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareAbandon(first.MessageID); err == nil ||
		!strings.Contains(err.Error(), "use inbox skip instead") {
		t.Fatalf("PrepareAbandon provisional error = %v", err)
	}
}

func TestStoreMarkRunningWithCheckpointAcceptsOpaqueForkID(t *testing.T) {
	store := openTestStore(t)
	pending, _, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	const checkpointSessionID = "agent-20260821T141425-8795"

	err = store.MarkRunningWithCheckpoint(
		pending.MessageID,
		"prompt",
		checkpointSessionID,
	)
	if err != nil {
		t.Fatalf("MarkRunningWithCheckpoint: %v", err)
	}
	got, found, err := store.PendingByID(pending.MessageID)
	if err != nil || !found || got.State != messageRunning ||
		got.CheckpointSessionID != checkpointSessionID {
		t.Fatalf("pending after checkpoint = %+v, %v, %v", got, found, err)
	}
}

func TestStorePrepareAbandonAcceptsOpaqueCheckpointID(t *testing.T) {
	store := openTestStore(t)
	const checkpointSessionID = "agent-20260821T141425-8795"
	stable, pending := prepareRunningFollowup(t, store, checkpointSessionID)

	plan, err := store.PrepareAbandon(pending.MessageID)
	if err != nil {
		t.Fatalf("PrepareAbandon: %v", err)
	}
	if plan.SessionID != stable.Session.SessionID ||
		!isCanonicalConversationReference(plan.SessionID) {
		t.Fatalf("stable session ID = %q", plan.SessionID)
	}
	if plan.CheckpointSessionID != checkpointSessionID {
		t.Fatalf("checkpoint session ID = %q", plan.CheckpointSessionID)
	}
}

func TestStoreCommitAbandonAcceptsOpaqueCheckpointID(t *testing.T) {
	store := openTestStore(t)
	const checkpointSessionID = "agent-20260821T141425-8795"
	stable, pending := prepareRunningFollowup(t, store, checkpointSessionID)
	plan, err := store.PrepareAbandon(pending.MessageID)
	if err != nil {
		t.Fatalf("PrepareAbandon: %v", err)
	}

	if err := store.CommitAbandon(plan, "stuck test client"); err != nil {
		t.Fatalf("CommitAbandon: %v", err)
	}
	if !isCanonicalConversationReference(stable.Session.SessionID) {
		t.Fatalf("stable source session ID = %q", stable.Session.SessionID)
	}
	if pending, err := store.Pending(); err != nil || len(pending) != 0 {
		t.Fatalf("Pending = %+v, %v", pending, err)
	}
	var storedSessionID string
	if err := store.db.QueryRow(
		`SELECT session_id FROM thread_sessions WHERE thread_id = ?`,
		pending.ThreadID,
	).Scan(&storedSessionID); err != nil {
		t.Fatalf("query stable thread: %v", err)
	}
	if storedSessionID != stable.Session.SessionID {
		t.Fatalf("stored session ID = %q, want stable %q", storedSessionID, stable.Session.SessionID)
	}
}

func TestStoreLoadPendingRejectsNonCanonicalStoredSessionButAcceptsOpaqueCheckpoint(t *testing.T) {
	store := openTestStore(t)
	pending, _, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	const checkpointSessionID = "agent-20260821T141425-8795"
	if _, err := store.db.Exec(
		`UPDATE pending_messages SET checkpoint_session_id = ? WHERE message_id = ?`,
		checkpointSessionID,
		pending.MessageID,
	); err != nil {
		t.Fatalf("set checkpoint fixture: %v", err)
	}

	got, found, err := store.PendingByID(pending.MessageID)
	if err != nil || !found {
		t.Fatalf("PendingByID with opaque checkpoint = %+v, %v, %v", got, found, err)
	}
	if got.Session.SessionID != pending.Session.SessionID ||
		got.CheckpointSessionID != checkpointSessionID {
		t.Fatalf("pending with opaque checkpoint = %+v", got)
	}

	if _, err := store.db.Exec(
		`UPDATE thread_sessions SET session_id = ? WHERE thread_id = ?`,
		"agent-not-a-stable-conversation-reference",
		pending.ThreadID,
	); err != nil {
		t.Fatalf("corrupt stable session fixture: %v", err)
	}
	if _, _, err := store.PendingByID(pending.MessageID); err == nil ||
		!strings.Contains(err.Error(), "stored canonical session ID") {
		t.Fatalf("PendingByID corrupt stable session error = %v", err)
	}
}

func TestStoreAbandonRejectsStalePlanAtomically(t *testing.T) {
	store := openTestStore(t)
	checkpointSessionID := "agent-20260821T141425-8795"
	first, _, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(first.MessageID, "prompt"); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreResult(first.MessageID, RunResult{Kind: ResultAnswer, Text: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(first.MessageID, "completed", "reply-1"); err != nil {
		t.Fatal(err)
	}
	second, _, err := store.BeginMessage("message-2", "thread-1", TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunningWithCheckpoint(
		second.MessageID,
		"partial",
		checkpointSessionID,
	); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PrepareAbandon(second.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	plan.CommittedSequence++
	if err := store.CommitAbandon(plan, "stale"); err == nil ||
		!strings.Contains(err.Error(), "changed before abandonment") {
		t.Fatalf("CommitAbandon stale error = %v", err)
	}
	if skipped, err := store.IsSkipped(second.MessageID); err != nil || skipped {
		t.Fatalf("IsSkipped after stale plan = %v, %v", skipped, err)
	}
	session, err := store.Session(second.ThreadID)
	if err != nil || session.SessionID != first.Session.SessionID {
		t.Fatalf("Session after stale plan = %+v, %v", session, err)
	}
}

func TestStoreRejectsSkipWhenMessageThreadDoesNotMatchPendingState(t *testing.T) {
	store := openTestStore(t)
	if _, _, err := store.BeginMessage("message-1", "thread-1", TierPlain); err != nil {
		t.Fatal(err)
	}
	_, err := store.SkipMessages(
		[]MessageRef{{MessageID: "message-1", ThreadID: "different-thread"}},
		"bad remote state",
	)
	if err == nil || !strings.Contains(err.Error(), "belongs to thread thread-1") {
		t.Fatalf("SkipMessages error = %v", err)
	}
	if skipped, err := store.IsSkipped("message-1"); err != nil || skipped {
		t.Fatalf("IsSkipped after rejected mismatch = %v, %v", skipped, err)
	}
	pending, err := store.Pending()
	if err != nil || len(pending) != 1 {
		t.Fatalf("Pending after rejected mismatch = %+v, %v", pending, err)
	}
}

func TestStoreClosedDatabaseErrors(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := store.Seen("message-1"); err == nil {
		t.Fatal("Seen succeeded on closed database")
	}
	if _, _, err := store.BeginMessage("message-1", "thread-1", TierPlain); err == nil {
		t.Fatal("BeginMessage succeeded on closed database")
	}
	if _, err := store.Pending(); err == nil {
		t.Fatal("Pending succeeded on closed database")
	}
	if _, err := store.IsSkipped("message-1"); err == nil {
		t.Fatal("IsSkipped succeeded on closed database")
	}
	if _, err := store.SkippedMessages(); err == nil {
		t.Fatal("SkippedMessages succeeded on closed database")
	}
	if _, err := store.Session("thread-1"); err == nil || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Session closed database error = %v", err)
	}
}

func TestBeginMessageSnapshotsResponseTier(t *testing.T) {
	store := openTestStore(t)
	first, existed, err := store.BeginMessage("m1", "thread-1", TierFormatted)
	if err != nil || existed {
		t.Fatalf("first BeginMessage = %+v, %v, %v", first, existed, err)
	}
	if first.Session.ResponseTier != TierFormatted {
		t.Fatalf("first pending response tier = %q, want %q", first.Session.ResponseTier, TierFormatted)
	}

	second, existed, err := store.BeginMessage("m2", "thread-1", TierPlain)
	if err != nil || existed {
		t.Fatalf("second BeginMessage = %+v, %v, %v", second, existed, err)
	}
	if second.Session.ResponseTier != TierFormatted {
		t.Fatalf(
			"second pending response tier = %q, want stored %q",
			second.Session.ResponseTier,
			TierFormatted,
		)
	}

	for _, pending := range []PendingMessage{first, second} {
		if err := store.MarkRunning(pending.MessageID, "prompt"); err != nil {
			t.Fatalf("MarkRunning(%s): %v", pending.MessageID, err)
		}
		if err := store.StoreResult(pending.MessageID, RunResult{Kind: ResultAnswer, Text: "answer"}); err != nil {
			t.Fatalf("StoreResult(%s): %v", pending.MessageID, err)
		}
		if err := store.Complete(pending.MessageID, "completed", "reply"); err != nil {
			t.Fatalf("Complete(%s): %v", pending.MessageID, err)
		}
	}
	if pending, err := store.Pending(); err != nil || len(pending) != 0 {
		t.Fatalf("Pending after completion = %+v, %v", pending, err)
	}
}

func TestBeginMessageDefaultsEmptyResponseTierToPlain(t *testing.T) {
	store := openTestStore(t)
	pending, existed, err := store.BeginMessage("m1", "thread-2", "")
	if err != nil || existed {
		t.Fatalf("BeginMessage = %+v, %v, %v", pending, existed, err)
	}
	if pending.Session.ResponseTier != TierPlain {
		t.Fatalf("pending response tier = %q, want %q", pending.Session.ResponseTier, TierPlain)
	}
}

func TestBeginMessageRejectsInvalidResponseTierOnNewThread(t *testing.T) {
	store := openTestStore(t)
	_, _, err := store.BeginMessage("m1", "thread-3", ResponseTier("fancy"))
	if err == nil || !strings.Contains(err.Error(), "response tier must be one of plain, formatted, complete") {
		t.Fatalf("BeginMessage error = %v", err)
	}
	var count int
	if err := store.db.QueryRow(
		`SELECT COUNT(*) FROM thread_sessions WHERE thread_id = ?`,
		"thread-3",
	).Scan(&count); err != nil {
		t.Fatalf("query thread session: %v", err)
	}
	if count != 0 {
		t.Fatalf("thread_sessions rows = %d, want 0", count)
	}
}

func TestStoreResultWithManifestPersistsManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	pending, existed, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil || existed {
		t.Fatalf("BeginMessage = %+v, %v, %v", pending, existed, err)
	}
	if err := store.MarkRunning(pending.MessageID, "prompt"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	manifestJSON := `{"turn_key":"s1-abc","files":[{"filename":"a.txt","sha256":"..","size_bytes":1}]}`
	if err := store.StoreResultWithManifest(
		pending.MessageID,
		RunResult{Kind: ResultAnswer, Text: "X"},
		manifestJSON,
	); err != nil {
		t.Fatalf("StoreResultWithManifest: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	got, found, err := reopened.PendingByID(pending.MessageID)
	if err != nil || !found {
		t.Fatalf("PendingByID = %+v, %v, %v", got, found, err)
	}
	if got.State != messageResultReady {
		t.Fatalf("state = %q, want %q", got.State, messageResultReady)
	}
	if got.ResultKind != ResultAnswer || got.ResultText != "X" {
		t.Fatalf("result = kind %q text %q, want answer/X", got.ResultKind, got.ResultText)
	}
	if got.ResultManifest != manifestJSON {
		t.Fatalf("ResultManifest = %q, want %q", got.ResultManifest, manifestJSON)
	}
	if err := reopened.Complete(pending.MessageID, "completed", "reply-1"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

func TestStoreResultPersistsMagnificaHumanitas(t *testing.T) {
	store := openTestStore(t)
	pending, existed, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil || existed {
		t.Fatalf("BeginMessage = %+v, %v, %v", pending, existed, err)
	}
	if err := store.MarkRunning(pending.MessageID, "prompt"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	want := &MagnificaHumanitas{
		Paragraph: 7,
		Line:      3,
		Quote:     "The durable word outlives the interrupted messenger.",
	}
	if err := store.StoreResultWithManifest(
		pending.MessageID,
		RunResult{Kind: ResultAnswer, Text: "answer", MagnificaHumanitas: want},
		`{"turn_key":"turn-1"}`,
	); err != nil {
		t.Fatalf("StoreResultWithManifest: %v", err)
	}

	got, found, err := store.PendingByID(pending.MessageID)
	if err != nil || !found {
		t.Fatalf("PendingByID = %+v, %v, %v", got, found, err)
	}
	if got.State != messageResultReady || got.ResultText != "answer" ||
		got.ResultManifest != `{"turn_key":"turn-1"}` {
		t.Fatalf("stored result = %+v", got)
	}
	if got.MagnificaHumanitas == nil || *got.MagnificaHumanitas != *want {
		t.Fatalf("MagnificaHumanitas = %+v, want %+v", got.MagnificaHumanitas, want)
	}
}

func TestStoreResultRecoveryPreservesMagnificaHumanitas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	pending, _, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil {
		t.Fatalf("BeginMessage: %v", err)
	}
	if err := store.MarkRunning(pending.MessageID, "prompt"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	want := &MagnificaHumanitas{Paragraph: 11, Line: 2, Quote: "Keep the chosen line."}
	if err := store.StoreResult(
		pending.MessageID,
		RunResult{Kind: ResultAnswer, Text: "saved answer", MagnificaHumanitas: want},
	); err != nil {
		t.Fatalf("StoreResult: %v", err)
	}
	// Simulate a crash after StoreResult and before Complete.
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	got, err := reopened.Pending()
	if err != nil || len(got) != 1 {
		t.Fatalf("Pending after recovery = %+v, %v", got, err)
	}
	if got[0].State != messageResultReady || got[0].ResultKind != ResultAnswer ||
		got[0].ResultText != "saved answer" {
		t.Fatalf("recovered result = %+v", got[0])
	}
	if got[0].MagnificaHumanitas == nil || *got[0].MagnificaHumanitas != *want {
		t.Fatalf("recovered MagnificaHumanitas = %+v, want %+v", got[0].MagnificaHumanitas, want)
	}
}

func TestStoreResultWithoutMagnificaHumanitasRoundTripsNil(t *testing.T) {
	store := openTestStore(t)
	pending, _, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil {
		t.Fatalf("BeginMessage: %v", err)
	}
	if err := store.MarkRunning(pending.MessageID, "prompt"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := store.StoreResult(
		pending.MessageID,
		RunResult{Kind: ResultAnswer, Text: "answer"},
	); err != nil {
		t.Fatalf("StoreResult: %v", err)
	}
	got, found, err := store.PendingByID(pending.MessageID)
	if err != nil || !found {
		t.Fatalf("PendingByID = %+v, %v, %v", got, found, err)
	}
	if got.MagnificaHumanitas != nil {
		t.Fatalf("MagnificaHumanitas = %+v, want nil", got.MagnificaHumanitas)
	}
	if err := store.Complete(pending.MessageID, "completed", "reply-1"); err != nil {
		t.Fatalf("Complete without quote: %v", err)
	}
}

func TestOpenStoreAddsMagnificaHumanitasToPriorSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	if _, err := db.Exec(priorStoreSchema); err != nil {
		t.Fatalf("create prior schema: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	sessionID := newConversationReference()
	if _, err := db.Exec(
		`INSERT INTO thread_sessions
		     (thread_id, session_id, sequence, status, response_tier, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"thread-1", sessionID, 0, "active", TierPlain, now, now,
	); err != nil {
		t.Fatalf("insert prior session: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO pending_messages
		     (message_id, thread_id, sequence, state, prompt, result_kind, result_text,
		      result_manifest, checkpoint_session_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"message-1", "thread-1", 1, messageResultReady, "prompt", ResultAnswer,
		"legacy answer", `{"turn_key":"legacy"}`, "", now, now,
	); err != nil {
		t.Fatalf("insert prior pending result: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore prior schema: %v", err)
	}
	defer store.Close()
	got, found, err := store.PendingByID("message-1")
	if err != nil || !found {
		t.Fatalf("PendingByID legacy result = %+v, %v, %v", got, found, err)
	}
	if got.ResultKind != ResultAnswer || got.ResultText != "legacy answer" ||
		got.ResultManifest != `{"turn_key":"legacy"}` || got.MagnificaHumanitas != nil {
		t.Fatalf("migrated legacy result = %+v", got)
	}
	var addedColumn int
	rows, err := store.db.Query(`PRAGMA table_info(pending_messages)`)
	if err != nil {
		t.Fatalf("inspect migrated schema: %v", err)
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan migrated schema: %v", err)
		}
		if name == "magnifica_humanitas" {
			addedColumn++
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close schema rows: %v", err)
	}
	if addedColumn != 1 {
		t.Fatalf("magnifica_humanitas columns = %d, want 1", addedColumn)
	}

	if _, err := store.db.Exec(
		`UPDATE pending_messages SET magnifica_humanitas = ? WHERE message_id = ?`,
		`{"paragraph":`,
		"message-1",
	); err != nil {
		t.Fatalf("store malformed quote: %v", err)
	}
	got, found, err = store.PendingByID("message-1")
	if err != nil || !found || got.MagnificaHumanitas != nil {
		t.Fatalf("PendingByID malformed quote = %+v, %v, %v; want nil quote", got, found, err)
	}
}

func TestSessionRejectsCorruptStoredResponseTier(t *testing.T) {
	store := openTestStore(t)
	if _, _, err := store.BeginMessage("m1", "thread-1", TierPlain); err != nil {
		t.Fatalf("BeginMessage: %v", err)
	}
	if _, err := store.db.Exec(
		`UPDATE thread_sessions SET response_tier = 'fancy' WHERE thread_id = 'thread-1'`,
	); err != nil {
		t.Fatalf("corrupt stored response tier: %v", err)
	}
	if _, err := store.Session("thread-1"); err == nil ||
		!strings.Contains(err.Error(), "stored response tier") {
		t.Fatalf("Session corrupt tier error = %v", err)
	}
}

func prepareRunningFollowup(
	t *testing.T,
	store *Store,
	checkpointSessionID string,
) (PendingMessage, PendingMessage) {
	t.Helper()
	stable, _, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(stable.MessageID, "first prompt"); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreResult(
		stable.MessageID,
		RunResult{Kind: ResultAnswer, Text: "done"},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(stable.MessageID, "completed", "reply-1"); err != nil {
		t.Fatal(err)
	}
	pending, _, err := store.BeginMessage("message-2", "thread-1", TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunningWithCheckpoint(
		pending.MessageID,
		"partial follow-up",
		checkpointSessionID,
	); err != nil {
		t.Fatal(err)
	}
	return stable, pending
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

const priorStoreSchema = `
CREATE TABLE thread_sessions (
    thread_id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL UNIQUE,
    sequence INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'active',
    response_tier TEXT NOT NULL DEFAULT 'plain',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE processed_messages (
    message_id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    outbound_message_id TEXT NOT NULL DEFAULT '',
    processed_at TEXT NOT NULL
);

CREATE TABLE pending_messages (
    message_id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    state TEXT NOT NULL,
    prompt TEXT NOT NULL DEFAULT '',
    result_kind TEXT NOT NULL DEFAULT '',
    result_text TEXT NOT NULL DEFAULT '',
    result_manifest TEXT NOT NULL DEFAULT '',
    checkpoint_session_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(thread_id, sequence)
);

CREATE TABLE skipped_messages (
    message_id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    skipped_at TEXT NOT NULL
);

CREATE TABLE thread_aliases (
    external_thread_id TEXT PRIMARY KEY,
    canonical_thread_id TEXT NOT NULL
);`
