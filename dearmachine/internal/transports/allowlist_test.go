package transports

import (
	"context"
	"errors"
	"testing"

	"github.com/dearmachine/dearmachine/internal/client"
)

type allowlistFake struct {
	messages map[string]client.Message
	poll     []client.Message
	calls    map[string]int
}

func (fake *allowlistFake) count(name string) { fake.calls[name]++ }
func (fake *allowlistFake) Poll(context.Context) ([]client.Message, error) {
	fake.count("poll")
	return fake.poll, nil
}
func (fake *allowlistFake) Thread(context.Context, string) ([]client.Message, error) {
	fake.count("thread")
	return fake.poll, nil
}
func (fake *allowlistFake) Message(_ context.Context, id string) (client.Message, error) {
	fake.count("message")
	message, ok := fake.messages[id]
	if !ok {
		return client.Message{}, errors.New("missing")
	}
	return message, nil
}
func (fake *allowlistFake) Reply(context.Context, string, client.ReplyPayload, string) (string, error) {
	fake.count("reply")
	return "receipt", nil
}
func (fake *allowlistFake) ReplyReceipt(context.Context, client.Message) (string, bool, error) {
	fake.count("receipt")
	return "receipt", true, nil
}
func (fake *allowlistFake) MarkProcessed(context.Context, string) error {
	fake.count("mark")
	return nil
}
func (fake *allowlistFake) FetchAttachment(context.Context, string, int64) ([]byte, error) {
	fake.count("attachment")
	return []byte("ok"), nil
}

func TestAllowlistPredicateAndSeamsFailClosed(t *testing.T) {
	allow, err := ParseAllowList("Pair <PAIR@example.test>, device@example.test")
	if err != nil {
		t.Fatal(err)
	}
	good := client.Message{MessageID: "good", From: "PAIR@example.test", To: []string{"DEVICE@example.test"}, Attachments: []client.AttachmentRef{{AttachmentID: "known"}}}
	fake := &allowlistFake{messages: map[string]client.Message{
		"good":     good,
		"bad-from": {MessageID: "bad-from", From: "stranger@example.test", To: []string{"device@example.test"}},
		"empty-to": {MessageID: "empty-to", From: "pair@example.test"},
		"bad-to":   {MessageID: "bad-to", From: "pair@example.test", To: []string{"device@example.test", "stranger@example.test"}},
	}, poll: []client.Message{good, {MessageID: "bad-from", From: "stranger@example.test", To: []string{"device@example.test"}}, {MessageID: "empty-to", From: "pair@example.test"}}, calls: map[string]int{}}
	transport, err := newAllowlistTransport(fake, allow)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := transport.Poll(context.Background())
	if err != nil || len(messages) != 1 || messages[0].MessageID != "good" {
		t.Fatalf("Poll = %+v, %v", messages, err)
	}
	for _, id := range []string{"bad-from", "empty-to", "bad-to"} {
		if _, err := transport.Message(context.Background(), id); err == nil {
			t.Fatalf("Message(%s) succeeded", id)
		}
		if _, err := transport.Reply(context.Background(), id, client.ReplyPayload{}, "key"); err == nil {
			t.Fatalf("Reply(%s) succeeded", id)
		}
		if err := transport.MarkProcessed(context.Background(), id); err == nil {
			t.Fatalf("MarkProcessed(%s) succeeded", id)
		}
		if _, _, err := transport.ReplyReceipt(context.Background(), fake.messages[id]); err == nil {
			t.Fatalf("ReplyReceipt(%s) succeeded", id)
		}
	}
	if fake.calls["reply"] != 0 || fake.calls["mark"] != 0 || fake.calls["receipt"] != 0 {
		t.Fatalf("unauthorized call mutated inner transport: %+v", fake.calls)
	}
	if _, err := transport.FetchAttachment(context.Background(), "unknown", 1); err == nil || fake.calls["attachment"] != 0 {
		t.Fatalf("unknown attachment = %v, calls=%+v", err, fake.calls)
	}
	if contents, err := transport.FetchAttachment(context.Background(), "known", 1); err != nil || string(contents) != "ok" {
		t.Fatalf("known attachment = %q, %v", contents, err)
	}
}

func TestAllowlistRejectsEmpty(t *testing.T) {
	if _, err := ParseAllowList(""); err == nil {
		t.Fatal("ParseAllowList accepted empty list")
	}
	if _, err := newAllowlistTransport(&allowlistFake{}, AllowList{}); err == nil {
		t.Fatal("newAllowlistTransport accepted empty list")
	}
}

func TestAllowlistCanonicalizesMessageAddresses(t *testing.T) {
	allow, err := ParseAllowList("pair@example.test, device@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		message client.Message
	}{
		{
			name:    "named from",
			message: client.Message{From: "Contact Name <pair@example.test>", To: []string{"device@example.test"}},
		},
		{
			name:    "named recipient",
			message: client.Message{From: "pair@example.test", To: []string{"Other Name <device@example.test>"}},
		},
		{
			name:    "named from with case differences",
			message: client.Message{From: "Pair <PAIR@Example.Test>", To: []string{"device@example.test"}},
		},
		{
			name:    "bare addr specs",
			message: client.Message{From: "pair@example.test", To: []string{"device@example.test"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !allow.authorized(tt.message) {
				t.Fatal("authorized returned false")
			}
		})
	}
}

func TestAllowlistMalformedOrUnknownMessageAddressesFailClosed(t *testing.T) {
	allow, err := ParseAllowList("pair@example.test, device@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		message client.Message
	}{
		{
			name:    "from missing addr spec",
			message: client.Message{From: "No Address Here", To: []string{"device@example.test"}},
		},
		{
			name:    "unknown from addr spec",
			message: client.Message{From: "stranger@example.test", To: []string{"device@example.test"}},
		},
		{
			name:    "empty from",
			message: client.Message{To: []string{"device@example.test"}},
		},
		{
			name:    "no recipients",
			message: client.Message{From: "pair@example.test"},
		},
		{
			name:    "empty recipient",
			message: client.Message{From: "pair@example.test", To: []string{""}},
		},
		{
			name:    "recipient fails to parse",
			message: client.Message{From: "pair@example.test", To: []string{"Not An Address"}},
		},
		{
			name:    "recipient contains multiple addresses",
			message: client.Message{From: "pair@example.test", To: []string{"device@example.test, pair@example.test"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if allow.authorized(tt.message) {
				t.Fatal("authorized returned true")
			}
		})
	}
}
