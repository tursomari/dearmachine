package transports

import (
	"strings"
	"testing"

	"github.com/dearmachine/dearmachine/internal/client"
)

func TestFactoryConstructsAgentMail(t *testing.T) {
	t.Setenv("AGENTMAIL_API_KEY", "offline-test-key")
	transport, err := New("agentmail", "inbox-test")
	if err != nil {
		t.Fatalf("New(agentmail): %v", err)
	}
	if _, ok := transport.(*client.Mailbox); !ok {
		t.Fatalf("New(agentmail) type = %T", transport)
	}
}

func TestFactoryConstructsOpenMailOffline(t *testing.T) {
	t.Setenv("OPENMAIL_API_KEY", "offline-test-key")
	t.Setenv("DEARMACHINE_OPENMAIL_ALLOWED_FROM", "sender@example.com")
	t.Setenv("DEARMACHINE_OPENMAIL_ALLOWED_TO", "sender@example.com")
	transport, err := New("openmail", "inb_test")
	if err != nil {
		t.Fatalf("New(openmail): %v", err)
	}
	if _, ok := transport.(*client.OpenMailTransport); !ok {
		t.Fatalf("New(openmail) type = %T", transport)
	}
}

func TestFactoryConstructsSendmuxOffline(t *testing.T) {
	t.Setenv("SENDMUX_MAILBOX_API_KEY", "smx_mbx_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	t.Setenv("DEARMACHINE_SENDMUX_ALLOWED_FROM", "sender@example.com")
	t.Setenv("DEARMACHINE_SENDMUX_ALLOWED_TO", "sender@example.com")
	transport, err := New("sendmux", "mbx_test")
	if err != nil {
		t.Fatalf("New(sendmux): %v", err)
	}
	if _, ok := transport.(*client.SendmuxTransport); !ok {
		t.Fatalf("New(sendmux) type = %T", transport)
	}
}

func TestFactoryDistinguishesUnknownAndUnimplemented(t *testing.T) {
	_, err := New("missing", "inbox-test")
	if err == nil || !strings.Contains(err.Error(), `unknown transport "missing"`) {
		t.Fatalf("unknown error = %v", err)
	}

	build := constructors["openmail"]
	delete(constructors, "openmail")
	t.Cleanup(func() { constructors["openmail"] = build })
	_, err = New("openmail", "inbox-test")
	if err == nil || !strings.Contains(err.Error(), `transport "openmail" is not implemented`) {
		t.Fatalf("unimplemented error = %v", err)
	}
}
