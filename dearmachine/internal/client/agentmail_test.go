package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentmail "github.com/agentmail-to/agentmail-go"
	"github.com/agentmail-to/agentmail-go/option"
)

const fakeInboxPrefix = "/v0/inboxes/test-inbox/"

func TestNewAgentMailTransportLoadsCredentialFile(t *testing.T) {
	credentialPath := filepath.Join(t.TempDir(), "agentmail-api-key")
	if err := os.WriteFile(credentialPath, []byte("test-key-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTMAIL_API_KEY", "")
	t.Setenv("AGENTMAIL_API_KEY_FILE", credentialPath)
	if _, err := NewAgentMailTransport("test-inbox"); err != nil {
		t.Fatalf("NewAgentMailTransport: %v", err)
	}
}

func TestNewAgentMailTransportRejectsMissingOrInvalidCredential(t *testing.T) {
	t.Setenv("AGENTMAIL_API_KEY", "")
	t.Setenv("AGENTMAIL_API_KEY_FILE", "")
	if _, err := NewAgentMailTransport("test-inbox"); err == nil || !strings.Contains(err.Error(), "AGENTMAIL_API_KEY or AGENTMAIL_API_KEY_FILE is required") {
		t.Fatalf("missing credential = %v", err)
	}

	for _, test := range []struct {
		name, contents, want string
	}{
		{name: "empty", contents: "\n", want: "AGENTMAIL_API_KEY_FILE is empty"},
		{name: "multiple lines", contents: "first\nsecond\n", want: "must contain exactly one line"},
	} {
		t.Run(test.name, func(t *testing.T) {
			credentialPath := filepath.Join(t.TempDir(), "agentmail-api-key")
			if err := os.WriteFile(credentialPath, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENTMAIL_API_KEY_FILE", credentialPath)
			if _, err := NewAgentMailTransport("test-inbox"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("credential error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestMailboxAuthorizePairEnsuresReceiveReplyAndSendAllowEntries(t *testing.T) {
	allowed := make(map[string]bool)
	posts := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		direction := ""
		for _, candidate := range []string{"receive", "reply", "send"} {
			prefix := "/v0/inboxes/test-inbox/lists/" + candidate + "/allow"
			if request.URL.Path == prefix || strings.HasPrefix(request.URL.Path, prefix+"/") {
				direction = candidate
				break
			}
		}
		if direction == "" {
			http.NotFound(writer, request)
			return
		}
		response := map[string]any{
			"created_at": "2026-08-31T00:00:00Z", "direction": direction,
			"entry": "pair@example.test", "entry_type": "email", "list_type": "allow",
			"organization_id": "org-test", "pod_id": "pod-test", "inbox_id": "test-inbox",
		}
		switch request.Method {
		case http.MethodGet:
			if !allowed[direction] {
				writer.WriteHeader(http.StatusNotFound)
				_, _ = writer.Write([]byte(`{"error":{"message":"missing"}}`))
				return
			}
		case http.MethodPost:
			var body struct {
				Entry string `json:"entry"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Entry != "pair@example.test" {
				t.Errorf("authorize %s body = %+v, %v", direction, body, err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			allowed[direction] = true
			posts[direction]++
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(response); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	api := agentmail.NewClient(
		option.WithBaseURL(server.URL+"/"), option.WithAPIKey("test-key"), option.WithMaxRetries(0),
	)
	mailbox, err := NewMailbox(api, "test-inbox")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := mailbox.AuthorizePair(context.Background(), " Pair <PAIR@Example.test> "); err != nil {
			t.Fatalf("AuthorizePair attempt %d: %v", attempt+1, err)
		}
	}
	if posts["receive"] != 1 || posts["reply"] != 1 || posts["send"] != 1 || len(posts) != 3 {
		t.Fatalf("authorization posts = %+v", posts)
	}
}

func TestMailboxAuthorizePairSurfacesPolicyFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(`{"error":{"message":"missing"}}`))
			return
		}
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"error":{"message":"injected outage"}}`))
	}))
	t.Cleanup(server.Close)
	api := agentmail.NewClient(
		option.WithBaseURL(server.URL+"/"), option.WithAPIKey("test-key"), option.WithMaxRetries(0),
	)
	mailbox, err := NewMailbox(api, "test-inbox")
	if err != nil {
		t.Fatal(err)
	}
	err = mailbox.AuthorizePair(context.Background(), "pair@example.test")
	if err == nil || !strings.Contains(err.Error(), "authorize AgentMail receive") ||
		!strings.Contains(err.Error(), "503") {
		t.Fatalf("AuthorizePair error = %v", err)
	}
}

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
				_, err := mailbox.Reply(
					ctx,
					"message-1",
					ReplyPayload{Text: "answer"},
					"key-1",
				)
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
				_, err := mailbox.Reply(
					ctx,
					"message-1",
					ReplyPayload{Text: "answer"},
					"key-1",
				)
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

func TestMailboxNormalizeFindsQuotedFooterOutsideExtractedText(t *testing.T) {
	reference := newConversationReference()
	mailbox := &Mailbox{}
	normalized := mailbox.normalize(agentmail.Message{
		MessageID:     "message-1",
		ThreadID:      "provider-thread-b",
		From:          "user@example.com",
		ExtractedText: "Continue with the next section.",
		Text: "Continue with the next section.\n\n> Earlier answer.\n> " +
			strings.ReplaceAll(conversationFooter(reference), "\n", "\n> "),
	})
	if normalized.Body != "Continue with the next section." {
		t.Fatalf("normalized body = %q", normalized.Body)
	}
	if len(normalized.ConversationReferences) != 1 ||
		normalized.ConversationReferences[0] != reference {
		t.Fatalf("normalized references = %v, want [%s]", normalized.ConversationReferences, reference)
	}
}

func TestMailboxNormalizeStripsReferencedQuoteWithoutExtractedText(t *testing.T) {
	reference := newConversationReference()
	mailbox := &Mailbox{}
	normalized := mailbox.normalize(agentmail.Message{
		MessageID: "message-1",
		ThreadID:  "provider-thread-b",
		From:      "user@example.com",
		Text: "Continue with the next section.\n\n> Earlier answer.\n> " +
			strings.ReplaceAll(conversationFooter(reference), "\n", "\n> "),
	})
	if normalized.Body != "Continue with the next section." {
		t.Fatalf("normalized body = %q, want only the new contribution", normalized.Body)
	}
	if len(normalized.ConversationReferences) != 1 ||
		normalized.ConversationReferences[0] != reference {
		t.Fatalf("normalized references = %v, want [%s]", normalized.ConversationReferences, reference)
	}
}

func TestMailboxReplyRequiresReceiptMessageID(t *testing.T) {
	fake, mailbox := newMailboxTestPair(t)
	fake.fail(http.MethodPost, fakeInboxPrefix+"messages/message-1/reply", fakeHTTPResponse{
		status: http.StatusOK,
		body:   `{"thread_id":"thread-1"}`,
	})

	_, err := mailbox.Reply(
		context.Background(),
		"message-1",
		ReplyPayload{Text: "answer"},
		"key-1",
	)
	if err == nil || !strings.Contains(err.Error(), "returned no receipt") {
		t.Fatalf("Reply missing receipt error = %v", err)
	}
}

func TestMailboxReplyMapsPayloadText(t *testing.T) {
	fake, mailbox := newMailboxTestPair(t)
	fake.add(testMessage("message-1", "thread-1", "body"))

	receiptID, err := mailbox.Reply(
		context.Background(),
		"message-1",
		ReplyPayload{Text: "answer"},
		"key-1",
	)
	if err != nil || receiptID != "reply-message-1" {
		t.Fatalf("Reply = %q, %v", receiptID, err)
	}
	replies := fake.sentReplies()
	if len(replies) != 1 || replies[0].Text != "answer" {
		t.Fatalf("replies = %+v", replies)
	}
}

func TestMailboxReplyMapsPayloadHTML(t *testing.T) {
	body := captureMailboxReplyBody(t, ReplyPayload{
		Text: "plain answer",
		HTML: "<p>rich answer</p>",
	})

	var text, html string
	if err := json.Unmarshal(body["text"], &text); err != nil {
		t.Fatalf("decode text: %v", err)
	}
	if err := json.Unmarshal(body["html"], &html); err != nil {
		t.Fatalf("decode html: %v", err)
	}
	if text != "plain answer" || html != "<p>rich answer</p>" {
		t.Fatalf("reply body text = %q, html = %q", text, html)
	}
}

func TestMailboxReplyMapsPayloadFiles(t *testing.T) {
	files := []OutboundFile{
		{
			Filename:    "report.txt",
			ContentType: "text/plain",
			Contents:    []byte("report contents"),
		},
		{
			Filename:    "chart.png",
			ContentType: "image/png",
			Contents:    []byte{0x89, 0x50, 0x4e, 0x47},
		},
	}
	body := captureMailboxReplyBody(t, ReplyPayload{Text: "answer", Files: files})

	var attachments []struct {
		Filename           string `json:"filename"`
		ContentType        string `json:"content_type"`
		Content            string `json:"content"`
		ContentDisposition string `json:"content_disposition"`
	}
	if err := json.Unmarshal(body["attachments"], &attachments); err != nil {
		t.Fatalf("decode attachments: %v", err)
	}
	if len(attachments) != len(files) {
		t.Fatalf("attachments count = %d, want %d", len(attachments), len(files))
	}
	for index, file := range files {
		attachment := attachments[index]
		if attachment.Filename != file.Filename ||
			attachment.ContentType != file.ContentType ||
			attachment.Content != base64.StdEncoding.EncodeToString(file.Contents) ||
			attachment.ContentDisposition != "attachment" {
			t.Errorf("attachment %d = %+v, want file %+v with attachment disposition", index, attachment, file)
		}
	}
}

func TestMailboxReplyOmitsEmptyHTMLAndFiles(t *testing.T) {
	body := captureMailboxReplyBody(t, ReplyPayload{Text: "plain answer"})

	if _, ok := body["html"]; ok {
		t.Errorf("reply body unexpectedly contains html: %s", body["html"])
	}
	if _, ok := body["attachments"]; ok {
		t.Errorf("reply body unexpectedly contains attachments: %s", body["attachments"])
	}
}

func captureMailboxReplyBody(t *testing.T, payload ReplyPayload) map[string]json.RawMessage {
	t.Helper()
	var body map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost ||
			request.URL.Path != "/v0/inboxes/test-inbox/messages/message-1/reply" {
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode reply request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(map[string]string{
			"message_id": "reply-message-1",
			"thread_id":  "thread-1",
		}); err != nil {
			t.Errorf("encode reply response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	client := agentmail.NewClient(
		option.WithBaseURL(server.URL+"/"),
		option.WithAPIKey("test-key"),
		option.WithMaxRetries(0),
	)
	mailbox, err := NewMailbox(client, "test-inbox")
	if err != nil {
		t.Fatalf("NewMailbox: %v", err)
	}
	receiptID, err := mailbox.Reply(context.Background(), "message-1", payload, "key-1")
	if err != nil || receiptID != "reply-message-1" {
		t.Fatalf("Reply = %q, %v", receiptID, err)
	}
	return body
}

func TestMailboxFetchAttachmentEnforcesMaxBytes(t *testing.T) {
	const contents = "payload"
	download := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(contents))
	}))
	t.Cleanup(download.Close)

	fake, mailbox := newMailboxTestPair(t)
	message := testMessage("message-1", "thread-1", "body")
	message.Attachments = []agentmail.AttachmentFile{{
		AttachmentID: "attachment-1",
		Filename:     "payload.txt",
		ContentType:  "text/plain",
		Size:         int64(len(contents)),
	}}
	fake.add(message)
	fake.fail(
		http.MethodGet,
		fakeInboxPrefix+"messages/message-1/attachments/attachment-1",
		fakeHTTPResponse{
			status: http.StatusOK,
			body: `{"attachment_id":"attachment-1","download_url":"` + download.URL +
				`","expires_at":"2026-08-16T00:00:00Z","size":7}`,
		},
	)
	if _, err := mailbox.Message(context.Background(), "message-1"); err != nil {
		t.Fatalf("Message: %v", err)
	}

	got, err := mailbox.FetchAttachment(context.Background(), "attachment-1", int64(len(contents)))
	if err != nil || string(got) != contents {
		t.Fatalf("FetchAttachment = %q, %v", got, err)
	}
	if _, err := mailbox.FetchAttachment(context.Background(), "attachment-1", int64(len(contents)-1)); !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("FetchAttachment error = %v, want %v", err, ErrAttachmentTooLarge)
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
		{
			name: "HTML-only body",
			message: agentmail.Message{
				HTML: `<html><head><style>p { color: red; }</style></head><body><p>Hello <b>world</b>.</p><script>alert(1)</script><p>Second<br>line &amp; more <a href="https://example.com/x">link</a></p></body></html>`,
			},
			want: "Hello world.\n\nSecond\nline & more link (https://example.com/x)",
		},
		{
			name:    "text beats html",
			message: agentmail.Message{Text: "plain wins", HTML: "<p>html</p>"},
			want:    "plain wins",
		},
		{
			name: "extracted text beats html",
			message: agentmail.Message{
				ExtractedText: "extracted",
				HTML:          "<p>ignored</p>",
			},
			want: "extracted",
		},
		{
			name: "extracted html beats html",
			message: agentmail.Message{
				ExtractedHTML: "<p>from extracted</p>",
				HTML:          "<p>from raw html</p>",
			},
			want: "from extracted",
		},
		{
			name: "converted html beats preview",
			message: agentmail.Message{
				HTML:    "<p>html body</p>",
				Preview: "truncated preview...",
			},
			want: "html body",
		},
		{
			name: "whitespace-only html falls through to preview",
			message: agentmail.Message{
				HTML:    "<body>   </body>",
				Preview: "preview fallback",
			},
			want: "preview fallback",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mailbox := &Mailbox{attachmentMessages: make(map[string]string)}
			if got := messageBody(mailbox.normalize(test.message)); got != test.want {
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
