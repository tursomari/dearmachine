package deviceclient

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

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
