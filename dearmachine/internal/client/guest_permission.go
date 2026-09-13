package client

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"syscall"
	"time"
)

// ReceiveAuthorizer operates on one exact inbox, address and permission
// direction. Directional adapters reuse this exact-entry reconciliation contract.
// Token
// identifies the observed rule version. Empty tokens never establish ownership.
// RemoveReceive must preserve a rule whose token no longer matches.
type ReceiveAuthorizer interface {
	InspectReceive(context.Context, string) (ReceivePermission, error)
	AddReceive(context.Context, string) (ReceivePermission, error)
	RemoveReceive(context.Context, string, string) error
}
type ReceivePermission struct {
	Present bool
	Token   string
}
type GuestPermissionStatus struct {
	InboxID, Address, Direction string
	Permanent, Pending, Owned   bool
	Operation, LastError        string
}

func (s *GuestStore) PermanentReceive(inboxID, address string) error {
	unlock, err := s.providerLock(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	return s.permanentReceive(inboxID, address)
}
func (s *GuestStore) permanentReceive(inboxID, address string) error {
	canonical, err := canonicalMessageAddress(address)
	if err != nil || canonical != address || inboxID == "" {
		return errors.New("exact inbox and canonical paired address are required")
	}
	_, err = s.db.Exec(`INSERT INTO receive_permissions(inbox_id,address,permanent) VALUES(?,?,1) ON CONFLICT(inbox_id,address,direction) DO UPDATE SET permanent=1,pending=1`, inboxID, address)
	return err
}
func (s *GuestStore) PermissionStatus(inboxID string) ([]GuestPermissionStatus, error) {
	rows, err := s.db.Query(`SELECT inbox_id,address,direction,permanent,pending,owned,operation,last_error FROM receive_permissions WHERE inbox_id=? ORDER BY address,direction`, inboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GuestPermissionStatus{}
	for rows.Next() {
		var p GuestPermissionStatus
		if err = rows.Scan(&p.InboxID, &p.Address, &p.Direction, &p.Permanent, &p.Pending, &p.Owned, &p.Operation, &p.LastError); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// The advisory lock serializes provider reconcilers across daemon and CLI.
// Local revocation never needs this lock and commits while remote I/O is slow.
func (s *GuestStore) providerLock(ctx context.Context) (func(), error) {
	return privateFileLock(ctx, s.path+".reconcile.lock")
}

func privateFileLock(ctx context.Context, path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
func (s *GuestStore) Reconcile(ctx context.Context, inboxID string, provider ReceiveAuthorizer) error {
	if provider == nil {
		return errors.New("provider receive authorization is unsupported")
	}
	unlock, err := s.providerLock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	providers := map[string]ReceiveAuthorizer{"receive": provider}
	if directional, ok := provider.(interface {
		GuestPermissionAuthorizers() map[string]ReceiveAuthorizer
	}); ok {
		providers = directional.GuestPermissionAuthorizers()
	}
	if err := s.preparePermissionDirections(inboxID, providers); err != nil {
		return err
	}
	statuses, err := s.PermissionStatus(inboxID)
	if err != nil {
		return err
	}
	var failures []error
	for _, status := range statuses {
		if !status.Pending {
			continue
		}
		if err := s.reconcileEntry(ctx, inboxID, status.Address, status.Direction, providers[status.Direction]); err != nil {
			// Provider error strings can contain private request data. Persist only a
			// content-free status; the caller receives the actual failure for reporting.
			_, persistErr := s.db.Exec(`UPDATE receive_permissions SET pending=1,last_error='provider synchronization failed' WHERE inbox_id=? AND address=? AND direction=?`, inboxID, status.Address, status.Direction)
			failures = append(failures, errors.Join(fmt.Errorf("%s permission for %s: %w", status.Direction, status.Address, err), persistErr))
		}
	}
	return errors.Join(failures...)
}
func (s *GuestStore) reconcileEntry(ctx context.Context, inbox, address, direction string, p ReceiveAuthorizer) error {
	if p == nil {
		return errors.New("provider permission direction unsupported")
	}
	// Re-read desired state after each remote operation; a concurrent local
	// revoke/add cannot be overwritten by a stale successful response.
	for attempt := 0; attempt < 8; attempt++ {
		var needed, owned bool
		var token, operation string
		err := s.db.QueryRow(`SELECT (permanent=1 OR EXISTS(SELECT 1 FROM guest_grants WHERE inbox_id=? AND address=? AND active=1)),owned,token,operation FROM receive_permissions WHERE inbox_id=? AND address=? AND direction=?`, inbox, address, inbox, address, direction).Scan(&needed, &owned, &token, &operation)
		if err != nil {
			return err
		}
		observed, err := p.InspectReceive(ctx, address)
		if err != nil {
			return err
		}
		if operation == "add" || (owned && (!observed.Present || token == "" || token != observed.Token)) {
			// An interrupted addition or externally changed entry is never adopted.
			owned = false
			token = ""
			if _, err = s.db.Exec(`UPDATE receive_permissions SET owned=0,token='',operation='' WHERE inbox_id=? AND address=? AND direction=?`, inbox, address, direction); err != nil {
				return err
			}
		}
		switch {
		case needed && !observed.Present:
			if _, err = s.db.Exec(`UPDATE receive_permissions SET operation='add',pending=1,owned=0,token='' WHERE inbox_id=? AND address=? AND direction=?`, inbox, address, direction); err != nil {
				return err
			}
			created, err := p.AddReceive(ctx, address)
			if err != nil {
				return err
			}
			if !created.Present {
				return errors.New("provider did not confirm receive permission")
			}
			if _, err = s.db.Exec(`UPDATE receive_permissions SET operation='',owned=?,token=? WHERE inbox_id=? AND address=? AND direction=?`, created.Token != "", created.Token, inbox, address, direction); err != nil {
				return err
			}
			continue
		case !needed && observed.Present && owned:
			if _, err = s.db.Exec(`UPDATE receive_permissions SET operation='remove',pending=1 WHERE inbox_id=? AND address=? AND direction=?`, inbox, address, direction); err != nil {
				return err
			}
			if err = p.RemoveReceive(ctx, address, token); err != nil {
				return err
			}
			if _, err = s.db.Exec(`UPDATE receive_permissions SET operation='',owned=0,token='' WHERE inbox_id=? AND address=? AND direction=?`, inbox, address, direction); err != nil {
				return err
			}
			continue
		}
		// Atomically clear intent only if the desired state still matches the
		// snapshot just inspected. Otherwise loop and reconcile the new intent.
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		var current bool
		err = tx.QueryRow(`SELECT (permanent=1 OR EXISTS(SELECT 1 FROM guest_grants WHERE inbox_id=? AND address=? AND active=1)) FROM receive_permissions WHERE inbox_id=? AND address=? AND direction=?`, inbox, address, inbox, address, direction).Scan(&current)
		if err != nil {
			tx.Rollback()
			return err
		}
		if current != needed {
			tx.Rollback()
			continue
		}
		_, err = tx.Exec(`UPDATE receive_permissions SET pending=0,operation='',last_error='' WHERE inbox_id=? AND address=? AND direction=?`, inbox, address, direction)
		if err != nil {
			tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return errors.New("receive permission changed repeatedly; reconciliation remains pending")
}

// AuthorizePair records permanent receive intent before the adapter establishes
// all permissions needed by a permanent pair. An interrupted creation retains
// its conservative permanent reservation and can safely resume.
func (s *GuestStore) AuthorizePair(ctx context.Context, inbox Inbox, address string, authorize func() error) error {
	unlock, err := s.providerLock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	canonical, err := canonicalMessageAddress(address)
	if err != nil {
		return err
	}
	if err := s.permanentReceive(guestInboxKey(inbox), canonical); err != nil {
		return err
	}
	return authorize()
}

// Directional rows share the grant reference predicate, but never ownership.
// Materialize durable intent before any provider operation, including upgrades
// from a database that only tracked receiving.
func (s *GuestStore) preparePermissionDirections(inbox string, providers map[string]ReceiveAuthorizer) error {
	directions := make([]string, 0, len(providers))
	for direction, p := range providers {
		if p == nil || (direction != "receive" && direction != "reply" && direction != "send") {
			return errors.New("invalid provider permission direction")
		}
		if direction != "receive" {
			directions = append(directions, direction)
		}
	}
	sort.Strings(directions)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, direction := range directions {
		_, err = tx.Exec(`INSERT OR IGNORE INTO receive_permissions(inbox_id,address,direction,permanent,pending)
   SELECT inbox_id,address,?,permanent,1 FROM receive_permissions WHERE inbox_id=? AND direction='receive'`, direction, inbox)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE receive_permissions AS dest SET
    permanent=(SELECT permanent FROM receive_permissions src WHERE src.inbox_id=dest.inbox_id AND src.address=dest.address AND src.direction='receive'),
    pending=MAX(pending,(SELECT pending FROM receive_permissions src WHERE src.inbox_id=dest.inbox_id AND src.address=dest.address AND src.direction='receive'))
    WHERE inbox_id=? AND direction=?`, inbox, direction)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func migrateGuestPermissionDirections(db *sql.DB) error {
	// Inspect under the same write transaction as the migration so concurrent
	// daemon/CLI startup cannot both migrate the old table.
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`PRAGMA table_info(receive_permissions)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		found = found || name == "direction"
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !found {
		_, err = tx.Exec(`ALTER TABLE receive_permissions RENAME TO receive_permissions_v1;
   CREATE TABLE receive_permissions(inbox_id TEXT NOT NULL,address TEXT NOT NULL,direction TEXT NOT NULL DEFAULT 'receive',
   permanent INTEGER NOT NULL DEFAULT 0,pending INTEGER NOT NULL DEFAULT 1,owned INTEGER NOT NULL DEFAULT 0,
   token TEXT NOT NULL DEFAULT '',operation TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',PRIMARY KEY(inbox_id,address,direction));
   INSERT INTO receive_permissions SELECT inbox_id,address,'receive',permanent,pending,owned,token,operation,last_error FROM receive_permissions_v1;
   DROP TABLE receive_permissions_v1;`)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
