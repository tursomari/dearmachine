package deviceclient

import (
	"database/sql"
	"errors"
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

func TestStoreMigratesPreOutboundMessageIDSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	legacySchema := `
CREATE TABLE thread_sessions (
    thread_id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL UNIQUE,
    sequence INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE processed_messages (
    message_id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    processed_at TEXT NOT NULL
);
INSERT INTO thread_sessions VALUES
    ('legacy-thread', 'legacy-session', 1, 'completed', 'created', 'updated');
INSERT INTO processed_messages VALUES
    ('legacy-message', 'legacy-thread', 'processed');`
	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("migrate legacy store: %v", err)
	}
	var outboundID string
	if err := store.db.QueryRow(
		`SELECT outbound_message_id FROM processed_messages WHERE message_id = ?`,
		"legacy-message",
	).Scan(&outboundID); err != nil {
		t.Fatalf("query migrated row: %v", err)
	}
	if outboundID != "" {
		t.Fatalf("migrated outbound_message_id = %q, want empty", outboundID)
	}
	if seen, err := store.Seen("legacy-message"); err != nil || !seen {
		t.Fatalf("Seen legacy message = %v, %v", seen, err)
	}
	if _, _, err := store.BeginMessage("new-message", "new-thread"); err != nil {
		t.Fatalf("pending_messages migration missing: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close migrated store: %v", err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	defer reopened.Close()
	if seen, err := reopened.Seen("legacy-message"); err != nil || !seen {
		t.Fatalf("Seen after repeat migration = %v, %v", seen, err)
	}
}

func TestStoreStateMachineGuards(t *testing.T) {
	store := openTestStore(t)
	pending, existed, err := store.BeginMessage("message-1", "thread-1")
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
	first, existed, err := store.BeginMessage("message-1", "thread-1")
	if err != nil || existed {
		t.Fatalf("first BeginMessage = %+v, %v, %v", first, existed, err)
	}
	if err := store.MarkRunning(first.MessageID, "durable prompt"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	duplicate, existed, err := store.BeginMessage("message-1", "thread-1")
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
			pending, existed, err := store.BeginMessage("message-1", "thread-1")
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
}

func TestStorePendingOrdersCreationThenMessageID(t *testing.T) {
	store := openTestStore(t)
	for _, id := range []string{"message-b", "message-a", "message-c"} {
		if _, _, err := store.BeginMessage(id, "thread-"+id); err != nil {
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
	pending, _, err := store.BeginMessage("message-1", "thread-1")
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
			created, _, err := store.BeginMessage("message-1", "thread-1")
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
	first, _, err := store.BeginMessage("message-1", "thread-1")
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
	second, existed, err := reopened.BeginMessage("message-2", "thread-1")
	if err != nil || existed {
		t.Fatalf("BeginMessage second = %+v, %v, %v", second, existed, err)
	}
	if second.Session.SessionID != first.Session.SessionID || second.Session.Sequence != 2 ||
		second.Session.IsNew || second.Session.Status != "completed" {
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
	pending, _, err := store.BeginMessage("message-1", "thread-1")
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
	restarted, existed, err := store.BeginMessage(pending.MessageID, pending.ThreadID)
	if err != nil || existed {
		t.Fatalf("BeginMessage after unskip = %+v, %v, %v", restarted, existed, err)
	}
	if restarted.Session.SessionID == pending.Session.SessionID || !restarted.Session.IsNew {
		t.Fatalf("session was not restarted cleanly: old=%+v new=%+v", pending.Session, restarted.Session)
	}
}

func TestStoreSkipReceivedFollowupPreservesCommittedSession(t *testing.T) {
	store := openTestStore(t)
	first, _, err := store.BeginMessage("message-1", "thread-1")
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
	second, _, err := store.BeginMessage("message-2", "thread-1")
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
	first, _, err := store.BeginMessage("message-1", "thread-1")
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
	second, _, err := store.BeginMessage("message-2", "thread-1")
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

func TestStoreAbandonRunningFollowupRemapsCommittedSession(t *testing.T) {
	store := openTestStore(t)
	first, _, err := store.BeginMessage("message-1", "thread-1")
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
	second, _, err := store.BeginMessage("message-2", "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunningWithCheckpoint(
		second.MessageID,
		"partial follow-up",
		"replacement-session",
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
	if plan.CheckpointSessionID != "replacement-session" {
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
	if session.SessionID != "replacement-session" || session.Sequence != 1 {
		t.Fatalf("replacement session = %+v", session)
	}

	if err := store.UnskipMessages([]string{second.MessageID}); err != nil {
		t.Fatal(err)
	}
	restarted, existed, err := store.BeginMessage(second.MessageID, second.ThreadID)
	if err != nil || existed {
		t.Fatalf("BeginMessage after unskip = %+v, %v, %v", restarted, existed, err)
	}
	if restarted.Session.SessionID != "replacement-session" || restarted.Session.Sequence != 2 ||
		restarted.Session.IsNew {
		t.Fatalf("clean continuation = %+v", restarted.Session)
	}
}

func TestStoreAbandonRejectsNonRunningOrProvisionalMessage(t *testing.T) {
	store := openTestStore(t)
	first, _, err := store.BeginMessage("message-1", "thread-1")
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

func TestStoreAbandonRejectsStalePlanAtomically(t *testing.T) {
	store := openTestStore(t)
	first, _, err := store.BeginMessage("message-1", "thread-1")
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
	second, _, err := store.BeginMessage("message-2", "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunningWithCheckpoint(
		second.MessageID,
		"partial",
		"replacement-session",
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
	if _, _, err := store.BeginMessage("message-1", "thread-1"); err != nil {
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
	if _, _, err := store.BeginMessage("message-1", "thread-1"); err == nil {
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

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
