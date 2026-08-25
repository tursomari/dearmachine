package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
)

type Store struct {
	db                 *sql.DB
	warnings           *log.Logger
	referenceGenerator func() string
}

type Session struct {
	ThreadID     string
	SessionID    string
	Sequence     int
	Status       string
	IsNew        bool
	ResponseTier ResponseTier
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
	ResultManifest      string
	MagnificaHumanitas  *MagnificaHumanitas
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
	messageReceived              = "received"
	messageRunning               = "running"
	messageResultReady           = "result_ready"
	sessionReferenceInsertTrials = 10
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

	store := &Store{db: db, referenceGenerator: newConversationReference}
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
    response_tier TEXT NOT NULL DEFAULT 'plain',
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
    result_manifest TEXT NOT NULL DEFAULT '',
    magnifica_humanitas TEXT NOT NULL DEFAULT '',
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
);

CREATE TABLE IF NOT EXISTS thread_aliases (
    external_thread_id TEXT PRIMARY KEY,
    canonical_thread_id TEXT NOT NULL
);`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate SQLite store: %w", err)
	}
	hasMagnificaHumanitas, err := sqliteTableHasColumn(
		s.db,
		"pending_messages",
		"magnifica_humanitas",
	)
	if err != nil {
		return fmt.Errorf("inspect SQLite store schema: %w", err)
	}
	if !hasMagnificaHumanitas {
		if _, err := s.db.Exec(
			`ALTER TABLE pending_messages
			 ADD COLUMN magnifica_humanitas TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return fmt.Errorf("add Magnifica Humanitas result storage: %w", err)
		}
	}
	return nil
}

func sqliteTableHasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(
			&cid,
			&name,
			&columnType,
			&notNull,
			&defaultValue,
			&primaryKey,
		); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
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
	return s.skipMessages(messages, reason, false)
}

// SkipPreemptedMessage atomically suppresses the active message replaced by a
// newer message on the same thread, preserving its canonical session and
// monotonic sequence number for the continuation.
func (s *Store) SkipPreemptedMessage(message MessageRef, reason string) ([]AbandonedMessage, error) {
	return s.skipMessages([]MessageRef{message}, reason, true)
}

func (s *Store) skipMessages(messages []MessageRef, reason string, preempted bool) ([]AbandonedMessage, error) {
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
		externalThreadID := message.ThreadID
		canonicalThreadID := message.ThreadID
		if resolvedThreadID, found, err := resolveThreadAlias(tx, message.ThreadID); err != nil {
			return nil, fmt.Errorf("resolve skipped message thread: %w", err)
		} else if found {
			canonicalThreadID = resolvedThreadID
		}

		var existingThread string
		err := tx.QueryRow(
			`SELECT thread_id FROM skipped_messages WHERE message_id = ?`,
			message.MessageID,
		).Scan(&existingThread)
		if err == nil {
			existingCanonical := existingThread
			if resolved, found, resolveErr := resolveThreadAlias(tx, existingThread); resolveErr != nil {
				return nil, fmt.Errorf("resolve existing skipped message thread: %w", resolveErr)
			} else if found {
				existingCanonical = resolved
			}
			if existingCanonical != canonicalThreadID {
				return nil, fmt.Errorf(
					"skipped message %s belongs to thread %s, not %s",
					message.MessageID,
					existingThread,
					externalThreadID,
				)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("query existing skipped message: %w", err)
		}

		var (
			pendingThread     string
			sessionID         string
			pendingState      string
			committedSequence int
			pendingSequence   int
		)
		err = tx.QueryRow(
			`SELECT p.thread_id, t.session_id, p.state, t.sequence, p.sequence
			   FROM pending_messages p
			   JOIN thread_sessions t ON t.thread_id = p.thread_id
			  WHERE p.message_id = ?`,
			message.MessageID,
		).Scan(&pendingThread, &sessionID, &pendingState, &committedSequence, &pendingSequence)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("inspect pending message before skip: %w", err)
		}
		if err == nil && pendingThread != canonicalThreadID {
			return nil, fmt.Errorf(
				"pending message %s belongs to thread %s, not %s",
				message.MessageID,
				pendingThread,
				externalThreadID,
			)
		}
		if err == nil && !isCanonicalConversationReference(sessionID) {
			return nil, fmt.Errorf("stored canonical session ID %q is invalid", sessionID)
		}
		if err == nil && preempted && pendingState != messageRunning {
			return nil, fmt.Errorf("preempted message %s is not running", message.MessageID)
		}
		if err == nil && pendingState != messageReceived && committedSequence > 0 && !preempted {
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
			externalThreadID,
			strings.TrimSpace(reason),
			now,
		); err != nil {
			return nil, fmt.Errorf("record skipped message: %w", err)
		}
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if preempted && pendingState == messageRunning {
			if _, err := tx.Exec(
				`UPDATE thread_sessions SET sequence = CASE WHEN sequence < ? THEN ? ELSE sequence END WHERE thread_id = ?`,
				pendingSequence, pendingSequence, canonicalThreadID,
			); err != nil {
				return nil, fmt.Errorf("preserve preempted thread sequence: %w", err)
			}
		}

		if _, err := tx.Exec(
			`DELETE FROM pending_messages WHERE message_id = ?`,
			message.MessageID,
		); err != nil {
			return nil, fmt.Errorf("remove skipped pending message: %w", err)
		}
		if committedSequence == 0 && !preempted {
			if _, err := tx.Exec(
				`DELETE FROM thread_aliases WHERE canonical_thread_id = ?`,
				canonicalThreadID,
			); err != nil {
				return nil, fmt.Errorf("remove provisional thread aliases: %w", err)
			}
			if _, err := tx.Exec(
				`DELETE FROM thread_sessions WHERE thread_id = ? AND sequence = 0`,
				canonicalThreadID,
			); err != nil {
				return nil, fmt.Errorf("remove provisional thread session: %w", err)
			}
		}
		abandoned = append(abandoned, AbandonedMessage{
			MessageID:      message.MessageID,
			SessionID:      sessionID,
			CleanupSession: pendingState != messageReceived && committedSequence == 0 && !preempted,
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit skipped messages: %w", err)
	}
	return abandoned, nil
}

// PrepareAbandon returns the stable state needed to restore the clean session
// checkpoint created before an established follow-up started. The caller must
// stop DearMachine Client before calling this method and keep it stopped until
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
	if !isCanonicalConversationReference(plan.SessionID) {
		return AbandonPlan{}, fmt.Errorf("stored canonical session ID %q is invalid", plan.SessionID)
	}
	if !isValidOpaqueSessionID(plan.CheckpointSessionID) {
		return AbandonPlan{}, fmt.Errorf(
			"stored checkpoint session ID %q is invalid",
			plan.CheckpointSessionID,
		)
	}
	return plan, nil
}

// CommitAbandon atomically records the partial message as locally skipped and
// removes its durable pending row. The stable canonical session ID remains
// unchanged while the caller restores the clean checkpoint on disk. The plan is
// revalidated so stale state cannot be abandoned.
func (s *Store) CommitAbandon(plan AbandonPlan, reason string) error {
	if strings.TrimSpace(plan.MessageID) == "" || strings.TrimSpace(plan.ThreadID) == "" ||
		strings.TrimSpace(plan.SessionID) == "" {
		return fmt.Errorf("complete abandon plan is required")
	}
	if strings.TrimSpace(plan.CheckpointSessionID) == "" ||
		plan.CheckpointSessionID == plan.SessionID {
		return fmt.Errorf("valid pre-run session checkpoint is required")
	}
	if !isCanonicalConversationReference(plan.SessionID) {
		return fmt.Errorf("abandon source session ID must be a canonical conversation reference")
	}
	if !isValidOpaqueSessionID(plan.CheckpointSessionID) {
		return fmt.Errorf("abandon checkpoint session ID is invalid")
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
	changed, err := deleted.RowsAffected()
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

func (s *Store) CountProcessedSince(since time.Time) (int, error) {
	var count int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM processed_messages WHERE processed_at > ?`,
		since.UTC().Format(time.RFC3339Nano),
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("count processed messages: %w", err)
	}
	return count, nil
}

func (s *Store) BeginMessage(messageID, threadID string, responseTier ResponseTier) (PendingMessage, bool, error) {
	return s.BeginMessageWithReference(messageID, threadID, "", responseTier)
}

func (s *Store) warnUnknownConversationReference(reference string) {
	if s.warnings != nil {
		s.warnings.Printf(
			"unknown conversation reference: reference %q matches no conversations; starting a new session",
			reference,
		)
	}
}

func (s *Store) insertThreadSession(tx *sql.Tx, session Session, now string) (string, error) {
	generate := s.referenceGenerator
	if generate == nil {
		generate = newConversationReference
	}
	for range sessionReferenceInsertTrials {
		sessionID := generate()
		if !isCanonicalConversationReference(sessionID) {
			return "", fmt.Errorf("generated session ID %q is not canonical", sessionID)
		}
		_, err := tx.Exec(
			`INSERT INTO thread_sessions
			     (thread_id, session_id, sequence, status, response_tier, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			session.ThreadID,
			sessionID,
			session.Sequence,
			session.Status,
			session.ResponseTier,
			now,
			now,
		)
		if err == nil {
			return sessionID, nil
		}
		var sqliteErr sqlite3.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.ExtendedCode != sqlite3.ErrConstraintUnique {
			return "", err
		}
	}
	return "", fmt.Errorf(
		"generate unique session ID after %d attempts",
		sessionReferenceInsertTrials,
	)
}

func (s *Store) BeginMessageWithReference(
	messageID, externalThreadID, conversationReference string,
	responseTier ResponseTier,
) (PendingMessage, bool, error) {
	if responseTier == "" {
		responseTier = TierPlain
	}
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

	canonicalThreadID, found, err := resolveThreadAlias(tx, externalThreadID)
	if err != nil {
		return PendingMessage{}, false, fmt.Errorf("resolve external thread: %w", err)
	}
	if !found {
		reference := canonicalInboundReference(conversationReference)
		if reference != "" {
			err = tx.QueryRow(
				`SELECT thread_id FROM thread_sessions WHERE session_id = ?`,
				reference,
			).Scan(&canonicalThreadID)
			switch {
			case err == nil:
				found = true
			case errors.Is(err, sql.ErrNoRows):
				err = nil
				s.warnUnknownConversationReference(reference)
			default:
				return PendingMessage{}, false, fmt.Errorf("resolve conversation reference: %w", err)
			}
		}
	}

	var session Session
	if found {
		err = scanSession(tx.QueryRow(
			`SELECT thread_id, session_id, sequence, status, response_tier
			   FROM thread_sessions WHERE thread_id = ?`,
			canonicalThreadID,
		), &session)
	} else {
		err = sql.ErrNoRows
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		var tier ResponseTier
		tier, err = ParseResponseTier(string(responseTier))
		if err != nil {
			return PendingMessage{}, false, fmt.Errorf("prepare thread session: %w", err)
		}
		session = Session{
			ThreadID:     externalThreadID,
			Sequence:     0,
			Status:       "active",
			IsNew:        true,
			ResponseTier: tier,
		}
		session.SessionID, err = s.insertThreadSession(tx, session, now)
	}
	if err != nil {
		return PendingMessage{}, false, fmt.Errorf("prepare thread session: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO thread_aliases (external_thread_id, canonical_thread_id)
		 VALUES (?, ?)`,
		externalThreadID,
		session.ThreadID,
	); err != nil {
		return PendingMessage{}, false, fmt.Errorf("persist external thread alias: %w", err)
	}
	var mappedThreadID string
	if err := tx.QueryRow(
		`SELECT canonical_thread_id FROM thread_aliases WHERE external_thread_id = ?`,
		externalThreadID,
	).Scan(&mappedThreadID); err != nil {
		return PendingMessage{}, false, fmt.Errorf("verify external thread alias: %w", err)
	}
	if mappedThreadID != session.ThreadID {
		return PendingMessage{}, false, fmt.Errorf(
			"external thread %s is already associated with another conversation",
			externalThreadID,
		)
	}

	latestSequence := session.Sequence
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(sequence), ?) FROM pending_messages WHERE thread_id = ?`,
		session.Sequence,
		session.ThreadID,
	).Scan(&latestSequence); err != nil {
		return PendingMessage{}, false, fmt.Errorf("query pending thread sequence: %w", err)
	}

	pending = PendingMessage{
		MessageID: messageID,
		ThreadID:  session.ThreadID,
		Session: Session{
			ThreadID:     session.ThreadID,
			SessionID:    session.SessionID,
			Sequence:     latestSequence + 1,
			Status:       session.Status,
			IsNew:        session.Sequence == 0,
			ResponseTier: session.ResponseTier,
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
	checkpointSessionID = strings.TrimSpace(checkpointSessionID)
	if checkpointSessionID != "" && !isValidOpaqueSessionID(checkpointSessionID) {
		return fmt.Errorf("checkpoint session ID is invalid")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.Exec(
		`UPDATE pending_messages
		    SET state = ?, prompt = ?, checkpoint_session_id = ?, updated_at = ?
		  WHERE message_id = ? AND state = ?`,
		messageRunning,
		prompt,
		checkpointSessionID,
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
	return s.StoreResultWithManifest(messageID, result, "")
}

func (s *Store) StoreResultWithManifest(messageID string, result RunResult, manifestJSON string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	magnificaHumanitasJSON := marshalMagnificaHumanitas(result.MagnificaHumanitas)
	update, err := s.db.Exec(
		`UPDATE pending_messages
		    SET state = ?, result_kind = ?, result_text = ?, result_manifest = ?,
		        magnifica_humanitas = ?, updated_at = ?
		  WHERE message_id = ? AND state = ?`,
		messageResultReady,
		result.Kind,
		result.Text,
		manifestJSON,
		magnificaHumanitasJSON,
		now,
		messageID,
		messageRunning,
	)
	if err != nil {
		return fmt.Errorf("store agent result: %w", err)
	}
	changed, err := update.RowsAffected()
	if err != nil {
		return fmt.Errorf("check agent result state change: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("store agent result: message is not running")
	}
	return nil
}

func marshalMagnificaHumanitas(value *MagnificaHumanitas) string {
	if value == nil {
		return ""
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
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
       t.response_tier,
       p.checkpoint_session_id,
       p.state,
       p.prompt,
       p.result_kind,
       p.result_text,
       p.result_manifest,
       p.magnifica_humanitas,
       t.sequence
  FROM pending_messages p
  JOIN thread_sessions t ON t.thread_id = p.thread_id`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPending(row rowScanner) (PendingMessage, error) {
	var pending PendingMessage
	var committedSequence int
	var storedTier string
	var storedMagnificaHumanitas sql.NullString
	err := row.Scan(
		&pending.MessageID,
		&pending.ThreadID,
		&pending.Session.SessionID,
		&pending.Session.Sequence,
		&pending.Session.Status,
		&storedTier,
		&pending.CheckpointSessionID,
		&pending.State,
		&pending.Prompt,
		&pending.ResultKind,
		&pending.ResultText,
		&pending.ResultManifest,
		&storedMagnificaHumanitas,
		&committedSequence,
	)
	if err != nil {
		return PendingMessage{}, err
	}
	tier, err := ParseResponseTier(storedTier)
	if err != nil {
		return PendingMessage{}, fmt.Errorf(
			"stored response tier %q is invalid: %w",
			storedTier,
			err,
		)
	}
	if !isCanonicalConversationReference(pending.Session.SessionID) {
		return PendingMessage{}, fmt.Errorf(
			"stored canonical session ID %q is invalid",
			pending.Session.SessionID,
		)
	}
	if pending.CheckpointSessionID != "" &&
		!isValidOpaqueSessionID(pending.CheckpointSessionID) {
		return PendingMessage{}, fmt.Errorf(
			"stored checkpoint session ID %q is invalid",
			pending.CheckpointSessionID,
		)
	}
	pending.Session.ThreadID = pending.ThreadID
	pending.Session.IsNew = committedSequence == 0
	pending.Session.ResponseTier = tier
	if storedMagnificaHumanitas.Valid && strings.TrimSpace(storedMagnificaHumanitas.String) != "" {
		var magnificaHumanitas MagnificaHumanitas
		if err := json.Unmarshal([]byte(storedMagnificaHumanitas.String), &magnificaHumanitas); err == nil {
			pending.MagnificaHumanitas = &magnificaHumanitas
		}
	}
	return pending, nil
}

func (s *Store) Session(threadID string) (Session, error) {
	var session Session
	err := scanSession(s.db.QueryRow(
		`SELECT t.thread_id, t.session_id, t.sequence, t.status, t.response_tier
		   FROM thread_aliases a
		   JOIN thread_sessions t ON t.thread_id = a.canonical_thread_id
		  WHERE a.external_thread_id = ?`,
		threadID,
	), &session)
	if err != nil {
		return Session{}, fmt.Errorf("get thread session: %w", err)
	}
	return session, nil
}

func resolveThreadAlias(tx *sql.Tx, externalThreadID string) (string, bool, error) {
	var canonicalThreadID string
	err := tx.QueryRow(
		`SELECT canonical_thread_id FROM thread_aliases WHERE external_thread_id = ?`,
		externalThreadID,
	).Scan(&canonicalThreadID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return canonicalThreadID, err == nil, err
}

func scanSession(row rowScanner, session *Session) error {
	var storedTier string
	err := row.Scan(
		&session.ThreadID,
		&session.SessionID,
		&session.Sequence,
		&session.Status,
		&storedTier,
	)
	if err != nil {
		return err
	}
	tier, err := ParseResponseTier(storedTier)
	if err != nil {
		return fmt.Errorf(
			"stored response tier %q is invalid: %w",
			storedTier,
			err,
		)
	}
	if !isCanonicalConversationReference(session.SessionID) {
		return fmt.Errorf(
			"stored canonical session ID %q is invalid",
			session.SessionID,
		)
	}
	session.ResponseTier = tier
	return nil
}
