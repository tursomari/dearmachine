package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// ReceiveAuthorizer operates on one exact inbox and exact address. Token
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
	InboxID, Address          string
	Permanent, Pending, Owned bool
	Operation, LastError      string
}

func (s *GuestStore) PermanentReceive(inboxID, address string) error {
	canonical, err := canonicalMessageAddress(address)
	if err != nil || canonical != address || inboxID == "" {
		return errors.New("exact inbox and canonical paired address are required")
	}
	_, err = s.db.Exec(`INSERT INTO receive_permissions(inbox_id,address,permanent) VALUES(?,?,1) ON CONFLICT(inbox_id,address) DO UPDATE SET permanent=1,pending=1`, inboxID, address)
	return err
}
func (s *GuestStore) PermissionStatus(inboxID string) ([]GuestPermissionStatus, error) {
	rows, err := s.db.Query(`SELECT inbox_id,address,permanent,pending,owned,operation,last_error FROM receive_permissions WHERE inbox_id=? ORDER BY address`, inboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GuestPermissionStatus{}
	for rows.Next() {
		var p GuestPermissionStatus
		if err = rows.Scan(&p.InboxID, &p.Address, &p.Permanent, &p.Pending, &p.Owned, &p.Operation, &p.LastError); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// The advisory lock serializes provider reconcilers across daemon and CLI.
// Local revocation never needs this lock and commits while remote I/O is slow.
func (s *GuestStore) providerLock(ctx context.Context) (func(), error) {
	f, err := os.OpenFile(s.path+".reconcile.lock", os.O_CREATE|os.O_RDWR, 0600)
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
	statuses, err := s.PermissionStatus(inboxID)
	if err != nil {
		return err
	}
	var failures []error
	for _, status := range statuses {
		if !status.Pending {
			continue
		}
		if err := s.reconcileEntry(ctx, inboxID, status.Address, provider); err != nil {
			// Provider error strings can contain private request data. Persist only a
			// content-free status; the caller receives the actual failure for reporting.
			_, persistErr := s.db.Exec(`UPDATE receive_permissions SET pending=1,last_error='provider synchronization failed' WHERE inbox_id=? AND address=?`, inboxID, status.Address)
			failures = append(failures, errors.Join(fmt.Errorf("receive permission for %s: %w", status.Address, err), persistErr))
		}
	}
	return errors.Join(failures...)
}
func (s *GuestStore) reconcileEntry(ctx context.Context, inbox, address string, p ReceiveAuthorizer) error {
	// Re-read desired state after each remote operation; a concurrent local
	// revoke/add cannot be overwritten by a stale successful response.
	for attempt := 0; attempt < 8; attempt++ {
		var needed, owned bool
		var token, operation string
		err := s.db.QueryRow(`SELECT (permanent=1 OR EXISTS(SELECT 1 FROM guest_grants WHERE inbox_id=? AND address=? AND active=1)),owned,token,operation FROM receive_permissions WHERE inbox_id=? AND address=?`, inbox, address, inbox, address).Scan(&needed, &owned, &token, &operation)
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
			if _, err = s.db.Exec(`UPDATE receive_permissions SET owned=0,token='',operation='' WHERE inbox_id=? AND address=?`, inbox, address); err != nil {
				return err
			}
		}
		switch {
		case needed && !observed.Present:
			if _, err = s.db.Exec(`UPDATE receive_permissions SET operation='add',pending=1,owned=0,token='' WHERE inbox_id=? AND address=?`, inbox, address); err != nil {
				return err
			}
			created, err := p.AddReceive(ctx, address)
			if err != nil {
				return err
			}
			if !created.Present {
				return errors.New("provider did not confirm receive permission")
			}
			if _, err = s.db.Exec(`UPDATE receive_permissions SET operation='',owned=?,token=? WHERE inbox_id=? AND address=?`, created.Token != "", created.Token, inbox, address); err != nil {
				return err
			}
			continue
		case !needed && observed.Present && owned:
			if _, err = s.db.Exec(`UPDATE receive_permissions SET operation='remove',pending=1 WHERE inbox_id=? AND address=?`, inbox, address); err != nil {
				return err
			}
			if err = p.RemoveReceive(ctx, address, token); err != nil {
				return err
			}
			if _, err = s.db.Exec(`UPDATE receive_permissions SET operation='',owned=0,token='' WHERE inbox_id=? AND address=?`, inbox, address); err != nil {
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
		err = tx.QueryRow(`SELECT (permanent=1 OR EXISTS(SELECT 1 FROM guest_grants WHERE inbox_id=? AND address=? AND active=1)) FROM receive_permissions WHERE inbox_id=? AND address=?`, inbox, address, inbox, address).Scan(&current)
		if err != nil {
			tx.Rollback()
			return err
		}
		if current != needed {
			tx.Rollback()
			continue
		}
		_, err = tx.Exec(`UPDATE receive_permissions SET pending=0,operation='',last_error='' WHERE inbox_id=? AND address=?`, inbox, address)
		if err != nil {
			tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return errors.New("receive permission changed repeatedly; reconciliation remains pending")
}
