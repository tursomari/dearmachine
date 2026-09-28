package client

import (
	"bytes"
	"context"
	"errors"
	"log"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestGuestAuthenticationExceptionRequiresIndependentApproval(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread")
	var logs bytes.Buffer
	router.logger = log.New(&logs, "", log.LstdFlags)
	process := func(m Message) {
		t.Helper()
		raw.setThread(m.ThreadID, append(raw.thread(m.ThreadID), m))
		raw.setPoll([]Message{m})
		router.lastPoll = time.Time{}
		mustProcess(t, rig)
	}
	guest := Message{MessageID: "unverified-1", ThreadID: "thread", From: "guest@example.test", To: []string{inbox.Address, pair.UserEmail}, CC: []string{"second@example.test"}, Body: "PRIVATE_PREVIEW", Labels: []string{"unauthenticated"}}
	process(guest)
	if rig.capture("count") != "1" || len(raw.sentReplies()) != 2 {
		t.Fatal("expected only private authentication warning")
	}
	warning := raw.sentReplies()[1]
	assertPrivateParticipantControlReply(t, warning, guest.MessageID, pair.UserEmail)
	command := regexp.MustCompile(`ALLOW UNVERIFIED [A-F0-9]{32}`).FindString(warning.Text)
	if command == "" || !strings.Contains(warning.Text, "impersonating") || !strings.Contains(warning.Text, "future messages") {
		t.Fatalf("warning missing scope/risk/command: %s", warning.Text)
	}
	if _, found, _ := rig.store.ParticipantRequestByMessage(guest.MessageID); found {
		t.Fatal("warning created a normal instruction approval prematurely")
	}
	process(guest)
	if len(raw.sentReplies()) != 2 {
		t.Fatal("duplicate warning")
	}
	owner := Message{MessageID: "accept", ThreadID: "thread", From: pair.UserEmail, To: []string{inbox.Address}, Body: strings.ToLower(command), InReplyTo: warning.ReceiptID}
	process(owner)
	// Held mail has been marked read. A subsequent poll must recover it from durable state.
	raw.setPoll(nil)
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	if len(raw.sentReplies()) != 3 || rig.capture("count") != "1" {
		t.Fatal("acceptance must release only an independent approval prompt")
	}
	approval := raw.sentReplies()[2]
	if !strings.Contains(approval.Text, "Yes — approve") {
		t.Fatalf("warning mistaken for approval: %s", approval.Text)
	}
	message, err := rig.app.transport.Message(context.Background(), guest.MessageID)
	if err != nil || message.authenticated || !message.riskAccepted {
		t.Fatalf("risk exception confused with identity: %+v %v", message, err)
	}
	owner.MessageID, owner.Body, owner.InReplyTo = "yes-1", "YES", approval.ReceiptID
	process(owner)
	if rig.capture("count") != "2" {
		t.Fatal("approved guest did not execute")
	}
	answer := approveGuestOutboundPreview(t, rig, raw, router, pair, inbox, guest, raw.sentReplies()[3])
	if !sameRecipientSet(answer.CC, []string{guest.From}) {
		t.Fatalf("unverified headers expanded recipients: %v", answer.CC)
	}
	guest.MessageID = "unverified-2"
	process(guest)
	if len(raw.sentReplies()) != 6 || rig.capture("count") != "2" || !strings.Contains(raw.sentReplies()[5].Text, "Yes — approve") {
		t.Fatal("future message needs approval, without repeated risk warning")
	}
	key := GuestKey{pair.ID, guestInboxKey(inbox), guest.From, guest.ThreadID}
	token, err := router.guests.RemovalToken(key)
	if err != nil {
		t.Fatal(err)
	}
	owner.MessageID, owner.Body = "remove", "REMOVE GUEST "+strings.ToUpper(token)
	process(owner)
	removedGuest := guest
	removedGuest.MessageID = "while-removed"
	before := len(raw.sentReplies())
	process(removedGuest)
	if len(raw.sentReplies()) != before || rig.capture("count") != "2" {
		t.Fatal("removed guest triggered a warning or work")
	}
	if _, err := router.guests.Allow(key, "reinvite", true); err != nil {
		t.Fatal(err)
	}
	owner.MessageID, owner.Body = "stale-accept", command
	process(owner)
	guest.MessageID = "unverified-3"
	process(guest)
	last := raw.sentReplies()[len(raw.sentReplies())-1]
	if !strings.Contains(last.Text, "impersonating") || strings.Contains(last.Text, command) {
		t.Fatal("reinvitation retained old exception/token")
	}
	if !strings.Contains(logs.String(), "notification=sent") || !strings.Contains(logs.String(), "authentication_exception=accepted") || !strings.Contains(logs.String(), strings.TrimPrefix(command, "ALLOW UNVERIFIED ")) || strings.Contains(logs.String(), "PRIVATE_PREVIEW") {
		t.Fatalf("missing/unsafe logging: %s", logs.String())
	}
}

func TestGuestAuthenticationExceptionRejectsWrongDecisions(t *testing.T) {
	for _, variant := range []string{"unauthenticated-owner", "guest", "wrong-thread", "wrong-pair", "wrong-token", "simple-yes", "wrong-inbox"} {
		t.Run(variant, func(t *testing.T) {
			rig, raw, r, p, inbox := newParticipantTestRig(t)
			establishParticipantThread(t, rig, raw, r, p, inbox, "thread")
			guest := Message{MessageID: "held", ThreadID: "thread", From: "guest@example.test", To: []string{p.UserEmail, inbox.Address}, Body: "Held", Labels: []string{"unauthenticated"}}
			raw.setThread("thread", append(raw.thread("thread"), guest))
			raw.setPoll([]Message{guest})
			r.lastPoll = time.Time{}
			mustProcess(t, rig)
			warning := raw.sentReplies()[1]
			command := regexp.MustCompile(`ALLOW UNVERIFIED [A-F0-9]{32}`).FindString(warning.Text)
			m := Message{MessageID: "decision", ThreadID: "thread", From: p.UserEmail, To: []string{inbox.Address}, Body: command, InReplyTo: warning.ReceiptID}
			switch variant {
			case "unauthenticated-owner":
				m.Labels = []string{"unauthenticated"}
			case "guest":
				m.From = guest.From
				m.To = append(m.To, p.UserEmail)
			case "wrong-thread":
				m.ThreadID = "another-thread"
			case "wrong-pair":
				m.From = "another-owner@example.test"
				other := Pair{ID: "22222222-2222-4222-8222-222222222222", UserEmail: m.From, InboxID: inbox.ID}
				r.pairs[other.ID] = other
				r.controllers[other.UserEmail] = true
				r.known[other.ID] = map[string]struct{}{}

			case "wrong-token":
				m.Body = "ALLOW UNVERIFIED 00000000000000000000000000000000"
			case "simple-yes":
				m.Body = "Yes"
			case "wrong-inbox":
				m.Delivery.InboxID = "different-provider-inbox"
			}
			raw.setThread(m.ThreadID, append(raw.thread(m.ThreadID), m))
			raw.setPoll([]Message{m})
			r.lastPoll = time.Time{}
			err := rig.app.ProcessOnce(context.Background())
			if variant == "wrong-inbox" {
				if err == nil {
					t.Fatal("wrong inbox accepted")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			g, _ := r.guests.Grant(GuestKey{p.ID, guestInboxKey(inbox), guest.From, "thread"})
			accepted, err := r.guests.authenticationAccepted(g)
			if err != nil || accepted || rig.capture("count") != "1" {
				t.Fatalf("bad decision granted risk or ran: %v %v", accepted, err)
			}
		})
	}
}

func TestGuestAuthenticationExceptionRestartRateLimitAndTampering(t *testing.T) {
	rig, raw, r, p, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, r, p, inbox, "thread")
	var logs bytes.Buffer
	r.logger = log.New(&logs, "", 0)
	send := func(id, address, thread string) Message {
		t.Helper()
		m := Message{MessageID: id, ThreadID: thread, From: address, To: []string{inbox.Address, p.UserEmail}, Body: "PRIVATE_BODY", Labels: []string{"unauthenticated"}}
		raw.setThread(thread, append(raw.thread(thread), m))
		raw.setPoll([]Message{m})
		r.lastPoll = time.Time{}
		mustProcess(t, rig)
		return m
	}
	first := send("held-1", "guest@example.test", "thread")
	send("held-2", "first@example.test", "thread")
	send("held-3", "second@example.test", "thread")
	fourth := send("held-4", "first-guest@example.test", "thread")
	if len(raw.sentReplies()) != 4 || !strings.Contains(logs.String(), "notification=rate_limited") {
		t.Fatal("owner warning flood was not limited")
	}
	// New message identity from the same guest cannot cause another warning.
	send("held-5", "guest@example.test", "thread")
	if len(raw.sentReplies()) != 4 {
		t.Fatal("notice repeated within grant")
	}
	path := r.guests.path
	if err := r.guests.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenGuestStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	r.guests = reopened
	raw.setPoll(nil)
	r.lastPoll = time.Time{}
	r.lastAuthRecovery = time.Time{}
	mustProcess(t, rig)
	if len(raw.sentReplies()) != 4 {
		t.Fatal("restart lost deduplication/rate limit")
	}
	// Age the persisted window; a delayed warning must become deliverable.
	if _, err := reopened.db.Exec(`UPDATE guest_auth_exceptions SET attempted_at=1 WHERE attempted_at>0`); err != nil {
		t.Fatal(err)
	}
	raw.setPoll(nil)
	r.lastPoll = time.Time{}
	r.lastAuthRecovery = time.Time{}
	mustProcess(t, rig)
	if len(raw.sentReplies()) != 5 || raw.sentReplies()[4].MessageID != fourth.MessageID {
		t.Fatal("rate-limited warning was lost")
	}
	warning := raw.sentReplies()[1]
	command := regexp.MustCompile(`ALLOW UNVERIFIED [A-F0-9]{32}`).FindString(warning.Text)
	owner := Message{MessageID: "accept", ThreadID: "thread", From: p.UserEmail, To: []string{inbox.Address}, Body: command}
	raw.setThread("thread", append(raw.thread("thread"), owner))
	raw.setPoll([]Message{owner})
	r.lastPoll = time.Time{}
	mustProcess(t, rig)
	// Frozen provider IDs cannot acquire a new body after the warning/decision.
	first.Body = "SUBSTITUTED_BODY"
	raw.setThread("thread", append(raw.thread("thread"), first))
	raw.setPoll(nil)
	r.lastPoll = time.Time{}
	mustProcess(t, rig)
	if _, found, _ := rig.store.ParticipantRequestByMessage(first.MessageID); found {
		t.Fatal("mutated held content became approvable")
	}
	if _, err := rig.app.transport.Message(context.Background(), first.MessageID); err == nil {
		t.Fatal("direct lookup accepted mutated content")
	}
	if rig.capture("count") != "1" || strings.Contains(logs.String(), "PRIVATE_BODY") || strings.Contains(logs.String(), "SUBSTITUTED_BODY") {
		t.Fatal("unapproved content executed or leaked to logs")
	}
}

type uncertainAuthenticationTransport struct {
	*fakeTransport
	attempts int
}

func (f *uncertainAuthenticationTransport) Reply(ctx context.Context, id string, payload ReplyPayload, key string) (string, error) {
	f.attempts++
	_, err := f.fakeTransport.Reply(ctx, id, payload, key)
	if err != nil {
		return "", err
	}
	return "", errors.New("lost response SECRET_PROVIDER_BODY")
}

func TestGuestAuthenticationWarningLostResponseRecovery(t *testing.T) {
	rig, raw, r, p, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, r, p, inbox, "thread")
	uncertain := &uncertainAuthenticationTransport{fakeTransport: raw}
	r.raw = uncertain
	var logs bytes.Buffer
	r.logger = log.New(&logs, "", 0)
	m := Message{MessageID: "lost", ThreadID: "thread", From: "guest@example.test", To: []string{inbox.Address, p.UserEmail}, Body: "secret-preview", Labels: []string{"unauthenticated"}}
	raw.setThread("thread", append(raw.thread("thread"), m))
	raw.setPoll([]Message{m})
	r.lastPoll = time.Time{}
	mustProcess(t, rig)
	if uncertain.attempts != 1 || !strings.Contains(logs.String(), "failed_delivery_uncertain") {
		t.Fatal("missing uncertain send record")
	}
	// Receipt visibility can lag: no fresh send while delivery is uncertain.
	raw.errors.Thread = errors.New("provider temporarily unavailable SECRET")
	raw.setPoll(nil)
	r.lastPoll = time.Time{}
	r.lastAuthRecovery = time.Time{}
	mustProcess(t, rig)
	if uncertain.attempts != 1 {
		t.Fatal("blindly retried uncertain send")
	}
	raw.errors.Thread = nil
	r.lastPoll = time.Time{}
	r.lastAuthRecovery = time.Time{}
	mustProcess(t, rig)
	if uncertain.attempts != 1 || !strings.Contains(logs.String(), "sent_recovered") || strings.Contains(logs.String(), "SECRET") || strings.Contains(logs.String(), "secret-preview") {
		t.Fatalf("unsafe receipt recovery: %s", logs.String())
	}
}

func TestGuestAuthenticationLogsEscapeUntrustedMetadata(t *testing.T) {
	_, _, r, _, _ := newParticipantTestRig(t)
	var logs bytes.Buffer
	r.logger = log.New(&logs, "", 0)
	r.logAuthentication(Message{MessageID: "id\nforged-event", ThreadID: "thread\r\n", From: "guest\n@example.test", Body: "secret"}, Pair{}, "no_eligible_owner")
	if strings.Count(logs.String(), "\n") != 1 || strings.Contains(logs.String(), "secret") {
		t.Fatalf("log injection or body leak: %q", logs.String())
	}
}

func TestGuestUnverifiedBindingRechecksExceptionGenerationAtomically(t *testing.T) {
	_, raw, r, p, inbox := newParticipantTestRig(t)
	k := GuestKey{p.ID, guestInboxKey(inbox), "guest@example.test", "thread"}
	g, err := r.guests.Allow(k, "invite", true)
	if err != nil {
		t.Fatal(err)
	}
	m := Message{MessageID: "held", ThreadID: k.ThreadID, From: k.Address, To: []string{p.UserEmail, inbox.Address}, Labels: []string{"unauthenticated"}}
	m, err = r.normalizeDelivery(m)
	if err != nil {
		t.Fatal(err)
	}
	raw.setThread(k.ThreadID, []Message{m})
	if err := r.holdUnauthenticated(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if _, err := r.guests.db.Exec(`UPDATE guest_auth_exceptions SET accepted=1`); err != nil {
		t.Fatal(err)
	}
	accepted, err := r.guests.authenticationAccepted(g)
	if err != nil || !accepted {
		t.Fatal("fixture acceptance missing")
	}
	// Simulate revocation/reinvitation after lookup but before work binding.
	if err := r.guests.Revoke(k, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.guests.Allow(k, "reinvite", true); err != nil {
		t.Fatal(err)
	}
	if err := r.guests.bindUnverifiedWork(k, "new-message", "fingerprint"); !errors.Is(err, ErrGuestUnauthorized) {
		t.Fatalf("old exception crossed atomic generation boundary: %v", err)
	}
}

func TestGuestWarningYesCannotApproveEarlierInstruction(t *testing.T) {
	rig, raw, r, p, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, r, p, inbox, "thread")
	process := func(m Message) {
		t.Helper()
		raw.setThread(m.ThreadID, append(raw.thread(m.ThreadID), m))
		raw.setPoll([]Message{m})
		r.lastPoll = time.Time{}
		mustProcess(t, rig)
	}
	guest := Message{MessageID: "verified", ThreadID: "thread", From: "guest@example.test", To: []string{inbox.Address, p.UserEmail}, Body: "Earlier request"}
	process(guest)
	approval := raw.sentReplies()[1]
	guest.MessageID, guest.Labels = "unverified", []string{"unauthenticated"}
	process(guest)
	warning := raw.sentReplies()[2]
	process(Message{MessageID: "ambiguous-yes", ThreadID: "thread", From: p.UserEmail, To: []string{inbox.Address}, Body: "Yes", InReplyTo: warning.ReceiptID, References: []string{approval.ReceiptID}})
	request, found, err := rig.store.ParticipantRequestByMessage("verified")
	if err != nil || !found || request.State != participantAwaitingDecision || rig.capture("count") != "1" {
		t.Fatalf("warning reply approved prior work: %+v %v", request, err)
	}
}
