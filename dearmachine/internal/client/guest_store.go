package client

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

var ErrGuestUnauthorized = errors.New("guest authorization is absent, revoked, or stale")

// GuestStore is shared by every pair and by concurrent CLI/daemon processes.
// All mutations use SQLite BEGIN IMMEDIATE, including the execution-start
// critical section. Only content-free identifiers and authorization facts live here.
type GuestStore struct {
	db   *sql.DB
	path string
}
type GuestKey struct{ PairID, InboxID, Address, ThreadID string }
type GuestGrant struct {
	GuestKey
	Evidence   string
	Generation int64
	Active     bool
}

func DefaultGuestDatabasePath(home func() (string, error)) (string, error) {
	root, err := resolveDeviceHome(home)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".dearmachine", "authorization.db"), nil
}

func OpenGuestStore(path string) (*GuestStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("guest database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	err = f.Chmod(0600)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	uri := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite3", uri.String()+"?_busy_timeout=10000&_journal_mode=WAL&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
 CREATE TABLE IF NOT EXISTS guest_grants (
 pair_id TEXT NOT NULL, inbox_id TEXT NOT NULL, address TEXT NOT NULL, thread_id TEXT NOT NULL,
 evidence TEXT NOT NULL, generation INTEGER NOT NULL CHECK(generation>0), active INTEGER NOT NULL CHECK(active IN (0,1)),
 PRIMARY KEY(pair_id,inbox_id,address,thread_id));
 CREATE TABLE IF NOT EXISTS guest_evidence (
 pair_id TEXT NOT NULL, inbox_id TEXT NOT NULL, address TEXT NOT NULL, thread_id TEXT NOT NULL, message_id TEXT NOT NULL,
 PRIMARY KEY(pair_id,inbox_id,address,thread_id,message_id));
 CREATE TABLE IF NOT EXISTS guest_work (
 pair_id TEXT NOT NULL, inbox_id TEXT NOT NULL, message_id TEXT NOT NULL, address TEXT NOT NULL, thread_id TEXT NOT NULL,
 generation INTEGER NOT NULL, PRIMARY KEY(pair_id,inbox_id,message_id));
 CREATE TABLE IF NOT EXISTS receive_permissions (
 inbox_id TEXT NOT NULL,address TEXT NOT NULL,permanent INTEGER NOT NULL DEFAULT 0,
 pending INTEGER NOT NULL DEFAULT 1,owned INTEGER NOT NULL DEFAULT 0,token TEXT NOT NULL DEFAULT '',
 operation TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(inbox_id,address));`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &GuestStore{db: db, path: path}, nil
}
func (s *GuestStore) Close() error { return s.db.Close() }
func (k GuestKey) validate() error {
	if k.PairID == "" || k.InboxID == "" || k.ThreadID == "" {
		return errors.New("exact pair, inbox and provider thread are required")
	}
	canonical, err := canonicalMessageAddress(k.Address)
	if err != nil || canonical != k.Address {
		return errors.New("canonical guest address is required")
	}
	return nil
}

const grantWhere = `pair_id=? AND inbox_id=? AND address=? AND thread_id=?`

func (k GuestKey) args() []any { return []any{k.PairID, k.InboxID, k.Address, k.ThreadID} }

type guestQuery interface{ QueryRow(string, ...any) *sql.Row }

func guestGrant(q guestQuery, k GuestKey) (GuestGrant, error) {
	g := GuestGrant{GuestKey: k}
	err := q.QueryRow(`SELECT evidence,generation,active FROM guest_grants WHERE `+grantWhere, k.args()...).Scan(&g.Evidence, &g.Generation, &g.Active)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return g, err
}
func (s *GuestStore) Grant(k GuestKey) (GuestGrant, error) {
	if err := k.validate(); err != nil {
		return GuestGrant{}, err
	}
	return guestGrant(s.db, k)
}

// Allow is internal persistence after invitation validation. Explicit means a
// local operator deliberately reauthorizes; automatic replay never resurrects.
func (s *GuestStore) Allow(k GuestKey, evidence string, explicit bool) (GuestGrant, error) {
	unlock, err := s.providerLock(context.Background())
	if err != nil {
		return GuestGrant{}, err
	}
	defer unlock()

	if err := k.validate(); err != nil {
		return GuestGrant{}, err
	}
	if strings.TrimSpace(evidence) == "" {
		return GuestGrant{}, errors.New("invitation message ID is required")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return GuestGrant{}, err
	}
	defer tx.Rollback()
	g, err := guestGrant(tx, k)
	if err != nil {
		return g, err
	}
	args := append(k.args(), evidence)
	res, err := tx.Exec(`INSERT OR IGNORE INTO guest_evidence VALUES(?,?,?,?,?)`, args...)
	if err != nil {
		return g, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return g, err
	}
	if n == 0 && !explicit {
		return g, tx.Commit()
	}
	// New evidence for an already-active grant does not invalidate ongoing work.
	if !g.Active {
		g.Generation++
		g.Active = true
		g.Evidence = evidence
		args = append(k.args(), evidence, g.Generation)
		_, err = tx.Exec(`INSERT INTO guest_grants VALUES(?,?,?,?,?,?,1) ON CONFLICT(pair_id,inbox_id,address,thread_id) DO UPDATE SET evidence=excluded.evidence,generation=excluded.generation,active=1`, args...)
		if err != nil {
			return g, err
		}
	}
	_, err = tx.Exec(`INSERT INTO receive_permissions(inbox_id,address) VALUES(?,?) ON CONFLICT(inbox_id,address) DO UPDATE SET pending=1`, k.InboxID, k.Address)
	if err != nil {
		return g, err
	}
	return g, tx.Commit()
}
func (s *GuestStore) Revoke(k GuestKey, all bool) error {
	if all {
		k.ThreadID = "*"
	}
	if err := k.validate(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	where := grantWhere
	args := k.args()
	if all {
		where = `pair_id=? AND inbox_id=? AND address=?`
		args = args[:3]
	}
	if _, err = tx.Exec(`UPDATE guest_grants SET active=0 WHERE `+where, args...); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE receive_permissions SET pending=1 WHERE inbox_id=? AND address=?`, k.InboxID, k.Address); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *GuestStore) List(pairID string) ([]GuestGrant, error) {
	rows, err := s.db.Query(`SELECT pair_id,inbox_id,address,thread_id,evidence,generation,active FROM guest_grants WHERE pair_id=? ORDER BY address,thread_id`, pairID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grants := []GuestGrant{}
	for rows.Next() {
		var g GuestGrant
		if err = rows.Scan(&g.PairID, &g.InboxID, &g.Address, &g.ThreadID, &g.Evidence, &g.Generation, &g.Active); err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}
func checkGuestWork(q guestQuery, k GuestKey, messageID string) error {
	var valid bool
	args := append(k.args(), messageID)
	err := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM guest_grants g JOIN guest_work w USING(pair_id,inbox_id,address,thread_id,generation) WHERE g.pair_id=? AND g.inbox_id=? AND g.address=? AND g.thread_id=? AND w.message_id=? AND g.active=1)`, args...).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrGuestUnauthorized
	}
	return nil
}
func (s *GuestStore) CheckWork(k GuestKey, messageID string) error {
	return checkGuestWork(s.db, k, messageID)
}
func (s *GuestStore) BindWork(k GuestKey, messageID string) error {
	if messageID == "" {
		return ErrGuestUnauthorized
	}
	if err := k.validate(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	g, err := guestGrant(tx, k)
	if err != nil {
		return err
	}
	if !g.Active {
		return ErrGuestUnauthorized
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO guest_work VALUES(?,?,?,?,?,?)`, k.PairID, k.InboxID, messageID, k.Address, k.ThreadID, g.Generation)
	if err != nil {
		return err
	}
	if err = checkGuestWork(tx, k, messageID); err != nil {
		return err
	}
	return tx.Commit()
}

// WithWorkStart orders revocation against the actual subprocess Start call.
// The transaction holds the write lock through Start, never through Wait. A
// crash after Start may have external effects; no rollback guarantee is made.
func (s *GuestStore) WithWorkStart(k GuestKey, messageID string, start func() error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkGuestWork(tx, k, messageID); err != nil {
		return err
	}
	if err = start(); err != nil {
		return err
	}
	return tx.Commit()
}
func guestInboxKey(inbox Inbox) string {
	return inbox.ID + "/" + inbox.Transport + "/" + inbox.ProviderID
}
func guestKey(pair Pair, inbox Inbox, message Message) (GuestKey, error) {
	address, err := canonicalMessageAddress(message.From)
	if err != nil {
		return GuestKey{}, fmt.Errorf("guest sender: %w", err)
	}
	return GuestKey{pair.ID, guestInboxKey(inbox), address, message.ThreadID}, nil
}

// BoundWorkKey distinguishes a controller's independent message from a queued
// replacement instruction released by a participant request.
func (s *GuestStore) BoundWorkKey(pairID, inboxID, messageID string) (GuestKey, bool, error) {
	k := GuestKey{PairID: pairID, InboxID: inboxID}
	err := s.db.QueryRow(`SELECT address,thread_id FROM guest_work WHERE pair_id=? AND inbox_id=? AND message_id=?`, pairID, inboxID, messageID).Scan(&k.Address, &k.ThreadID)
	if errors.Is(err, sql.ErrNoRows) {
		return k, false, nil
	}
	return k, err == nil, err
}
func (s *GuestStore) BindReplacement(k GuestKey, requestID, replacementID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkGuestWork(tx, k, requestID); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO guest_work SELECT pair_id,inbox_id,?,address,thread_id,generation FROM guest_work WHERE pair_id=? AND inbox_id=? AND message_id=?`, replacementID, k.PairID, k.InboxID, requestID)
	if err != nil {
		return err
	}
	if err = checkGuestWork(tx, k, replacementID); err != nil {
		return err
	}
	return tx.Commit()
}
