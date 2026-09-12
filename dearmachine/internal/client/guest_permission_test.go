package client

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type guestPermissionFake struct {
	present       bool
	token         string
	adds, removes int
	failAdd       bool
}

func (f *guestPermissionFake) InspectReceive(context.Context, string) (ReceivePermission, error) {
	return ReceivePermission{f.present, f.token}, nil
}
func (f *guestPermissionFake) AddReceive(context.Context, string) (ReceivePermission, error) {
	f.present = true
	f.token = "created"
	f.adds++
	if f.failAdd {
		return ReceivePermission{}, errors.New("lost response")
	}
	return ReceivePermission{true, f.token}, nil
}
func (f *guestPermissionFake) RemoveReceive(_ context.Context, _ string, token string) error {
	if token != f.token {
		return errors.New("changed")
	}
	f.present = false
	f.removes++
	return nil
}

func TestGuestPermissionsReferencesAndPermanentPairs(t *testing.T) {
	s, err := OpenGuestStore(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := GuestKey{"p1", "inbox", "guest@example.test", "t1"}
	b := GuestKey{"p2", "inbox", a.Address, "t2"}
	for _, k := range []GuestKey{a, b} {
		if _, err = s.Allow(k, "invite", true); err != nil {
			t.Fatal(err)
		}
	}
	f := &guestPermissionFake{}
	sync := func() {
		t.Helper()
		if err = s.Reconcile(context.Background(), "inbox", f); err != nil {
			t.Fatal(err)
		}
	}
	sync()
	if f.adds != 1 {
		t.Fatalf("adds=%d", f.adds)
	}
	if err = s.Revoke(a, false); err != nil {
		t.Fatal(err)
	}
	sync()
	if !f.present {
		t.Fatal("removed another pair's reference")
	}
	if err = s.Revoke(b, false); err != nil {
		t.Fatal(err)
	}
	sync()
	if f.present || f.removes != 1 {
		t.Fatalf("final removal=%+v", f)
	}
	if _, err = s.Allow(a, "next", true); err != nil {
		t.Fatal(err)
	}
	sync()
	if err = s.PermanentReceive("inbox", a.Address); err != nil {
		t.Fatal(err)
	}
	if err = s.Revoke(a, false); err != nil {
		t.Fatal(err)
	}
	sync()
	if !f.present {
		t.Fatal("removed permanent pair permission")
	}
}
func TestGuestPermissionsPreserveUnmanagedAndUncertain(t *testing.T) {
	for _, mode := range []string{"unmanaged", "lost-response", "changed-token"} {
		t.Run(mode, func(t *testing.T) {
			s, err := OpenGuestStore(filepath.Join(t.TempDir(), "auth.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			k := GuestKey{"p", "i", "g@example.test", "t"}
			if _, err = s.Allow(k, "m", true); err != nil {
				t.Fatal(err)
			}
			f := &guestPermissionFake{present: mode == "unmanaged", token: "preexisting", failAdd: mode == "lost-response"}
			err = s.Reconcile(context.Background(), "i", f)
			if (err != nil) != (mode == "lost-response") {
				t.Fatalf("sync=%v", err)
			}
			if mode == "changed-token" {
				f.token = "replaced-externally"
			}
			if err = s.Revoke(k, false); err != nil {
				t.Fatal(err)
			}
			if err = s.Reconcile(context.Background(), "i", f); err != nil {
				t.Fatal(err)
			}
			if !f.present || f.removes != 0 {
				t.Fatalf("removed uncertain entry: %+v", f)
			}
			g, err := s.Grant(k)
			if err != nil || g.Active {
				t.Fatalf("provider uncertainty broadened local grant: %+v %v", g, err)
			}
		})
	}
}

type blockingGuestRemoval struct {
	guestPermissionFake
	entered, release chan struct{}
}

func (f *blockingGuestRemoval) RemoveReceive(ctx context.Context, address, token string) error {
	close(f.entered)
	<-f.release
	return f.guestPermissionFake.RemoveReceive(ctx, address, token)
}
func TestGuestNewReferenceWaitsForInFlightRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	s, err := OpenGuestStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	other, err := OpenGuestStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	k := GuestKey{"p", "i", "g@example.test", "t"}
	if _, err = s.Allow(k, "m", true); err != nil {
		t.Fatal(err)
	}
	f := &blockingGuestRemoval{entered: make(chan struct{}), release: make(chan struct{})}
	if err = s.Reconcile(context.Background(), "i", f); err != nil {
		t.Fatal(err)
	}
	if err = s.Revoke(k, false); err != nil {
		t.Fatal(err)
	}
	removed := make(chan error, 1)
	go func() { removed <- s.Reconcile(context.Background(), "i", f) }()
	<-f.entered
	added := make(chan error, 1)
	go func() { _, err := other.Allow(k, "new", true); added <- err }()
	select {
	case err := <-added:
		close(f.release)
		<-removed
		t.Fatalf("new reference committed while its permission was being removed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(f.release)
	if err = <-removed; err != nil {
		t.Fatal(err)
	}
	if err = <-added; err != nil {
		t.Fatal(err)
	}
	if err = other.Reconcile(context.Background(), "i", f); err != nil {
		t.Fatal(err)
	}
	if !f.present {
		t.Fatal("new reference was not reconciled")
	}
}

type guestDirectionsFake struct {
	guestPermissionFake
	reply, send guestPermissionFake
}

func (f *guestDirectionsFake) GuestPermissionAuthorizers() map[string]ReceiveAuthorizer {
	return map[string]ReceiveAuthorizer{"receive": &f.guestPermissionFake, "reply": &f.reply, "send": &f.send}
}
func TestGuestPermissionsReconcileEveryDirectionIndependently(t *testing.T) {
	for _, preserve := range []bool{false, true} {
		s, err := OpenGuestStore(filepath.Join(t.TempDir(), "auth.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		k := GuestKey{"p", "i", "g@example.test", "t"}
		other := k
		other.ThreadID = "other"
		for _, key := range []GuestKey{k, other} {
			if _, err = s.Allow(key, "invite", true); err != nil {
				t.Fatal(err)
			}
		}
		f := &guestDirectionsFake{}
		f.reply.present = preserve
		f.reply.token = "preexisting"
		if err = s.Reconcile(context.Background(), "i", f); err != nil {
			t.Fatal(err)
		}
		if !f.present || !f.reply.present || !f.send.present {
			t.Fatal("grant did not establish receive, reply and send permissions")
		}
		if err = s.Revoke(k, false); err != nil {
			t.Fatal(err)
		}
		if err = s.Reconcile(context.Background(), "i", f); err != nil {
			t.Fatal(err)
		}
		if !f.send.present {
			t.Fatal("first revoke removed shared outbound permission")
		}
		if err = s.Revoke(other, false); err != nil {
			t.Fatal(err)
		}
		if err = s.Reconcile(context.Background(), "i", f); err != nil {
			t.Fatal(err)
		}
		if f.present || f.send.present || f.reply.present != preserve {
			t.Fatal("final revoke lost direction ownership isolation")
		}
	}
}

func TestGuestPermissionUpgradeRetainsOwnedReceiveRule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE receive_permissions(inbox_id TEXT NOT NULL,address TEXT NOT NULL,permanent INTEGER NOT NULL DEFAULT 0,pending INTEGER NOT NULL DEFAULT 1,owned INTEGER NOT NULL DEFAULT 0,token TEXT NOT NULL DEFAULT '',operation TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',PRIMARY KEY(inbox_id,address));
 INSERT INTO receive_permissions VALUES('i','g@example.test',0,0,1,'created','','');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := OpenGuestStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k := GuestKey{"p", "i", "g@example.test", "t"}
	if _, err = s.Allow(k, "invite", true); err != nil {
		t.Fatal(err)
	}
	f := &guestDirectionsFake{}
	f.present = true
	f.token = "created"
	if err = s.Reconcile(context.Background(), "i", f); err != nil {
		t.Fatal(err)
	}
	if f.adds != 0 || f.send.adds != 1 || f.reply.adds != 1 {
		t.Fatalf("upgrade operations: receive=%d reply=%d send=%d", f.adds, f.reply.adds, f.send.adds)
	}
	if err = s.Revoke(k, false); err != nil {
		t.Fatal(err)
	}
	if err = s.Reconcile(context.Background(), "i", f); err != nil {
		t.Fatal(err)
	}
	if f.removes != 1 || f.reply.removes != 1 || f.send.removes != 1 {
		t.Fatal("upgrade lost permission ownership")
	}
}

func TestGuestOutboundLostResponseDoesNotLoseReceiveOwnership(t *testing.T) {
	s, err := OpenGuestStore(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k := GuestKey{"p", "i", "g@example.test", "t"}
	if _, err = s.Allow(k, "invite", true); err != nil {
		t.Fatal(err)
	}
	f := &guestDirectionsFake{}
	f.send.failAdd = true
	if err = s.Reconcile(context.Background(), "i", f); err == nil {
		t.Fatal("lost outbound response reported success")
	}
	if err = s.Revoke(k, false); err != nil {
		t.Fatal(err)
	}
	if err = s.Reconcile(context.Background(), "i", f); err != nil {
		t.Fatal(err)
	}
	if f.present || f.reply.present || !f.send.present {
		t.Fatal("uncertain outbound ownership contaminated other directions")
	}
}
