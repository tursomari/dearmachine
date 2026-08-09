package deviceclient

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db *sql.DB
}

type Session struct {
	ThreadID  string
	SessionID string
	Sequence  int
	Status    string
	IsNew     bool
}

type PendingMessage struct {
	MessageID           string
	ThreadID            string
	Session             Session
	CheckpointSessionID string
	State               string
	Prompt              string
	ResultKind          ResultKind
	ResultText          string
}

type MessageRef struct {
	MessageID string
	ThreadID  string
}

type SkippedMessage struct {
	MessageID string `json:"message_id"`
	ThreadID  string `json:"thread_id"`
	Reason    string `json:"reason"`
	SkippedAt string `json:"skipped_at"`
}

type AbandonedMessage struct {
	MessageID      string
	SessionID      string
	CleanupSession bool
}

// AbandonPlan identifies an in-progress follow-up and the committed session
// boundary that must be preserved when the partial turn is abandoned.
type AbandonPlan struct {
	MessageID           string
	ThreadID            string
	SessionID           string
	CheckpointSessionID string
	PendingSequence     int
	CommittedSequence   int
}

const (
	messageReceived    = "received"
	messageRunning     = "running"
	messageResultReady = "result_ready"
)

var errMessageSkipped = errors.New("message is locally skipped")

func OpenStore(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("SQLite store path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create SQLite store directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("prepare SQLite store: %w", err)
	}
	info, statErr := file.Stat()
	if statErr == nil && !info.Mode().IsRegular() {
		statErr = fmt.Errorf("path is not a regular file")
	}
	chmodErr := file.Chmod(0o600)
	closeErr := file.Close()
	if statErr != nil {
		return nil, fmt.Errorf("inspect SQLite store: %w", statErr)
	}
	if chmodErr != nil {
		return nil, fmt.Errorf("secure SQLite store: %w", chmodErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close prepared SQLite store: %w", closeErr)
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite store: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable SQLite WAL: %w", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, fmt.Errorf("configure SQLite busy timeout: %w", err)
	}

	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS thread_sessions (
    thread_id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL UNIQUE,
    sequence INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS processed_messages (
    message_id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    outbound_message_id TEXT NOT NULL DEFAULT '',
    processed_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS pending_messages (
    message_id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    state TEXT NOT NULL,
    prompt TEXT NOT NULL DEFAULT '',
    result_kind TEXT NOT NULL DEFAULT '',
    result_text TEXT NOT NULL DEFAULT '',
    checkpoint_session_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(thread_id, sequence)
);

CREATE TABLE IF NOT EXISTS skipped_messages (
    message_id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    skipped_at TEXT NOT NULL
);`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate SQLite store: %w", err)
	}
	if err := s.addColumnIfMissing(
		"processed_messages",
		"outbound_message_id",
		`TEXT NOT NULL DEFAULT ''`,
	); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(
		"pending_messages",
		"checkpoint_session_id",
		`TEXT NOT NULL DEFAULT ''`,
	); err != nil {
		return err
	}
	return nil
}

func (s *Store) IsSkipped(messageID string) (bool, error) {
	var found int
	err := s.db.QueryRow(
		`SELECT 1 FROM skipped_messages WHERE message_id = ?`,
		messageID,
	).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query skipped message: %w", err)
	}
	return true, nil
}

func (s *Store) SkipMessages(messages []MessageRef, reason string) ([]AbandonedMessage, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("no messages selected")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin skipping messages: %w", err)
	}
	defer tx.Rollback()

	seen := make(map[string]struct{}, len(messages))
	var abandoned []AbandonedMessage
	for _, message := range messages {
		message.MessageID = strings.TrimSpace(message.MessageID)
		message.ThreadID = strings.TrimSpace(message.ThreadID)
		if message.MessageID == "" || message.ThreadID == "" {
			return nil, fmt.Errorf("message and thread IDs are required")
		}
		if _, duplicate := seen[message.MessageID]; duplicate {
			continue
		}
		seen[message.MessageID] = struct{}{}

		var existingThread string
		err := tx.QueryRow(
			`SELECT thread_id FROM skipped_messages WHERE message_id = ?`,
			message.MessageID,
		).Scan(&existingThread)
		switch {
		case err == nil && existingThread != message.ThreadID:
			return nil, fmt.Errorf(
				"skipped message %s belongs to thread %s, not %s",
				message.MessageID,
				existingThread,
				message.ThreadID,
			)
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return nil, fmt.Errorf("query existing skipped message: %w", err)
		}

		var (
			pendingThread     string
			sessionID         string
			pendingState      string
			committedSequence int
		)
		err = tx.QueryRow(
			`SELECT p.thread_id, t.session_id, p.state, t.sequence
			   FROM pending_messages p
			   JOIN thread_sessions t ON t.thread_id = p.thread_id
			  WHERE p.message_id = ?`,
			message.MessageID,
		).Scan(&pendingThread, &sessionID, &pendingState, &committedSequence)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("inspect pending message before skip: %w", err)
		}
		if err == nil && pendingThread != message.ThreadID {
			return nil, fmt.Errorf(
				"pending message %s belongs to thread %s, not %s",
				message.MessageID,
				pendingThread,
				message.ThreadID,
			)
		}
		if err == nil && pendingState != messageReceived && committedSequence > 0 {
			return nil, fmt.Errorf(
				"cannot safely skip in-progress follow-up %s: session %s already has committed history",
				message.MessageID,
				sessionID,
			)
		}

		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec(
			`INSERT INTO skipped_messages (message_id, thread_id, reason, skipped_at)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(message_id) DO UPDATE SET reason = excluded.reason`,
			message.MessageID,
			message.ThreadID,
			strings.TrimSpace(reason),
			now,
		); err != nil {
			return nil, fmt.Errorf("record skipped message: %w", err)
		}
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}

		if _, err := tx.Exec(
			`DELETE FROM pending_messages WHERE message_id = ?`,
			message.MessageID,
		); err != nil {
			return nil, fmt.Errorf("remove skipped pending message: %w", err)
		}
		if committedSequence == 0 {
			if _, err := tx.Exec(
				`DELETE FROM thread_sessions WHERE thread_id = ? AND sequence = 0`,
				message.ThreadID,
			); err != nil {
				return nil, fmt.Errorf("remove provisional thread session: %w", err)
			}
		}
		abandoned = append(abandoned, AbandonedMessage{
			MessageID:      message.MessageID,
			SessionID:      sessionID,
			CleanupSession: pendingState != messageReceived && committedSequence == 0,
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit skipped messages: %w", err)
	}
	return abandoned, nil
}

// PrepareAbandon returns the stable state needed to restore the clean session
// checkpoint created before an established follow-up started. The caller must
// stop Device Client before calling this method and keep it stopped until
// CommitAbandon succeeds.
func (s *Store) PrepareAbandon(messageID string) (AbandonPlan, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return AbandonPlan{}, fmt.Errorf("message ID is required")
	}

	var plan AbandonPlan
	var state string
	err := s.db.QueryRow(
		`SELECT p.message_id,
		        p.thread_id,
		        t.session_id,
		        p.sequence,
		        t.sequence,
		        p.checkpoint_session_id,
		        p.state
		   FROM pending_messages p
		   JOIN thread_sessions t ON t.thread_id = p.thread_id
		  WHERE p.message_id = ?`,
		messageID,
	).Scan(
		&plan.MessageID,
		&plan.ThreadID,
		&plan.SessionID,
		&plan.PendingSequence,
		&plan.CommittedSequence,
		&plan.CheckpointSessionID,
		&state,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AbandonPlan{}, fmt.Errorf("pending message %s was not found", messageID)
	}
	if err != nil {
		return AbandonPlan{}, fmt.Errorf("prepare in-progress message abandonment: %w", err)
	}
	if state != messageRunning {
		return AbandonPlan{}, fmt.Errorf(
			"message %s is %s, not a running follow-up",
			messageID,
			state,
		)
	}
	if plan.CommittedSequence <= 0 {
		return AbandonPlan{}, fmt.Errorf(
			"message %s has no committed session history; use inbox skip instead",
			messageID,
		)
	}
	if plan.PendingSequence != plan.CommittedSequence+1 {
		return AbandonPlan{}, fmt.Errorf(
			"message %s sequence %d does not follow committed sequence %d",
			messageID,
			plan.PendingSequence,
			plan.CommittedSequence,
		)
	}
	if strings.TrimSpace(plan.CheckpointSessionID) == "" {
		return AbandonPlan{}, fmt.Errorf(
			"message %s has no clean pre-run session checkpoint",
			messageID,
		)
	}
	if plan.CheckpointSessionID == plan.SessionID {
		return AbandonPlan{}, fmt.Errorf(
			"message %s has an invalid pre-run session checkpoint",
			messageID,
		)
	}
	return plan, nil
}

// CommitAbandon atomically remaps a thread to a clean fork of its committed
// mct history, records the partial message as locally skipped, and removes its
// durable pending row. The plan is revalidated so a stale plan cannot rewrite
// newer state.
func (s *Store) CommitAbandon(plan AbandonPlan, reason string) error {
	if strings.TrimSpace(plan.MessageID) == "" || strings.TrimSpace(plan.ThreadID) == "" ||
		strings.TrimSpace(plan.SessionID) == "" {
		return fmt.Errorf("complete abandon plan is required")
	}
	if strings.TrimSpace(plan.CheckpointSessionID) == "" ||
		plan.CheckpointSessionID == plan.SessionID {
		return fmt.Errorf("valid pre-run session checkpoint is required")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin in-progress message abandonment: %w", err)
	}
	defer tx.Rollback()

	var current AbandonPlan
	var state string
	err = tx.QueryRow(
		`SELECT p.message_id,
		        p.thread_id,
		        t.session_id,
		        p.sequence,
		        t.sequence,
		        p.checkpoint_session_id,
		        p.state
		   FROM pending_messages p
		   JOIN thread_sessions t ON t.thread_id = p.thread_id
		  WHERE p.message_id = ?`,
		plan.MessageID,
	).Scan(
		&current.MessageID,
		&current.ThreadID,
		&current.SessionID,
		&current.PendingSequence,
		&current.CommittedSequence,
		&current.CheckpointSessionID,
		&state,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("pending message %s changed before abandonment", plan.MessageID)
	}
	if err != nil {
		return fmt.Errorf("revalidate in-progress message abandonment: %w", err)
	}
	if current != plan || state != messageRunning || current.CommittedSequence <= 0 ||
		current.PendingSequence != current.CommittedSequence+1 {
		return fmt.Errorf("pending message %s changed before abandonment", plan.MessageID)
	}

	var skippedThread string
	err = tx.QueryRow(
		`SELECT thread_id FROM skipped_messages WHERE message_id = ?`,
		plan.MessageID,
	).Scan(&skippedThread)
	switch {
	case err == nil && skippedThread != plan.ThreadID:
		return fmt.Errorf(
			"skipped message %s belongs to thread %s, not %s",
			plan.MessageID,
			skippedThread,
			plan.ThreadID,
		)
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("query existing skipped message: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	update, err := tx.Exec(
		`UPDATE thread_sessions
		    SET session_id = ?, updated_at = ?
		  WHERE thread_id = ? AND session_id = ? AND sequence = ?`,
		plan.CheckpointSessionID,
		now,
		plan.ThreadID,
		plan.SessionID,
		plan.CommittedSequence,
	)
	if err != nil {
		return fmt.Errorf("replace abandoned mct session: %w", err)
	}
	changed, err := update.RowsAffected()
	if err != nil {
		return fmt.Errorf("check abandoned mct session replacement: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("thread session changed before abandonment")
	}

	if _, err := tx.Exec(
		`INSERT INTO skipped_messages (message_id, thread_id, reason, skipped_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(message_id) DO UPDATE SET reason = excluded.reason`,
		plan.MessageID,
		plan.ThreadID,
		strings.TrimSpace(reason),
		now,
	); err != nil {
		return fmt.Errorf("record abandoned message as skipped: %w", err)
	}
	deleted, err := tx.Exec(
		`DELETE FROM pending_messages WHERE message_id = ?`,
		plan.MessageID,
	)
	if err != nil {
		return fmt.Errorf("remove abandoned pending message: %w", err)
	}
	changed, err = deleted.RowsAffected()
	if err != nil {
		return fmt.Errorf("check abandoned pending message removal: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("pending message %s changed before abandonment", plan.MessageID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit in-progress message abandonment: %w", err)
	}
	return nil
}

func (s *Store) UnskipMessages(messageIDs []string) error {
	if len(messageIDs) == 0 {
		return fmt.Errorf("no messages selected")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin unskipping messages: %w", err)
	}
	defer tx.Rollback()
	seen := make(map[string]struct{}, len(messageIDs))
	for _, messageID := range messageIDs {
		messageID = strings.TrimSpace(messageID)
		if messageID == "" {
			return fmt.Errorf("message ID is required")
		}
		if _, duplicate := seen[messageID]; duplicate {
			continue
		}
		seen[messageID] = struct{}{}
		result, err := tx.Exec(
			`DELETE FROM skipped_messages WHERE message_id = ?`,
			messageID,
		)
		if err != nil {
			return fmt.Errorf("unskip message %s: %w", messageID, err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("check unskip for message %s: %w", messageID, err)
		}
		if changed != 1 {
			return fmt.Errorf("message %s is not skipped", messageID)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit unskipped messages: %w", err)
	}
	return nil
}

func (s *Store) SkippedMessages() ([]SkippedMessage, error) {
	rows, err := s.db.Query(
		`SELECT message_id, thread_id, reason, skipped_at
		   FROM skipped_messages
		  ORDER BY skipped_at, message_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("query skipped messages: %w", err)
	}
	defer rows.Close()
	messages := make([]SkippedMessage, 0)
	for rows.Next() {
		var message SkippedMessage
		if err := rows.Scan(
			&message.MessageID,
			&message.ThreadID,
			&message.Reason,
			&message.SkippedAt,
		); err != nil {
			return nil, fmt.Errorf("scan skipped message: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query skipped messages: %w", err)
	}
	return messages, nil
}

func (s *Store) addColumnIfMissing(table, column, declaration string) error {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return fmt.Errorf("inspect SQLite table %s: %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			index      int
			name       string
			columnType string
			notNull    int
			defaultVal any
			primaryKey int
		)
		if err := rows.Scan(
			&index,
			&name,
			&columnType,
			&notNull,
			&defaultVal,
			&primaryKey,
		); err != nil {
			return fmt.Errorf("scan SQLite table %s: %w", table, err)
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect SQLite table %s: %w", table, err)
	}
	if _, err := s.db.Exec(
		`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + declaration,
	); err != nil {
		return fmt.Errorf("add SQLite column %s.%s: %w", table, column, err)
	}
	return nil
}

func (s *Store) Seen(messageID string) (bool, error) {
	var found int
	err := s.db.QueryRow(
		`SELECT 1 FROM processed_messages WHERE message_id = ?`,
		messageID,
	).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query processed message: %w", err)
	}
	return true, nil
}

func (s *Store) BeginMessage(messageID, threadID string) (PendingMessage, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return PendingMessage{}, false, fmt.Errorf("begin inbound message: %w", err)
	}
	defer tx.Rollback()

	var skipped int
	err = tx.QueryRow(
		`SELECT 1 FROM skipped_messages WHERE message_id = ?`,
		messageID,
	).Scan(&skipped)
	if err == nil {
		return PendingMessage{}, false, errMessageSkipped
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return PendingMessage{}, false, fmt.Errorf("query skipped inbound message: %w", err)
	}

	pending, err := scanPending(tx.QueryRow(
		pendingMessageQuery+` WHERE p.message_id = ?`,
		messageID,
	))
	if err == nil {
		return pending, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return PendingMessage{}, false, fmt.Errorf("query pending message: %w", err)
	}

	var session Session
	err = tx.QueryRow(
		`SELECT thread_id, session_id, sequence, status
		   FROM thread_sessions
		  WHERE thread_id = ?`,
		threadID,
	).Scan(&session.ThreadID, &session.SessionID, &session.Sequence, &session.Status)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		session = Session{
			ThreadID:  threadID,
			SessionID: newUUID(),
			Sequence:  0,
			Status:    "active",
			IsNew:     true,
		}
		_, err = tx.Exec(
			`INSERT INTO thread_sessions
			     (thread_id, session_id, sequence, status, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			session.ThreadID,
			session.SessionID,
			session.Sequence,
			session.Status,
			now,
			now,
		)
	}
	if err != nil {
		return PendingMessage{}, false, fmt.Errorf("prepare thread session: %w", err)
	}

	latestSequence := session.Sequence
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(sequence), ?) FROM pending_messages WHERE thread_id = ?`,
		session.Sequence,
		threadID,
	).Scan(&latestSequence); err != nil {
		return PendingMessage{}, false, fmt.Errorf("query pending thread sequence: %w", err)
	}

	pending = PendingMessage{
		MessageID: messageID,
		ThreadID:  threadID,
		Session: Session{
			ThreadID:  threadID,
			SessionID: session.SessionID,
			Sequence:  latestSequence + 1,
			Status:    session.Status,
			IsNew:     session.Sequence == 0,
		},
		State: messageReceived,
	}
	claim, err := tx.Exec(
		`INSERT INTO pending_messages
		     (message_id, thread_id, sequence, state, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(message_id) DO NOTHING`,
		pending.MessageID,
		pending.ThreadID,
		pending.Session.Sequence,
		pending.State,
		now,
		now,
	)
	if err != nil {
		return PendingMessage{}, false, fmt.Errorf("persist inbound message: %w", err)
	}
	claimed, err := claim.RowsAffected()
	if err != nil {
		return PendingMessage{}, false, fmt.Errorf("check inbound message claim: %w", err)
	}
	if claimed == 0 {
		if err := tx.Rollback(); err != nil {
			return PendingMessage{}, false, fmt.Errorf("release duplicate inbound message claim: %w", err)
		}
		existing, err := scanPending(s.db.QueryRow(
			pendingMessageQuery+` WHERE p.message_id = ?`,
			messageID,
		))
		if err != nil {
			return PendingMessage{}, false, fmt.Errorf("query pending message after claim conflict: %w", err)
		}
		return existing, true, nil
	}
	if err := tx.Commit(); err != nil {
		return PendingMessage{}, false, fmt.Errorf("commit inbound message: %w", err)
	}
	return pending, false, nil
}

func (s *Store) PendingByID(messageID string) (PendingMessage, bool, error) {
	pending, err := scanPending(s.db.QueryRow(
		pendingMessageQuery+` WHERE p.message_id = ?`,
		messageID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return PendingMessage{}, false, nil
	}
	if err != nil {
		return PendingMessage{}, false, fmt.Errorf("query pending message: %w", err)
	}
	return pending, true, nil
}

func (s *Store) MarkRunning(messageID, prompt string) error {
	return s.MarkRunningWithCheckpoint(messageID, prompt, "")
}

func (s *Store) MarkRunningWithCheckpoint(messageID, prompt, checkpointSessionID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.Exec(
		`UPDATE pending_messages
		    SET state = ?, prompt = ?, checkpoint_session_id = ?, updated_at = ?
		  WHERE message_id = ? AND state = ?`,
		messageRunning,
		prompt,
		strings.TrimSpace(checkpointSessionID),
		now,
		messageID,
		messageReceived,
	)
	if err != nil {
		return fmt.Errorf("mark inbound message running: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check inbound message state change: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("mark inbound message running: message is not received")
	}
	return nil
}

func (s *Store) StoreResult(messageID string, result RunResult) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	update, err := s.db.Exec(
		`UPDATE pending_messages
		    SET state = ?, result_kind = ?, result_text = ?, updated_at = ?
		  WHERE message_id = ? AND state = ?`,
		messageResultReady,
		result.Kind,
		result.Text,
		now,
		messageID,
		messageRunning,
	)
	if err != nil {
		return fmt.Errorf("store mct result: %w", err)
	}
	changed, err := update.RowsAffected()
	if err != nil {
		return fmt.Errorf("check mct result state change: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("store mct result: message is not running")
	}
	return nil
}

func (s *Store) Pending() ([]PendingMessage, error) {
	rows, err := s.db.Query(pendingMessageQuery + ` ORDER BY p.created_at, p.message_id`)
	if err != nil {
		return nil, fmt.Errorf("query pending messages: %w", err)
	}
	defer rows.Close()

	var pending []PendingMessage
	for rows.Next() {
		message, err := scanPending(rows)
		if err != nil {
			return nil, fmt.Errorf("scan pending message: %w", err)
		}
		pending = append(pending, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query pending messages: %w", err)
	}
	return pending, nil
}

func (s *Store) Complete(messageID, status, outboundMessageID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin message completion: %w", err)
	}
	defer tx.Rollback()

	var threadID string
	var sequence int
	var state string
	if err := tx.QueryRow(
		`SELECT thread_id, sequence, state
		   FROM pending_messages
		  WHERE message_id = ?`,
		messageID,
	).Scan(&threadID, &sequence, &state); err != nil {
		return fmt.Errorf("get pending message completion: %w", err)
	}
	if state != messageResultReady {
		return fmt.Errorf("complete inbound message: message is not result ready")
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(
		`INSERT INTO processed_messages
		     (message_id, thread_id, outbound_message_id, processed_at)
		 VALUES (?, ?, ?, ?)`,
		messageID,
		threadID,
		outboundMessageID,
		now,
	); err != nil {
		return fmt.Errorf("record processed message: %w", err)
	}
	update, err := tx.Exec(
		`UPDATE thread_sessions
		    SET sequence = ?, status = ?, updated_at = ?
		  WHERE thread_id = ? AND sequence = ?`,
		sequence,
		status,
		now,
		threadID,
		sequence-1,
	)
	if err != nil {
		return fmt.Errorf("update thread status: %w", err)
	}
	changed, err := update.RowsAffected()
	if err != nil {
		return fmt.Errorf("check thread sequence advancement: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf(
			"advance thread sequence to %d: prior sequence is not %d",
			sequence,
			sequence-1,
		)
	}
	if _, err := tx.Exec(
		`DELETE FROM pending_messages WHERE message_id = ?`,
		messageID,
	); err != nil {
		return fmt.Errorf("remove pending message: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit message completion: %w", err)
	}
	return nil
}

const pendingMessageQuery = `
SELECT p.message_id,
       p.thread_id,
       t.session_id,
       p.sequence,
       t.status,
       p.checkpoint_session_id,
       p.state,
       p.prompt,
       p.result_kind,
       p.result_text,
       t.sequence
  FROM pending_messages p
  JOIN thread_sessions t ON t.thread_id = p.thread_id`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPending(row rowScanner) (PendingMessage, error) {
	var pending PendingMessage
	var committedSequence int
	err := row.Scan(
		&pending.MessageID,
		&pending.ThreadID,
		&pending.Session.SessionID,
		&pending.Session.Sequence,
		&pending.Session.Status,
		&pending.CheckpointSessionID,
		&pending.State,
		&pending.Prompt,
		&pending.ResultKind,
		&pending.ResultText,
		&committedSequence,
	)
	pending.Session.ThreadID = pending.ThreadID
	pending.Session.IsNew = committedSequence == 0
	return pending, err
}

func (s *Store) Session(threadID string) (Session, error) {
	var session Session
	err := s.db.QueryRow(
		`SELECT thread_id, session_id, sequence, status
		   FROM thread_sessions
		  WHERE thread_id = ?`,
		threadID,
	).Scan(&session.ThreadID, &session.SessionID, &session.Sequence, &session.Status)
	if err != nil {
		return Session{}, fmt.Errorf("get thread session: %w", err)
	}
	return session, nil
}

func newUUID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(fmt.Sprintf("generate session UUID: %v", err))
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		id[0:4],
		id[4:6],
		id[6:8],
		id[8:10],
		id[10:16],
	)
}
