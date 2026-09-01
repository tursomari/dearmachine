package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type openMailReplyRecord struct {
	To             string
	Body           string
	HTML           string
	ThreadID       string
	IncludeQuote   string
	IdempotencyKey string
	Filename       string
	FileContents   string
}

type fakeOpenMailAPI struct {
	t                            *testing.T
	server                       *httptest.Server
	mu                           sync.Mutex
	read                         map[string]bool
	data                         map[string][]openMailMessage
	replies                      []openMailReplyRecord
	patches                      []string
	downloadWithoutAuthorization bool
}

func newFakeOpenMailAPI(t *testing.T) *fakeOpenMailAPI {
	t.Helper()
	base := time.Date(2026, time.August, 17, 10, 0, 0, 0, time.UTC)
	fake := &fakeOpenMailAPI{
		t: t,
		read: map[string]bool{
			"thread-old":          false,
			"thread-ignored":      false,
			"thread-contaminated": false,
			"thread-new":          false,
		},
		data: map[string][]openMailMessage{
			"thread-old": {
				{
					ID: "message-old-2", ThreadID: "thread-old", Direction: "inbound",
					FromAddr: "Allowed Sender <sender@example.com>", ToAddr: "device@openmail.sh",
					HeaderTo: "device@openmail.sh", Subject: "Old thread", BodyText: "second",
					CreatedAt: base.Add(time.Hour), Attachments: []openMailAttachment{{
						Filename: "request.txt", ContentType: "text/plain", SizeBytes: 7,
					}},
				},
				{
					ID: "message-old-1", ThreadID: "thread-old", Direction: "inbound",
					FromAddr: "sender@example.com", ToAddr: "device@openmail.sh",
					HeaderTo: "device@openmail.sh", Subject: "Old thread", BodyText: "first",
					CreatedAt: base,
				},
			},
			"thread-ignored": {{
				ID: "message-ignored", ThreadID: "thread-ignored", Direction: "inbound",
				FromAddr: "stranger@example.net", ToAddr: "device@openmail.sh",
				Subject: "Untrusted", BodyText: "ignore me", CreatedAt: base.Add(90 * time.Minute),
				Attachments: []openMailAttachment{{Filename: "untrusted.txt", SizeBytes: 3}},
			}},
			"thread-contaminated": {
				{
					ID: "message-contaminated-untrusted", ThreadID: "thread-contaminated", Direction: "inbound",
					FromAddr: "sender@example.com", ToAddr: "device@openmail.sh", CC: []string{"stranger@example.net"},
					Subject: "Mixed", BodyText: "foreign", CreatedAt: base.Add(100 * time.Minute),
				},
				{
					ID: "message-contaminated-allowed", ThreadID: "thread-contaminated", Direction: "inbound",
					FromAddr: "sender@example.com", ToAddr: "device@openmail.sh",
					Subject: "Mixed", BodyText: "allowed but unsafe thread", CreatedAt: base.Add(110 * time.Minute),
				},
			},
			"thread-new": {{
				ID: "message-new", ThreadID: "thread-new", Direction: "inbound",
				FromAddr: "sender@example.com", ToAddr: "device@openmail.sh",
				Subject: "New thread", BodyText: "newest", CreatedAt: base.Add(2 * time.Hour),
			}},
		},
	}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serveHTTP))
	t.Cleanup(fake.server.Close)
	return fake
}

func (fake *fakeOpenMailAPI) transport(t *testing.T, inbox string, allowMutation bool) *OpenMailTransport {
	t.Helper()
	transport, err := newOpenMailTransport(openMailTransportConfig{
		BaseURL:       fake.server.URL,
		APIKey:        "offline-openmail-key",
		Inbox:         inbox,
		HTTPClient:    fake.server.Client(),
		AllowMutation: allowMutation,
	})
	if err != nil {
		t.Fatalf("newOpenMailTransport: %v", err)
	}
	return transport
}

func (fake *fakeOpenMailAPI) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if strings.HasPrefix(request.URL.Path, "/download/") {
		fake.downloadWithoutAuthorization = request.Header.Get("Authorization") == ""
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(writer, "payload")
		return
	}
	if request.Header.Get("Authorization") != "Bearer offline-openmail-key" {
		fake.writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/v1/inboxes":
		fake.writeJSON(writer, openMailList[openMailInbox]{
			Data:  []openMailInbox{{ID: "inb-test", Address: "device@openmail.sh"}},
			Total: 1,
		})
	case request.Method == http.MethodGet && request.URL.Path == "/v1/inboxes/inb-test":
		fake.writeJSON(writer, openMailInbox{ID: "inb-test", Address: "device@openmail.sh"})
	case request.Method == http.MethodGet && request.URL.Path == "/v1/inboxes/inb-test/threads":
		var threads []openMailThread
		for _, threadID := range []string{"thread-new", "thread-contaminated", "thread-ignored", "thread-old"} {
			if request.URL.Query().Get("is_read") == "false" && fake.read[threadID] {
				continue
			}
			threads = append(threads, openMailThread{ID: threadID, IsRead: fake.read[threadID]})
		}
		fake.writeJSON(writer, openMailList[openMailThread]{Data: threads, Total: len(threads)})
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/threads/") && strings.HasSuffix(request.URL.Path, "/messages"):
		threadID := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/v1/threads/"), "/messages")
		messages, ok := fake.data[threadID]
		if !ok {
			fake.writeError(writer, http.StatusNotFound, "not_found")
			return
		}
		fake.writeJSON(writer, openMailThreadMessages{
			ThreadID: threadID,
			Subject:  messages[0].Subject,
			IsRead:   fake.read[threadID],
			Data:     messages,
		})
	case request.Method == http.MethodGet && request.URL.Path == "/v1/inboxes/inb-test/messages":
		var messages []openMailMessage
		for _, threadID := range []string{"thread-new", "thread-contaminated", "thread-ignored", "thread-old"} {
			messages = append(messages, fake.data[threadID]...)
		}
		fake.writeJSON(writer, openMailList[openMailMessage]{Data: messages, Total: len(messages)})
	case request.Method == http.MethodPost && request.URL.Path == "/v1/inboxes/inb-test/send":
		fake.serveSend(writer, request)
	case request.Method == http.MethodPatch && strings.HasPrefix(request.URL.Path, "/v1/threads/"):
		threadID := strings.TrimPrefix(request.URL.Path, "/v1/threads/")
		var update struct {
			IsRead bool `json:"is_read"`
		}
		if err := json.NewDecoder(request.Body).Decode(&update); err != nil {
			fake.t.Errorf("decode thread patch: %v", err)
		}
		fake.read[threadID] = update.IsRead
		fake.patches = append(fake.patches, threadID)
		fake.writeJSON(writer, map[string]bool{"ok": true})
	case request.Method == http.MethodGet && request.URL.Path == "/v1/attachments/message-old-2/request.txt":
		writer.Header().Set("Location", fake.server.URL+"/download/request.txt")
		writer.WriteHeader(http.StatusFound)
	default:
		fake.t.Errorf("unexpected OpenMail request: %s %s", request.Method, request.URL.String())
		fake.writeError(writer, http.StatusNotFound, "not_found")
	}
}

func (fake *fakeOpenMailAPI) serveSend(writer http.ResponseWriter, request *http.Request) {
	record := openMailReplyRecord{IdempotencyKey: request.Header.Get("Idempotency-Key")}
	contentType := request.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := request.ParseMultipartForm(2 << 20); err != nil {
			fake.t.Errorf("parse multipart reply: %v", err)
		}
		record.To = request.FormValue("to")
		record.Body = request.FormValue("body")
		record.HTML = request.FormValue("bodyHtml")
		record.ThreadID = request.FormValue("threadId")
		record.IncludeQuote = request.FormValue("includeQuote")
		file, header, err := request.FormFile("attachments")
		if err != nil {
			fake.t.Errorf("read multipart attachment: %v", err)
		} else {
			defer file.Close()
			contents, readErr := io.ReadAll(file)
			if readErr != nil {
				fake.t.Errorf("read attachment contents: %v", readErr)
			}
			record.Filename = header.Filename
			record.FileContents = string(contents)
		}
	} else {
		var payload struct {
			To           string `json:"to"`
			Body         string `json:"body"`
			HTML         string `json:"bodyHtml"`
			ThreadID     string `json:"threadId"`
			IncludeQuote bool   `json:"includeQuote"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			fake.t.Errorf("decode JSON reply: %v", err)
		}
		record.To, record.Body, record.HTML, record.ThreadID = payload.To, payload.Body, payload.HTML, payload.ThreadID
		record.IncludeQuote = fmt.Sprint(payload.IncludeQuote)
	}
	fake.replies = append(fake.replies, record)
	receiptID := fmt.Sprintf("outbound-%d", len(fake.replies))
	fake.data[record.ThreadID] = append(fake.data[record.ThreadID], openMailMessage{
		ID: receiptID, ThreadID: record.ThreadID, Direction: "outbound",
		FromAddr: "device@openmail.sh", ToAddr: record.To, BodyText: record.Body,
		CreatedAt: time.Date(2026, time.August, 17, 13, 0, len(fake.replies), 0, time.UTC),
	})
	fake.read[record.ThreadID] = true
	fake.writeJSON(writer, map[string]string{
		"messageId": receiptID,
		"threadId":  record.ThreadID,
		"status":    "sent",
	})
}

func (fake *fakeOpenMailAPI) writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		fake.t.Errorf("encode response: %v", err)
	}
}

func (fake *fakeOpenMailAPI) writeError(writer http.ResponseWriter, status int, code string) {
	writer.WriteHeader(status)
	fake.writeJSON(writer, map[string]string{"error": code, "message": "test error"})
}

func TestOpenMailTransportContract(t *testing.T) {
	fake := newFakeOpenMailAPI(t)
	transport := fake.transport(t, "device@openmail.sh", true)
	ctx := context.Background()

	messages, err := transport.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got := messageIDs(messages); !slices.Equal(got, []string{"message-old-2", "message-ignored", "message-contaminated-allowed", "message-new"}) {
		t.Fatalf("Poll IDs = %v", got)
	}
	if messages[0].Body != "second" || len(messages[0].Attachments) != 1 ||
		!containsFold(messages[0].Labels, "unread") {
		t.Fatalf("normalized poll message = %+v", messages[0])
	}

	thread, err := transport.Thread(ctx, "thread-old")
	if err != nil {
		t.Fatalf("Thread: %v", err)
	}
	if got := messageIDs(thread); !slices.Equal(got, []string{"message-old-1", "message-old-2"}) {
		t.Fatalf("Thread IDs = %v", got)
	}

	attachmentID := messages[0].Attachments[0].AttachmentID
	contents, err := transport.FetchAttachment(ctx, attachmentID, 7)
	if err != nil || string(contents) != "payload" {
		t.Fatalf("FetchAttachment = %q, %v", contents, err)
	}
	if !fake.downloadWithoutAuthorization {
		t.Fatal("OpenMail bearer credential was sent to the signed download URL")
	}
	if _, err := transport.FetchAttachment(ctx, attachmentID, 6); !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("FetchAttachment too-large error = %v", err)
	}

	if err := transport.MarkProcessed(ctx, "message-old-2"); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	messages, err = transport.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll after MarkProcessed: %v", err)
	}
	if got := messageIDs(messages); !slices.Equal(got, []string{"message-ignored", "message-contaminated-allowed", "message-new"}) {
		t.Fatalf("Poll after MarkProcessed IDs = %v", got)
	}

	receipt, err := transport.Reply(ctx, "message-new", ReplyPayload{
		Text: "completed",
		HTML: "<p>completed</p>",
		Files: []OutboundFile{{
			Filename: "result.txt", ContentType: "text/plain", Contents: []byte("result"),
		}},
	}, "idempotency-1")
	if err != nil || receipt != "outbound-1" {
		t.Fatalf("Reply = %q, %v", receipt, err)
	}
	fake.mu.Lock()
	replies := append([]openMailReplyRecord(nil), fake.replies...)
	fake.mu.Unlock()
	wantReply := openMailReplyRecord{
		To: "sender@example.com", Body: "completed", HTML: "<p>completed</p>",
		ThreadID: "thread-new", IncludeQuote: "false", IdempotencyKey: "idempotency-1",
		Filename: "result.txt", FileContents: "result",
	}
	if len(replies) != 1 || replies[0] != wantReply {
		t.Fatalf("reply record = %+v, want %+v", replies, wantReply)
	}
	inbound, err := transport.Message(ctx, "message-new")
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	gotReceipt, found, err := transport.ReplyReceipt(ctx, inbound)
	if err != nil || !found || gotReceipt != receipt {
		t.Fatalf("ReplyReceipt = %q, %v, %v", gotReceipt, found, err)
	}
}

func TestOpenMailAllowListIgnoresWithoutMutation(t *testing.T) {
	fake := newFakeOpenMailAPI(t)
	transport := fake.transport(t, "inb-test", true)
	ctx := context.Background()

	messages, err := transport.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !slices.Contains(messageIDs(messages), "message-ignored") {
		t.Fatalf("Poll = %+v", messages)
	}
	if _, err := transport.Message(ctx, "message-ignored"); err != nil {
		t.Fatalf("Message: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.replies) != 0 || len(fake.patches) != 0 {
		t.Fatalf("ignored message caused mutations: replies=%+v patches=%v", fake.replies, fake.patches)
	}
}

func TestOpenMailMutationsHonorInjectedReadOnlyConfiguration(t *testing.T) {
	fake := newFakeOpenMailAPI(t)
	transport := fake.transport(t, "inb-test", false)
	if _, err := transport.Reply(
		context.Background(),
		"message-new",
		ReplyPayload{Text: "blocked"},
		"blocked-key",
	); err == nil || !strings.Contains(err.Error(), "disabled by adapter configuration") {
		t.Fatalf("Reply gate error = %v", err)
	}
	if err := transport.MarkProcessed(context.Background(), "message-new"); err == nil ||
		!strings.Contains(err.Error(), "disabled by adapter configuration") {
		t.Fatalf("MarkProcessed gate error = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.replies) != 0 || len(fake.patches) != 0 {
		t.Fatalf("gated operations mutated API: replies=%+v patches=%v", fake.replies, fake.patches)
	}
}

func TestNewOpenMailTransportLoadsOneLineCredentialFile(t *testing.T) {
	credentialPath := filepath.Join(t.TempDir(), "openmail-api-key")
	if err := os.WriteFile(credentialPath, []byte("offline-file-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENMAIL_API_KEY", "")
	t.Setenv("OPENMAIL_API_KEY_FILE", credentialPath)
	transport, err := NewOpenMailTransport("inb-test")
	if err != nil {
		t.Fatalf("NewOpenMailTransport: %v", err)
	}
	if transport.apiKey != "offline-file-key" || transport.baseURL.String() != openMailAPIBaseURL ||
		!transport.allowMutation {
		t.Fatal("OpenMail constructor did not load the expected local configuration")
	}
}

func TestNewOpenMailTransportFailsClosedWithoutCredential(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENMAIL_API_KEY", "")
	t.Setenv("OPENMAIL_API_KEY_FILE", "")
	_, err := NewOpenMailTransport("inb-test")
	if err == nil || !strings.Contains(err.Error(), "OPENMAIL_API_KEY or OPENMAIL_API_KEY_FILE is required") {
		t.Fatalf("NewOpenMailTransport error = %v", err)
	}
}

func TestOpenMailRejectsRedirectOutsideAPIOrigin(t *testing.T) {
	outside := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("outside attachment host must not be contacted")
	}))
	defer outside.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/v1/inboxes/inb-test":
			_ = json.NewEncoder(writer).Encode(openMailInbox{ID: "inb-test", Address: "device@openmail.sh"})
		case request.URL.Path == "/v1/inboxes/inb-test/messages":
			_ = json.NewEncoder(writer).Encode(openMailList[openMailMessage]{Data: []openMailMessage{{
				ID: "message", ThreadID: "thread", Direction: "inbound", FromAddr: "sender@example.com", ToAddr: "device@openmail.sh",
				Attachments: []openMailAttachment{{Filename: "file.txt", SizeBytes: 1}},
			}}, Total: 1})
		case strings.HasPrefix(request.URL.Path, "/v1/attachments/"):
			writer.Header().Set("Location", outside.URL+"/file")
			writer.WriteHeader(http.StatusFound)
		default:
			t.Errorf("unexpected request: %s", request.URL.String())
		}
	}))
	defer server.Close()
	transport, err := newOpenMailTransport(openMailTransportConfig{
		BaseURL: server.URL, APIKey: "key", Inbox: "inb-test", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = transport.FetchAttachment(
		context.Background(),
		encodeOpenMailAttachmentID("message", "file.txt"),
		1,
	)
	if err == nil || !strings.Contains(err.Error(), "refused redirect") {
		t.Fatalf("FetchAttachment redirect error = %v", err)
	}
}

func messageIDs(messages []Message) []string {
	ids := make([]string, len(messages))
	for index, message := range messages {
		ids[index] = message.MessageID
	}
	return ids
}

func TestOpenMailAPIBaseURLIsCanonicalDocumentedHost(t *testing.T) {
	parsed, err := url.Parse(openMailAPIBaseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "api.openmail.sh" || parsed.Path != "" {
		t.Fatalf("openMailAPIBaseURL = %q, %v", openMailAPIBaseURL, err)
	}
}

func TestOpenMailNormalizeExtractsAndStripsHTMLFooter(t *testing.T) {
	reference := newConversationReference()
	transport := &OpenMailTransport{}
	normalized := transport.normalize(openMailMessage{
		ID:        "message-1",
		ThreadID:  "provider-thread-b",
		Direction: "inbound",
		FromAddr:  "sender@example.com",
		ToAddr:    "device@openmail.sh",
		BodyHTML: "<p>Continue with the next section.</p><blockquote>" +
			"<p style=\"white-space: pre-wrap\">Earlier answer.\n" +
			conversationFooter(reference) + "</p></blockquote>",
	}, false)
	if normalized.Body != "Continue with the next section." {
		t.Fatalf("normalized body = %q, want only the new contribution", normalized.Body)
	}
	if strings.Contains(normalized.Body, "Dear Machine:") || strings.Contains(normalized.Body, "session dm1-") {
		t.Fatalf("normalized body retained footer metadata: %q", normalized.Body)
	}
	if len(normalized.ConversationReferences) != 1 ||
		normalized.ConversationReferences[0] != reference {
		t.Fatalf("normalized references = %v, want [%s]", normalized.ConversationReferences, reference)
	}
}

func TestOpenMailNormalizeStripsLabeledQuoteFromSessionFooter(t *testing.T) {
	reference := newConversationReference()
	transport := &OpenMailTransport{}
	normalized := transport.normalize(openMailMessage{
		ID:        "message-quote",
		ThreadID:  "provider-thread-quote",
		Direction: "inbound",
		FromAddr:  "sender@example.com",
		ToAddr:    "device@openmail.sh",
		BodyHTML: "<p>Continue with the next section.</p><blockquote>" +
			"<p style=\"white-space: pre-wrap\">Earlier answer.\n" +
			conversationFooter(reference) + "\n\n" + conversationFooterMotto + " " +
			conversationFooterQuoteLabel + ":\n\"Humanity is our finest work.\"</p></blockquote>",
	}, false)
	if normalized.Body != "Continue with the next section." {
		t.Fatalf("normalized body = %q, want only the new contribution", normalized.Body)
	}
	if strings.Contains(strings.ToLower(normalized.Body), "quote:") {
		t.Fatalf("normalized body retained quote footer metadata: %q", normalized.Body)
	}
	if len(normalized.ConversationReferences) != 1 ||
		normalized.ConversationReferences[0] != reference {
		t.Fatalf("normalized references = %v, want [%s]", normalized.ConversationReferences, reference)
	}
}
