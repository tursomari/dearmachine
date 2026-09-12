package client

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestGuestSubprocessStartRechecksRevocation(t *testing.T) {
	s, err := OpenGuestStore(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k := GuestKey{"p", "i", "g@example.test", "t"}
	if _, err = s.Allow(k, "invite", true); err != nil {
		t.Fatal(err)
	}
	if err = s.BindWork(k, "queued"); err != nil {
		t.Fatal(err)
	}
	guard := guestStartGuard(func(start func() error) error { return s.WithWorkStart(k, "queued", start) })
	ctx := context.WithValue(context.Background(), guestStartContextKey{}, guard)
	if err = s.Revoke(k, false); err != nil {
		t.Fatal(err)
	}
	launched := false
	r := &AgentRunner{invoke: func(*exec.Cmd) error { launched = true; return nil }}
	err = r.runActiveCommand(ctx, "t", exec.Command("unused"), nil)
	if !errors.Is(err, ErrGuestUnauthorized) || launched {
		t.Fatalf("revoked work launched=%v err=%v", launched, err)
	}
}

func TestGuestReauthorizationDoesNotReleaseOldApproval(t *testing.T) {
	rig, raw, r, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, r, pair, inbox, "thread")
	seedAdmittedParticipants(t, rig.store, "guest@example.test")
	guest := Message{MessageID: "held", ThreadID: "thread", From: "guest@example.test", To: []string{inbox.Address, pair.UserEmail}, Body: "HELD_BODY", Timestamp: time.Now().UTC()}
	raw.setThread("thread", append(raw.thread("thread"), guest))
	raw.setPoll([]Message{guest})
	r.lastPoll = time.Time{}
	mustProcess(t, rig)
	prompt := raw.sentReplies()[1]
	key, _ := guestKey(pair, inbox, guest)
	if err := r.guests.Revoke(key, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.guests.Allow(key, "new-invitation", true); err != nil {
		t.Fatal(err)
	}
	yes := Message{MessageID: "old-yes", ThreadID: "thread", From: pair.UserEmail, To: []string{inbox.Address}, Body: "Yes", InReplyTo: prompt.ReceiptID, Timestamp: guest.Timestamp.Add(time.Minute)}
	raw.setThread("thread", append(raw.thread("thread"), yes))
	raw.setPoll([]Message{yes})
	r.lastPoll = time.Time{}
	mustProcess(t, rig)
	if rig.capture("count") != "1" || len(raw.sentReplies()) != 2 {
		t.Fatalf("stale approval released work or prompt: count=%s replies=%d", rig.capture("count"), len(raw.sentReplies()))
	}
}

func TestGuestRevocationBlocksQueuedTrustedAndRecoveredWork(t *testing.T) {
	rig, raw, r, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, r, pair, inbox, "thread")
	seedAdmittedParticipants(t, rig.store, "guest@example.test")
	if _, err := rig.store.SetParticipantTrust("guest@example.test", true); err != nil {
		t.Fatal(err)
	}
	guest := Message{MessageID: "queued", ThreadID: "thread", From: "guest@example.test", To: []string{inbox.Address, pair.UserEmail}, Body: "QUEUED_BODY", Timestamp: time.Now().UTC()}
	raw.setThread("thread", append(raw.thread("thread"), guest))
	raw.setPoll([]Message{guest})
	r.lastPoll = time.Time{}
	queue := newThreadWorkQueue()
	if err := rig.app.pollAndClaim(context.Background(), queue); err != nil {
		t.Fatal(err)
	}
	key, _ := guestKey(pair, inbox, guest)
	if err := r.guests.Revoke(key, false); err != nil {
		t.Fatal(err)
	}
	if err := rig.app.dispatch(context.Background(), queue); err != nil {
		t.Fatal(err)
	}
	if _, err := r.guests.Allow(key, "next", true); err != nil {
		t.Fatal(err)
	}
	raw.setPoll(nil)
	r.lastPoll = time.Time{}
	mustProcess(t, rig)
	if rig.capture("count") != "1" {
		t.Fatal("queued or recovered guest ran after revocation")
	}
	admitted, trusted, err := rig.store.ParticipantStatus(key.Address)
	if err != nil || !admitted || !trusted {
		t.Fatal("grant revocation erased retained admission/trust")
	}
}
