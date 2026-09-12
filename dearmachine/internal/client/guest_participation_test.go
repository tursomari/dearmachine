package client

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type participationPermissions map[string]string

func (p participationPermissions) InspectReceive(_ context.Context, address string) (ReceivePermission, error) {
	token, present := p[address]
	return ReceivePermission{present, token}, nil
}
func (p participationPermissions) AddReceive(_ context.Context, address string) (ReceivePermission, error) {
	p[address] = "owned-" + address
	return ReceivePermission{true, p[address]}, nil
}
func (p participationPermissions) RemoveReceive(_ context.Context, address, token string) error {
	if p[address] != token {
		return errors.New("permission changed")
	}
	delete(p, address)
	return nil
}

type participationTransport struct {
	*fakeTransport
	participationPermissions
	reply, send participationPermissions
}

func (p *participationTransport) GuestPermissionAuthorizers() map[string]ReceiveAuthorizer {
	return map[string]ReceiveAuthorizer{"receive": p.participationPermissions, "reply": p.reply, "send": p.send}
}

func TestGuestAutomaticParticipationApprovalPrivacyAndRemoval(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	provider := &participationTransport{raw, participationPermissions{}, participationPermissions{}, participationPermissions{}}
	router.raw = provider
	ctx := context.Background()
	process := func(m Message) {
		t.Helper()
		raw.setThread(m.ThreadID, append(raw.thread(m.ThreadID), m))
		raw.setPoll([]Message{m})
		router.lastPoll = time.Time{}
		mustProcess(t, rig)
	}
	owner := Message{MessageID: "invite", ThreadID: "shared", From: pair.UserEmail, To: []string{inbox.Address}, CC: []string{"guest@example.test"}, Body: "Work with my guest", Timestamp: time.Now().UTC()}
	process(owner)
	key := GuestKey{pair.ID, guestInboxKey(inbox), "guest@example.test", owner.ThreadID}
	g, err := router.guests.Grant(key)
	if err != nil || !g.Active || g.Generation != 1 {
		t.Fatalf("visible owner invitation did not grant participation: %+v %v", g, err)
	}
	for direction, auth := range provider.GuestPermissionAuthorizers() {
		permission, err := auth.InspectReceive(ctx, key.Address)
		if err != nil || !permission.Present {
			t.Fatalf("automatic %s synchronization: %+v %v", direction, permission, err)
		}
	}
	shared := raw.sentReplies()[0]
	if !equalFoldSlice(shared.CC, []string{key.Address}) || strings.Contains(shared.Text, "REMOVE GUEST") {
		t.Fatalf("shared answer or private footer isolation: %+v", shared)
	}
	guest := Message{MessageID: "guest-1", ThreadID: owner.ThreadID, From: key.Address, To: []string{pair.UserEmail}, CC: []string{inbox.Address}, Body: "First guest request", Timestamp: time.Now().UTC()}
	process(guest)
	prompt := raw.sentReplies()[1]
	assertPrivateParticipantControlReply(t, prompt, guest.MessageID, pair.UserEmail)
	request, _, _ := rig.store.ParticipantRequestByMessage(guest.MessageID)
	if request.Kind != participantRequestInstruction || request.State != participantAwaitingDecision || rig.capture("count") != "1" {
		t.Fatalf("guest was admitted separately or ran without approval: %+v", request)
	}
	command := regexp.MustCompile(`REMOVE GUEST [A-F0-9]{32}`).FindString(prompt.Text)
	if command == "" {
		t.Fatal("private approval lacks a removal command")
	}
	private := owner
	private.MessageID, private.CC, private.Body = "owner-private", nil, "Private continuation"
	process(private)
	answer := raw.sentReplies()[2]
	if len(answer.CC) != 0 || !strings.Contains(answer.Text, command) {
		t.Fatal("private continuation recipient/footer mismatch")
	}
	yes := private
	yes.MessageID, yes.Body, yes.InReplyTo = "approve-1", "Yes", prompt.ReceiptID
	process(yes)
	answer = raw.sentReplies()[3]
	if rig.capture("count") != "3" || !equalFoldSlice(answer.To, []string{pair.UserEmail}) || !equalFoldSlice(answer.CC, []string{key.Address}) || strings.Contains(answer.Text, command) {
		t.Fatal("approved guest answer did not preserve its original shared recipients")
	}
	guest.MessageID, guest.Body = "guest-2", "Second guest request"
	process(guest)
	second := raw.sentReplies()[4]
	assertPrivateParticipantControlReply(t, second, guest.MessageID, pair.UserEmail)
	process(yes) // Replaying approval #1 cannot release request #2.
	if rig.capture("count") != "3" {
		t.Fatal("an approval applied to another guest message")
	}
	remove := private
	remove.MessageID, remove.Body, remove.InReplyTo = "remove", command, second.ReceiptID
	process(remove)
	g, _ = router.guests.Grant(key)
	if g.Active {
		t.Fatal("authenticated removal did not revoke the grant")
	}
	for direction, auth := range provider.GuestPermissionAuthorizers() {
		guestRule, _ := auth.InspectReceive(ctx, key.Address)
		ownerRule, _ := auth.InspectReceive(ctx, pair.UserEmail)
		if guestRule.Present || !ownerRule.Present {
			t.Fatalf("%s removal did not preserve the paired owner", direction)
		}
	}
	yes.MessageID, yes.InReplyTo = "stale-approval", second.ReceiptID
	process(yes)
	if rig.capture("count") != "3" {
		t.Fatal("approval survived revocation")
	}
	owner.MessageID, owner.Body = "accidental-reply-all", "Continue our work"
	process(owner)
	g, _ = router.guests.Grant(key)
	if g.Active || len(raw.sentReplies()[len(raw.sentReplies())-1].CC) != 0 {
		t.Fatal("ordinary reply-all resurrected a revoked grant")
	}
	if _, err := router.Invite(ctx, pair.ID, owner.MessageID, key.Address); err != nil {
		t.Fatal(err)
	}
	remove.MessageID = "old-command-new-message"
	process(remove)
	g, _ = router.guests.Grant(key)
	if !g.Active || g.Generation != 2 {
		t.Fatal("old removal token revoked a new grant generation")
	}
}

func TestGuestUnauthenticatedMessagesNeverReachTheParticipantFlow(t *testing.T) {
	for _, role := range []string{"owner", "guest", "approval", "removal", "unsupported"} {
		t.Run(role, func(t *testing.T) {
			rig, raw, router, pair, inbox := newParticipantTestRig(t)
			establishParticipantThread(t, rig, raw, router, pair, inbox, "thread")
			message := Message{MessageID: "forged", ThreadID: "thread", From: pair.UserEmail, To: []string{inbox.Address}, CC: []string{"new-guest@example.test"}, Body: "Run forged work", Labels: []string{"unauthenticated"}}
			if role == "guest" || role == "approval" {
				message.From = "guest@example.test"
				message.To = []string{inbox.Address, pair.UserEmail}
			}
			if role == "approval" {
				legitimate := message
				legitimate.MessageID, legitimate.Labels = "held", nil
				raw.setThread("thread", append(raw.thread("thread"), legitimate))
				raw.setPoll([]Message{legitimate})
				router.lastPoll = time.Time{}
				mustProcess(t, rig)
				message.From, message.Body = pair.UserEmail, "Yes"
				message.InReplyTo = raw.sentReplies()[1].ReceiptID
			}
			if role == "removal" {
				token, err := router.guests.RemovalToken(GuestKey{pair.ID, guestInboxKey(inbox), "guest@example.test", "thread"})
				if err != nil {
					t.Fatal(err)
				}
				message.Body = "REMOVE GUEST " + token
			}
			if role == "unsupported" {
				message.Labels = nil
				router.raw = WithTransportRetries(struct{ Transport }{raw})
			}
			before := len(raw.sentReplies())
			raw.setThread("thread", append(raw.thread("thread"), message))
			raw.setPoll([]Message{message})
			router.lastPoll = time.Time{}
			err := rig.app.ProcessOnce(context.Background())
			if role == "unsupported" {
				if !errors.Is(err, ErrSenderAttributionUnsupported) {
					t.Fatalf("unsupported adapter did not fail closed: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if rig.capture("count") != "1" || len(raw.sentReplies()) != before {
				t.Fatal("unauthenticated sender reached agent or control mail")
			}
			endpoint, _ := router.Endpoint(pair.ID)
			if _, err := endpoint.Message(context.Background(), message.MessageID); err == nil {
				t.Fatal("direct retrieval bypassed authentication")
			}
			if role != "unsupported" {
				history, err := endpoint.Thread(context.Background(), "thread")
				if err != nil {
					t.Fatal(err)
				}
				for _, m := range history {
					if m.MessageID == message.MessageID {
						t.Fatal("thread retrieval exposed the rejected message")
					}
				}
			}
			g, _ := router.guests.Grant(GuestKey{pair.ID, guestInboxKey(inbox), "guest@example.test", "thread"})
			newGuest, _ := router.guests.Grant(GuestKey{pair.ID, guestInboxKey(inbox), "new-guest@example.test", "thread"})
			if !g.Active || newGuest.Active {
				t.Fatal("unauthenticated message changed participation")
			}
		})
	}
}

func TestGuestRemovalTokensAreScopedAndDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authorization.db")
	s, err := OpenGuestStore(path)
	if err != nil {
		t.Fatal(err)
	}
	k := GuestKey{"pair", "inbox", "guest@example.test", "thread"}
	if _, err := s.Allow(k, "invitation", false); err != nil {
		t.Fatal(err)
	}
	token, err := s.RemovalToken(k)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenGuestStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, scope := range [][3]string{{"other", k.InboxID, k.ThreadID}, {k.PairID, "other", k.ThreadID}, {k.PairID, k.InboxID, "other"}} {
		if err := s.revokeToken(scope[0], scope[1], scope[2], fmt.Sprint(i), token); !errors.Is(err, ErrGuestUnauthorized) {
			t.Fatalf("cross-scope revocation: %v", err)
		}
	}
	if err := s.revokeToken(k.PairID, k.InboxID, k.ThreadID, "command", token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Allow(k, "new-invitation", true); err != nil {
		t.Fatal(err)
	}
	if err := s.revokeToken(k.PairID, k.InboxID, k.ThreadID, "command", token); err != nil {
		t.Fatal(err)
	}
	g, _ := s.Grant(k)
	if !g.Active || g.Generation != 2 {
		t.Fatal("recovered old command revoked the later generation")
	}
}

func TestGuestApprovalCannotExecuteChangedProviderContent(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread")
	guest := Message{MessageID: "held", ThreadID: "thread", From: "guest@example.test", To: []string{inbox.Address, pair.UserEmail}, Body: "Original request"}
	raw.setThread("thread", append(raw.thread("thread"), guest))
	raw.setPoll([]Message{guest})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	prompt := raw.sentReplies()[1]
	guest.Body = "Substituted request"
	owner := Message{MessageID: "yes", ThreadID: "thread", From: pair.UserEmail, To: []string{inbox.Address}, Body: "Yes", InReplyTo: prompt.ReceiptID}
	raw.setThread("thread", []Message{guest, owner})
	raw.setPoll([]Message{owner})
	router.lastPoll = time.Time{}
	if err := rig.app.ProcessOnce(context.Background()); err == nil {
		t.Fatal("changed approved message was accepted")
	}
	if rig.capture("count") != "1" {
		t.Fatal("changed guest content reached the agent")
	}
}

func TestGuestUnverifiableRecoveryDoesNotBlockOwnerWork(t *testing.T) {
	for _, mode := range []string{"pre-upgrade", "failed-authentication", "changed-content"} {
		t.Run(mode, func(t *testing.T) {
			rig, raw, router, pair, inbox := newParticipantTestRig(t)
			establishParticipantThread(t, rig, raw, router, pair, inbox, "thread")
			guest := Message{MessageID: "held", ThreadID: "thread", From: "guest@example.test", To: []string{inbox.Address, pair.UserEmail}, Body: "Original"}
			key := GuestKey{pair.ID, guestInboxKey(inbox), guest.From, guest.ThreadID}
			fingerprint := ""
			if mode != "pre-upgrade" {
				fingerprint = messageFingerprint(guest)
			}
			if err := router.guests.bindWork(key, guest.MessageID, fingerprint); err != nil {
				t.Fatal(err)
			}
			if _, _, err := rig.store.BeginParticipantRequest(guest, pair.UserEmail); err != nil {
				t.Fatal(err)
			}
			if mode == "failed-authentication" {
				guest.Labels = []string{"unauthenticated"}
			}
			if mode == "changed-content" {
				guest.Body = "Substituted"
			}
			owner := Message{MessageID: "owner-new", ThreadID: "thread", From: pair.UserEmail, To: []string{inbox.Address}, Body: "Continue privately"}
			raw.setThread("thread", append(raw.thread("thread"), guest, owner))
			raw.setPoll([]Message{owner})
			router.lastPoll = time.Time{}
			mustProcess(t, rig)
			if rig.capture("count") != "2" || len(raw.sentReplies()) != 2 {
				t.Fatal("unverifiable held request blocked owner work or created a prompt")
			}
		})
	}
}
