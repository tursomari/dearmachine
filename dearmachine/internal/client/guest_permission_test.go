package client

import (
	"context"
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
