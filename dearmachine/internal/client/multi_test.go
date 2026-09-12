package client

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	testInboxUUID = "10000000-0000-4000-8000-000000000001"
	testPairAUUID = "20000000-0000-4000-8000-000000000001"
	testPairCUUID = "20000000-0000-4000-8000-000000000003"
)

func TestInboxRouterPollsSharedInboxOnceAndRoutesExactly(t *testing.T) {
	raw := &routerTestTransport{messages: []Message{
		{MessageID: "a", ThreadID: "thread-a", From: "A Person <a@example.test>", To: []string{"Machine <machine@example.test>"}},
		{MessageID: "c", ThreadID: "thread-c", From: "c@example.test", To: []string{"machine@example.test"}},
	}}
	inbox := Inbox{ID: testInboxUUID, Transport: "test", ProviderID: "provider", Address: "machine@example.test"}
	pairs := []Pair{
		{ID: testPairAUUID, UserEmail: "a@example.test", InboxID: inbox.ID},
		{ID: testPairCUUID, UserEmail: "c@example.test", InboxID: inbox.ID},
	}
	router, err := NewInboxRouter(raw, inbox, pairs, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := router.Endpoint(testPairAUUID)
	c, _ := router.Endpoint(testPairCUUID)
	aMessages, err := a.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cMessages, err := c.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if raw.polls != 1 || len(aMessages) != 1 || aMessages[0].MessageID != "a" || len(cMessages) != 1 || cMessages[0].MessageID != "c" {
		t.Fatalf("polls/routes = %d/%+v/%+v", raw.polls, aMessages, cMessages)
	}
}

func TestInboxRouterRejectsUnknownAndAmbiguousRoutesBeforeDelivery(t *testing.T) {
	inbox := Inbox{ID: testInboxUUID, Transport: "test", ProviderID: "provider", Address: "machine@example.test"}
	duplicate := []Pair{
		{ID: testPairAUUID, UserEmail: "same@example.test", InboxID: inbox.ID},
		{ID: testPairCUUID, UserEmail: "same@example.test", InboxID: inbox.ID},
	}
	if _, err := NewInboxRouter(&routerTestTransport{}, inbox, duplicate, time.Second); err == nil || !strings.Contains(err.Error(), "ambiguous inbox route") {
		t.Fatalf("duplicate route error = %v", err)
	}
	raw := &routerTestTransport{messages: []Message{{MessageID: "unknown", From: "other@example.test", To: []string{inbox.Address}}}}
	router, err := NewInboxRouter(raw, inbox, duplicate[:1], time.Second)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := router.Endpoint(testPairAUUID)
	messages, err := endpoint.Poll(context.Background())
	if err != nil || len(messages) != 0 {
		t.Fatalf("unknown route result = %+v, %v", messages, err)
	}
	if len(router.pending) != 0 {
		t.Fatalf("unknown message was delivered: %+v", router.pending)
	}
}

func TestInboxRouterRoutesByTrustedDeliveryInsteadOfMergedHeaders(t *testing.T) {
	inbox := Inbox{ID: testInboxUUID, Transport: "test", ProviderID: "provider", Address: "machine@example.test"}
	pair := Pair{ID: testPairAUUID, UserEmail: "a@example.test", InboxID: inbox.ID}
	raw := &routerTestTransport{messages: []Message{
		{
			MessageID: "cc", ThreadID: "thread-cc", From: pair.UserEmail,
			To: []string{"guest@example.test"}, CC: []string{inbox.Address},
		},
		{
			MessageID: "blind", ThreadID: "thread-blind", From: pair.UserEmail,
			Delivery: MessageDelivery{InboxID: inbox.ProviderID, ReadState: MessageReadStateUnread},
		},
	}}
	router, err := NewInboxRouter(raw, inbox, []Pair{pair}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := router.Endpoint(pair.ID)
	messages, err := endpoint.Poll(context.Background())
	if err != nil || !slices.Equal(messageIDs(messages), []string{"cc", "blind"}) {
		t.Fatalf("Poll = %+v, %v", messages, err)
	}
	if messages[0].Delivery.Role != DeliveryRoleCC ||
		messages[0].Delivery.Recipient != inbox.Address ||
		messages[1].Delivery.Role != DeliveryRoleUnknown {
		t.Fatalf("deliveries = %+v, %+v", messages[0].Delivery, messages[1].Delivery)
	}
}

func TestInboxRouterRejectsMismatchedProviderDelivery(t *testing.T) {
	inbox := Inbox{ID: testInboxUUID, Transport: "test", ProviderID: "provider", Address: "machine@example.test"}
	pair := Pair{ID: testPairAUUID, UserEmail: "a@example.test", InboxID: inbox.ID}
	raw := &routerTestTransport{messages: []Message{{
		MessageID: "wrong-inbox", From: pair.UserEmail,
		Delivery: MessageDelivery{InboxID: "different-provider-inbox"},
	}}}
	router, err := NewInboxRouter(raw, inbox, []Pair{pair}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := router.Endpoint(pair.ID)
	if _, err := endpoint.Poll(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "different-provider-inbox") {
		t.Fatalf("mismatched delivery error = %v", err)
	}
}

func TestInboxRouterReauthorizesMessageAndAttachmentOperations(t *testing.T) {
	inbox := Inbox{ID: testInboxUUID, Transport: "test", ProviderID: "provider", Address: "machine@example.test"}
	pair := Pair{ID: testPairAUUID, UserEmail: "a@example.test", InboxID: inbox.ID}
	raw := &routerTestTransport{byID: map[string]Message{
		"a": {MessageID: "a", From: pair.UserEmail, To: []string{inbox.Address}, Attachments: []AttachmentRef{{AttachmentID: "attachment-a"}}},
		"c": {MessageID: "c", From: "c@example.test", To: []string{inbox.Address}},
	}}
	router, err := NewInboxRouter(raw, inbox, []Pair{pair}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := router.Endpoint(pair.ID)
	if _, err := endpoint.Message(context.Background(), "c"); err == nil {
		t.Fatal("cross-pair message was authorized")
	}
	if _, err := endpoint.Message(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := endpoint.FetchAttachment(context.Background(), "attachment-a", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := endpoint.FetchAttachment(context.Background(), "attachment-c", 10); err == nil {
		t.Fatal("unknown attachment was authorized")
	}
}

func TestMultiDaemonUsesOneSharedWorkerGate(t *testing.T) {
	apps := []*App{{}, {}, {}}
	if _, err := NewMultiDaemon(apps, 2, ""); err != nil {
		t.Fatal(err)
	}
	if apps[0].workerGate == nil || apps[0].workerGate != apps[1].workerGate || apps[1].workerGate != apps[2].workerGate || cap(apps[0].workerGate) != 2 {
		t.Fatal("pair apps do not share one bounded worker gate")
	}
}

func TestDaemonLockIsExclusiveAndRecoversStaleOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "dearmachine.pid")
	unlock, err := CreateDaemonLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateDaemonLock(path); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second lock error = %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("999999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unlock, err = CreateDaemonLock(path)
	if err != nil {
		t.Fatalf("recover stale lock: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
}

type routerTestTransport struct {
	messages []Message
	byID     map[string]Message
	polls    int
}

func (transport *routerTestTransport) AuthenticateMessage(context.Context, Message) error {
	return nil // Trusted fixture assertion; production adapters must prove it.
}

func (transport *routerTestTransport) Poll(context.Context) ([]Message, error) {
	transport.polls++
	return append([]Message(nil), transport.messages...), nil
}
func (transport *routerTestTransport) Thread(context.Context, string) ([]Message, error) {
	return nil, nil
}
func (transport *routerTestTransport) Message(_ context.Context, id string) (Message, error) {
	return transport.byID[id], nil
}
func (transport *routerTestTransport) Reply(context.Context, string, ReplyPayload, string) (string, error) {
	return "reply", nil
}
func (transport *routerTestTransport) ReplyReceipt(context.Context, Message, string) (string, bool, error) {
	return "", false, nil
}
func (transport *routerTestTransport) MarkProcessed(context.Context, string) error { return nil }
func (transport *routerTestTransport) FetchAttachment(context.Context, string, int64) ([]byte, error) {
	return []byte("ok"), nil
}
