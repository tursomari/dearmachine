package deviceclient

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	agentmail "github.com/agentmail-to/agentmail-go"
	"github.com/agentmail-to/agentmail-go/option"
)

const fakeInboxPrefix = "/v0/inboxes/test-inbox/"

func TestMailboxAPIErrorContracts(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		path      string
		prepare   func(*fakeAgentMail)
		operation func(context.Context, *Mailbox) error
		want      string
	}{
		{
			name:   "list",
			method: http.MethodGet,
			path:   fakeInboxPrefix + "messages",
			operation: func(ctx context.Context, mailbox *Mailbox) error {
				_, err := mailbox.Poll(ctx)
				return err
			},
			want: "list AgentMail messages",
		},
		{
			name:   "poll get",
			method: http.MethodGet,
			path:   fakeInboxPrefix + "messages/message-1",
			prepare: func(fake *fakeAgentMail) {
				fake.add(testMessage("message-1", "thread-1", "body"))
			},
			operation: func(ctx context.Context, mailbox *Mailbox) error {
				_, err := mailbox.Poll(ctx)
				return err
			},
			want: "get AgentMail message message-1",
		},
		{
			name:   "message",
			method: http.MethodGet,
			path:   fakeInboxPrefix + "messages/message-1",
			operation: func(ctx context.Context, mailbox *Mailbox) error {
				_, err := mailbox.Message(ctx, "message-1")
				return err
			},
			want: "get AgentMail message message-1",
		},
		{
			name:   "thread",
			method: http.MethodGet,
			path:   fakeInboxPrefix + "threads/thread-1",
			operation: func(ctx context.Context, mailbox *Mailbox) error {
				_, err := mailbox.Thread(ctx, "thread-1")
				return err
			},
			want: "get AgentMail thread thread-1",
		},
		{
			name:   "reply",
			method: http.MethodPost,
			path:   fakeInboxPrefix + "messages/message-1/reply",
			prepare: func(fake *fakeAgentMail) {
				fake.add(testMessage("message-1", "thread-1", "body"))
			},
			operation: func(ctx context.Context, mailbox *Mailbox) error {
				_, err := mailbox.Reply(ctx, "message-1", "answer", "key-1")
				return err
			},
			want: "reply to AgentMail message message-1",
		},
		{
			name:   "update",
			method: http.MethodPatch,
			path:   fakeInboxPrefix + "messages/message-1",
			operation: func(ctx context.Context, mailbox *Mailbox) error {
				return mailbox.MarkProcessed(ctx, "message-1")
			},
			want: "mark AgentMail message message-1 processed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake, mailbox := newMailboxTestPair(t)
			if test.prepare != nil {
				test.prepare(fake)
			}
			fake.fail(test.method, test.path, fakeHTTPResponse{
				status: http.StatusServiceUnavailable,
				body:   `{"error":{"message":"injected outage"}}`,
			})
			err := test.operation(context.Background(), mailbox)
			if err == nil || !strings.Contains(err.Error(), test.want) ||
				!strings.Contains(err.Error(), "503") {
				t.Fatalf("operation error = %v, want %q and status 503", err, test.want)
			}
		})
	}
}

func TestMailboxMalformedResponseContracts(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		path      string
		operation func(context.Context, *Mailbox) error
		want      string
	}{
		{
			name:   "list",
			method: http.MethodGet,
			path:   fakeInboxPrefix + "messages",
			operation: func(ctx context.Context, mailbox *Mailbox) error {
				_, err := mailbox.Poll(ctx)
				return err
			},
			want: "list AgentMail messages",
		},
		{
			name:   "message",
			method: http.MethodGet,
			path:   fakeInboxPrefix + "messages/message-1",
			operation: func(ctx context.Context, mailbox *Mailbox) error {
				_, err := mailbox.Message(ctx, "message-1")
				return err
			},
			want: "get AgentMail message message-1",
		},
		{
			name:   "thread",
			method: http.MethodGet,
			path:   fakeInboxPrefix + "threads/thread-1",
			operation: func(ctx context.Context, mailbox *Mailbox) error {
				_, err := mailbox.Thread(ctx, "thread-1")
				return err
			},
			want: "get AgentMail thread thread-1",
		},
		{
			name:   "reply",
			method: http.MethodPost,
			path:   fakeInboxPrefix + "messages/message-1/reply",
			operation: func(ctx context.Context, mailbox *Mailbox) error {
				_, err := mailbox.Reply(ctx, "message-1", "answer", "key-1")
				return err
			},
			want: "reply to AgentMail message message-1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake, mailbox := newMailboxTestPair(t)
			fake.fail(test.method, test.path, fakeHTTPResponse{
				status: http.StatusOK,
				body:   `{`,
			})
			err := test.operation(context.Background(), mailbox)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("operation error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestMailboxTimeoutContract(t *testing.T) {
	fake, mailbox := newMailboxTestPair(t)
	fake.fail(http.MethodGet, fakeInboxPrefix+"messages", fakeHTTPResponse{
		status: http.StatusOK,
		body:   `{"count":0,"messages":[],"limit":100}`,
		delay:  50 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	_, err := mailbox.Poll(ctx)
	if err == nil || !strings.Contains(err.Error(), "list AgentMail messages") ||
		!strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("Poll timeout error = %v", err)
	}
}

func TestMailboxReplyRequiresReceiptMessageID(t *testing.T) {
	fake, mailbox := newMailboxTestPair(t)
	fake.fail(http.MethodPost, fakeInboxPrefix+"messages/message-1/reply", fakeHTTPResponse{
		status: http.StatusOK,
		body:   `{"thread_id":"thread-1"}`,
	})

	_, err := mailbox.Reply(context.Background(), "message-1", "answer", "key-1")
	if err == nil || !strings.Contains(err.Error(), "returned no receipt") {
		t.Fatalf("Reply missing receipt error = %v", err)
	}
}

func TestMessageBodyPrefersExtractedText(t *testing.T) {
	tests := []struct {
		name    string
		message agentmail.Message
		want    string
	}{
		{
			name: "extracted text",
			message: agentmail.Message{
				ExtractedText: "new reply",
				Text:          "new reply\n\n> quoted history",
				Preview:       "preview",
			},
			want: "new reply",
		},
		{
			name:    "plain text fallback",
			message: agentmail.Message{Text: "plain body", Preview: "preview"},
			want:    "plain body",
		},
		{
			name:    "preview fallback",
			message: agentmail.Message{Preview: "preview"},
			want:    "preview",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := messageBody(test.message); got != test.want {
				t.Fatalf("messageBody() = %q, want %q", got, test.want)
			}
		})
	}
}

func newMailboxTestPair(t *testing.T) (*fakeAgentMail, *Mailbox) {
	t.Helper()
	fake := newFakeAgentMail(t)
	t.Cleanup(fake.server.Close)
	client := agentmail.NewClient(
		option.WithBaseURL(fake.server.URL+"/"),
		option.WithAPIKey("test-key"),
		option.WithMaxRetries(0),
	)
	mailbox, err := NewMailbox(client, "test-inbox")
	if err != nil {
		t.Fatalf("NewMailbox: %v", err)
	}
	return fake, mailbox
}
