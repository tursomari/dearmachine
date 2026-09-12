package client

import (
	"context"
	"errors"
	"os"
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

func TestGuestHistoricalInvitationStartsOnlyApprovedNewWork(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	invitation := Message{MessageID: "historical-invite", ThreadID: "historical-thread", From: pair.UserEmail, To: []string{inbox.Address, "guest@example.test"}, Body: "OLD_OWNER_INSTRUCTION", Timestamp: time.Now().UTC().Add(-time.Hour)}
	raw.setThread(invitation.ThreadID, []Message{invitation})
	if _, err := router.Invite(context.Background(), pair.ID, invitation.MessageID, "guest@example.test"); err != nil {
		t.Fatal(err)
	}
	seedAdmittedParticipants(t, rig.store, "guest@example.test")
	guest := Message{MessageID: "new-guest-work", ThreadID: invitation.ThreadID, From: "guest@example.test", To: []string{inbox.Address, pair.UserEmail}, Body: "NEW_GUEST_INSTRUCTION", Timestamp: time.Now().UTC()}
	raw.setThread(guest.ThreadID, []Message{invitation, guest})
	raw.setPoll([]Message{guest})
	mustProcess(t, rig)
	if _, err := os.Stat(filepath.Join(rig.captureDir, "count")); !os.IsNotExist(err) {
		t.Fatal("historical authorization executed work without approval")
	}
	approval := raw.sentReplies()[0]
	yes := Message{MessageID: "historical-yes", ThreadID: guest.ThreadID, From: pair.UserEmail, To: []string{inbox.Address}, Body: "Yes", InReplyTo: approval.ReceiptID, Timestamp: guest.Timestamp.Add(time.Minute)}
	raw.setThread(guest.ThreadID, []Message{invitation, guest, yes})
	raw.setPoll([]Message{yes})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	if rig.capture("count") != "1" {
		t.Fatal("approved new work did not start exactly once")
	}
	if pending, found, err := rig.store.PendingByID(invitation.MessageID); err != nil || found {
		t.Fatalf("historical invitation became work: %+v %v %v", pending, found, err)
	}
	result := raw.sentReplies()[1]
	if !equalFoldSlice(result.To, []string{pair.UserEmail}) || len(result.CC) != 0 || len(result.BCC) != 0 {
		t.Fatal("guest result escaped the private controller route")
	}
}
