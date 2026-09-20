package client

import (
	"path/filepath"
	"testing"
)

func TestGuestGrantReplayGenerationAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authorization Ω & #.db")
	s, err := OpenGuestStore(path)
	if err != nil {
		t.Fatal(err)
	}
	key := GuestKey{PairID: "pair", InboxID: "inbox", Address: "guest@example.test", ThreadID: "thread"}
	g, err := s.Allow(key, "invite-1", false)
	if err != nil || g.Generation != 1 || !g.Active {
		t.Fatalf("allow = %+v %v", g, err)
	}
	if err := s.BindWork(key, "held"); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(key, false); err != nil {
		t.Fatal(err)
	}
	g, err = s.Allow(key, "invite-1", false)
	if err != nil || g.Active {
		t.Fatalf("replayed invitation restored access: %+v %v", g, err)
	}
	g, err = s.Allow(key, "invite-2", false)
	if err != nil || g.Active {
		t.Fatalf("ordinary reply-all restored access: %+v %v", g, err)
	}
	g, err = s.Allow(key, "invite-2", true)
	if err != nil || !g.Active || g.Generation != 2 {
		t.Fatalf("fresh invite = %+v %v", g, err)
	}
	if err := s.CheckWork(key, "held"); err == nil {
		t.Fatal("stale held work survived reauthorization")
	}
	if err := s.BindWork(key, "held"); err == nil {
		t.Fatal("replayed work rebound to new generation")
	}
	s.Close()
	s, err = OpenGuestStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g, err = s.Grant(key)
	if err != nil || !g.Active || g.Generation != 2 {
		t.Fatalf("restart = %+v %v", g, err)
	}
	if err := s.Revoke(key, false); err != nil {
		t.Fatal(err)
	}
	g, err = s.Allow(key, "invite-1", true)
	if err != nil || !g.Active || g.Generation != 3 {
		t.Fatalf("explicit reauthorization = %+v %v", g, err)
	}
	if err := s.CheckWork(key, "held"); err == nil {
		t.Fatal("explicit reauthorization restored stale work")
	}
}

func TestGuestStoreScopeAndNoHistoricalMigration(t *testing.T) {
	s, err := OpenGuestStore(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := GuestKey{"p", "i", "g@example.test", "t"}
	g, err := s.Grant(key)
	if err != nil || g.Active {
		t.Fatalf("historical grant = %+v %v", g, err)
	}
	if _, err = s.Allow(key, "m", false); err != nil {
		t.Fatal(err)
	}
	for _, other := range []GuestKey{{"q", "i", key.Address, "t"}, {"p", "j", key.Address, "t"}, {"p", "i", "h@example.test", "t"}, {"p", "i", key.Address, "u"}} {
		g, err = s.Grant(other)
		if err != nil || g.Active {
			t.Fatalf("scope widened: %+v %v", g, err)
		}
	}
}
