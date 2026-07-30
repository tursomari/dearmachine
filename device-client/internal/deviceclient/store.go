package deviceclient

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
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

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite store: %w", err)
	}
	db.SetMaxOpenConns(1)

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
    processed_at TEXT NOT NULL
);`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate SQLite store: %w", err)
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

func (s *Store) ReserveSession(threadID string) (Session, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Session{}, fmt.Errorf("begin session reservation: %w", err)
	}
	defer tx.Rollback()

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
			Sequence:  1,
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
	case err == nil:
		session.Sequence++
		session.Status = "active"
		_, err = tx.Exec(
			`UPDATE thread_sessions
			    SET sequence = ?, status = ?, updated_at = ?
			  WHERE thread_id = ?`,
			session.Sequence,
			session.Status,
			now,
			threadID,
		)
	}
	if err != nil {
		return Session{}, fmt.Errorf("reserve thread session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit session reservation: %w", err)
	}
	return session, nil
}

func (s *Store) Complete(messageID, threadID, status string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin message completion: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(
		`INSERT INTO processed_messages (message_id, thread_id, processed_at)
		 VALUES (?, ?, ?)`,
		messageID,
		threadID,
		now,
	); err != nil {
		return fmt.Errorf("record processed message: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE thread_sessions SET status = ?, updated_at = ? WHERE thread_id = ?`,
		status,
		now,
		threadID,
	); err != nil {
		return fmt.Errorf("update thread status: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit message completion: %w", err)
	}
	return nil
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
