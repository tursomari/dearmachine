package client

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestGuestKnownThreadDoesNotAuthorizeDeliveryOrHistory(t *testing.T) {
	inbox := Inbox{ID: testInboxUUID, Transport: "test", ProviderID: "provider", Address: "machine@example.test"}
	pair := Pair{ID: testPairAUUID, InboxID: inbox.ID, UserEmail: "a@example.test"}
	msg := Message{MessageID: "guest", ThreadID: "known", From: "guest@example.test", To: []string{inbox.Address, pair.UserEmail}, Delivery: MessageDelivery{InboxID: inbox.ProviderID}}
	raw := &routerTestTransport{messages: []Message{msg}}
	router, err := NewInboxRouter(raw, inbox, []Pair{pair}, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(filepath.Join(t.TempDir(), "pair.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, _, err = s.BeginMessage("owner", "known", TierPlain); err != nil {
		t.Fatal(err)
	}
	router.stores[pair.ID] = s
	endpoint, _ := router.Endpoint(pair.ID)
	batch, err := endpoint.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 0 {
		t.Fatal("known-thread-only rule admitted an uninvited guest")
	}
	if err = router.authorize(pair.ID, msg); err == nil {
		t.Fatal("history/receipt path admitted an uninvited guest")
	}
}

type guestAuthenticatedTransport struct {
	*fakeTransport
	sender string
}

func (t *guestAuthenticatedTransport) AuthenticateMessage(_ context.Context, m Message) error {
	if t.sender != m.From {
		return ErrMessageUnauthenticated
	}
	return nil
}

func TestGuestInvitationOuterVisibilityAndControllerAttribution(t *testing.T) {
	for _, mode := range []string{"to", "cc", "bcc-guest", "bcc-machine", "body-only", "forwarded-body", "guest-sender", "wrong-attribution", "other-controller", "wrong-inbox"} {
		t.Run(mode, func(t *testing.T) {
			inbox := Inbox{ID: testInboxUUID, Transport: "test", ProviderID: "provider", Address: "machine@example.test"}
			pair := Pair{ID: testPairAUUID, InboxID: inbox.ID, UserEmail: "a@example.test"}
			other := Pair{ID: testPairCUUID, InboxID: inbox.ID, UserEmail: "c@example.test"}
			raw := &guestAuthenticatedTransport{fakeTransport: newFakeTransport(), sender: pair.UserEmail}
			r, err := NewInboxRouter(raw, inbox, []Pair{pair}, time.Nanosecond)
			if err != nil {
				t.Fatal(err)
			}
			s, err := OpenGuestStore(filepath.Join(t.TempDir(), "auth.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err = r.ConfigureGuests(s, []Pair{pair, other}); err != nil {
				t.Fatal(err)
			}
			address := "guest@example.test"
			m := Message{MessageID: "invite", ThreadID: "thread", From: pair.UserEmail, To: []string{inbox.Address, address}, Delivery: MessageDelivery{InboxID: inbox.ProviderID}}
			switch mode {
			case "cc":
				m.To = []string{pair.UserEmail}
				m.CC = []string{inbox.Address, address}
			case "bcc-guest":
				m.To = []string{inbox.Address}
				m.BCC = []string{address}
			case "bcc-machine":
				m.To = []string{address}
				m.BCC = []string{inbox.Address}
			case "body-only", "forwarded-body":
				m.To = []string{inbox.Address}
				m.Body = "From: a@example.test\nTo: machine@example.test, guest@example.test"
			case "guest-sender":
				m.From = address
			case "wrong-attribution":
				raw.sender = "forger@example.test"
			case "other-controller":
				address = other.UserEmail
				m.To = []string{inbox.Address, address}
			case "wrong-inbox":
				m.Delivery.InboxID = "unrelated-provider"
			}
			m, err = r.authenticateMessage(context.Background(), m)
			if err == nil {
				_, err = r.allowInvitation(context.Background(), pair.ID, m, address, false)
			}
			want := mode == "to" || mode == "cc"
			if (err == nil) != want {
				t.Fatalf("invitation %s err=%v", mode, err)
			}
			grants, err := s.List(pair.ID)
			if err != nil {
				t.Fatal(err)
			}
			if (len(grants) == 1) != want {
				t.Fatalf("grants=%+v", grants)
			}
			if len(r.pairs) != 1 {
				t.Fatal("recipient detection modified pairs")
			}
		})
	}
}

func TestGuestRepliesRequireExactGrantAndVisibleReplyAllOnEveryPath(t *testing.T) {
	inbox := Inbox{ID: testInboxUUID, Transport: "test", ProviderID: "provider", Address: "machine@example.test"}
	pair := Pair{ID: testPairAUUID, InboxID: inbox.ID, UserEmail: "a@example.test"}
	raw := newFakeTransport()
	r, err := NewInboxRouter(raw, inbox, []Pair{pair}, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenGuestStore(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = r.ConfigureGuests(s, []Pair{pair}); err != nil {
		t.Fatal(err)
	}
	k := GuestKey{pair.ID, guestInboxKey(inbox), "guest@example.test", "thread"}
	if _, err = s.Allow(k, "invite", true); err != nil {
		t.Fatal(err)
	}
	base := Message{MessageID: "guest", ThreadID: k.ThreadID, From: k.Address, To: []string{inbox.Address}, CC: []string{pair.UserEmail}, Delivery: MessageDelivery{InboxID: inbox.ProviderID}, Attachments: []AttachmentRef{{AttachmentID: "attachment"}}}
	endpoint, _ := r.Endpoint(pair.ID)
	for _, mode := range []string{"good", "private-to-machine", "bcc-controller", "different-thread", "alias", "different-sender"} {
		t.Run(mode, func(t *testing.T) {
			m := base
			m.MessageID = mode
			switch mode {
			case "private-to-machine":
				m.CC = nil
			case "bcc-controller":
				m.CC = nil
				m.BCC = []string{pair.UserEmail}
			case "different-thread", "alias":
				m.ThreadID = mode
			case "different-sender":
				m.From = "other@example.test"
			}
			raw.setThread(m.ThreadID, []Message{m})
			raw.setPoll([]Message{m})
			r.lastPoll = time.Time{}
			batch, err := endpoint.Poll(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if (len(batch) == 1) != (mode == "good") {
				t.Fatalf("route %s=%+v", mode, batch)
			}
			_, err = endpoint.Message(context.Background(), m.MessageID)
			if (err == nil) != (mode == "good") {
				t.Fatalf("Message %s=%v", mode, err)
			}
			_, err = endpoint.Thread(context.Background(), m.ThreadID)
			if (err == nil) != (mode == "good") {
				t.Fatalf("Thread %s=%v", mode, err)
			}
			_, _, err = endpoint.ReplyReceipt(context.Background(), m, pair.UserEmail)
			if (err == nil) != (mode == "good") {
				t.Fatalf("Receipt %s=%v", mode, err)
			}
		})
	}
	if err = s.Revoke(k, false); err != nil {
		t.Fatal(err)
	}
	if err = endpoint.(*pairEndpoint).ensureMessage(context.Background(), "good"); err == nil {
		t.Fatal("cached receipt bypassed revocation")
	}
	if _, err = endpoint.FetchAttachment(context.Background(), "attachment", 100); err == nil {
		t.Fatal("cached attachment bypassed revocation")
	}
}

func TestGuestExplicitInvitationValidatesProviderMessageWithoutBackfill(t *testing.T) {
	rig, raw, r, pair, inbox := newParticipantTestRig(t)
	_ = rig
	m := Message{MessageID: "historical", ThreadID: "historical-thread", From: pair.UserEmail, To: []string{inbox.Address, "guest@example.test"}}
	raw.setThread(m.ThreadID, []Message{m})
	grants, _ := r.guests.List(pair.ID)
	if len(grants) != 0 {
		t.Fatal("migration backfilled grants")
	}
	if _, err := r.Invite(context.Background(), pair.ID, m.MessageID, "guest@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Invite(context.Background(), pair.ID, m.MessageID, "unmentioned@example.test"); err == nil {
		t.Fatal("CLI accepted arbitrary address")
	}
	normalized, _ := r.normalizeDelivery(m)
	if _, err := r.allowInvitation(context.Background(), pair.ID, normalized, "guest@example.test", false); err == nil {
		t.Fatalf("raw From treated as authentication: %v", err)
	}
}
