package transports

import (
	"context"
	"strings"
	"testing"

	"github.com/dearmachine/dearmachine/internal/client"
)

type factoryAuthorizingTransport struct {
	*allowlistFake
	email string
}

func (transport *factoryAuthorizingTransport) AuthorizePair(_ context.Context, email string) error {
	transport.email = email
	return nil
}

func TestFactoryConstructsAgentMail(t *testing.T) {
	t.Setenv("AGENTMAIL_API_KEY", "offline-test-key")
	allow, _ := ParseAllowList("agent@example.test")
	transport, err := New("agentmail", "inbox-test", allow)
	if err != nil {
		t.Fatalf("New(agentmail): %v", err)
	}
	if _, ok := transport.(*allowlistTransport); !ok {
		t.Fatalf("New(agentmail) type = %T", transport)
	}
}

func TestFactoryConstructsOpenMailOffline(t *testing.T) {
	t.Setenv("OPENMAIL_API_KEY", "offline-test-key")
	allow, _ := ParseAllowList("agent@example.test")
	transport, err := New("openmail", "inb_test", allow)
	if err != nil {
		t.Fatalf("New(openmail): %v", err)
	}
	if _, ok := transport.(*allowlistTransport); !ok {
		t.Fatalf("New(openmail) type = %T", transport)
	}
}

func TestFactoryConstructsSendmuxOffline(t *testing.T) {
	t.Setenv("SENDMUX_MAILBOX_API_KEY", "smx_mbx_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	allow, _ := ParseAllowList("agent@example.test")
	transport, err := New("sendmux", "mbx_test", allow)
	if err != nil {
		t.Fatalf("New(sendmux): %v", err)
	}
	if _, ok := transport.(*allowlistTransport); !ok {
		t.Fatalf("New(sendmux) type = %T", transport)
	}
}

func TestFactoryDistinguishesUnknownAndUnimplemented(t *testing.T) {
	allow, _ := ParseAllowList("agent@example.test")
	_, err := New("missing", "inbox-test", allow)
	if err == nil || !strings.Contains(err.Error(), `unknown transport "missing"`) {
		t.Fatalf("unknown error = %v", err)
	}

	build := constructors["openmail"]
	delete(constructors, "openmail")
	t.Cleanup(func() { constructors["openmail"] = build })
	_, err = New("openmail", "inbox-test", allow)
	if err == nil || !strings.Contains(err.Error(), `transport "openmail" is not implemented`) {
		t.Fatalf("unimplemented error = %v", err)
	}
}

func TestFactoryDelegatesPairAuthorizationToAdapter(t *testing.T) {
	fake := &factoryAuthorizingTransport{allowlistFake: &allowlistFake{
		messages: map[string]client.Message{}, calls: map[string]int{},
	}}
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
