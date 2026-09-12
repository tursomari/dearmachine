package transports

import (
	"context"
	"strings"
	"testing"

	"github.com/dearmachine/dearmachine/internal/client"
)

type factoryAuthorizingTransport struct {
	email string
}

func (*factoryAuthorizingTransport) Poll(context.Context) ([]client.Message, error) { return nil, nil }
func (*factoryAuthorizingTransport) Thread(context.Context, string) ([]client.Message, error) {
	return nil, nil
}
func (*factoryAuthorizingTransport) Message(context.Context, string) (client.Message, error) {
	return client.Message{}, nil
}
func (*factoryAuthorizingTransport) Reply(context.Context, string, client.ReplyPayload, string) (string, error) {
	return "", nil
}
func (*factoryAuthorizingTransport) ReplyReceipt(context.Context, client.Message, string) (string, bool, error) {
	return "", false, nil
}
func (*factoryAuthorizingTransport) MarkProcessed(context.Context, string) error { return nil }
func (*factoryAuthorizingTransport) FetchAttachment(context.Context, string, int64) ([]byte, error) {
	return nil, nil
}

func (transport *factoryAuthorizingTransport) AuthorizePair(_ context.Context, email string) error {
	transport.email = email
	return nil
}

func TestFactoryConstructsAgentMail(t *testing.T) {
	t.Setenv("AGENTMAIL_API_KEY", "offline-test-key")
	transport, err := NewRaw("agentmail", "inbox-test")
	if err != nil {
		t.Fatalf("New(agentmail): %v", err)
	}
	if transport == nil {
		t.Fatal("NewRaw(agentmail) returned nil")
	}
}

func TestFactoryConstructsOpenMailOffline(t *testing.T) {
	t.Setenv("OPENMAIL_API_KEY", "offline-test-key")
	transport, err := NewRaw("openmail", "inb_test")
	if err != nil {
		t.Fatalf("New(openmail): %v", err)
	}
	if transport == nil {
		t.Fatal("NewRaw(openmail) returned nil")
	}
}

func TestFactoryConstructsSendmuxOffline(t *testing.T) {
	t.Setenv("SENDMUX_MAILBOX_API_KEY", "smx_mbx_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	transport, err := NewRaw("sendmux", "mbx_test")
	if err != nil {
		t.Fatalf("New(sendmux): %v", err)
	}
	if transport == nil {
		t.Fatal("NewRaw(sendmux) returned nil")
	}
}

func TestFactoryDistinguishesUnknownAndUnimplemented(t *testing.T) {
	_, err := NewRaw("missing", "inbox-test")
	if err == nil || !strings.Contains(err.Error(), `unknown transport "missing"`) {
		t.Fatalf("unknown error = %v", err)
	}

	build := constructors["openmail"]
	delete(constructors, "openmail")
	t.Cleanup(func() { constructors["openmail"] = build })
	_, err = NewRaw("openmail", "inbox-test")
	if err == nil || !strings.Contains(err.Error(), `transport "openmail" is not implemented`) {
		t.Fatalf("unimplemented error = %v", err)
	}
}

func TestFactoryDelegatesPairAuthorizationToAdapter(t *testing.T) {
	fake := &factoryAuthorizingTransport{}
	build := constructors["agentmail"]
	constructors["agentmail"] = func(inboxID string) (client.Transport, error) {
		if inboxID != "provider-inbox" {
			t.Fatalf("inbox ID = %q", inboxID)
		}
		return fake, nil
	}
	t.Cleanup(func() { constructors["agentmail"] = build })

	if err := AuthorizePair(
		context.Background(), "agentmail", "provider-inbox", "pair@example.test",
	); err != nil {
		t.Fatal(err)
	}
	if fake.email != "pair@example.test" {
		t.Fatalf("authorized email = %q", fake.email)
	}
}
