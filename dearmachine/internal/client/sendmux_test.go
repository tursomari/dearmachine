package client

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"sendmux.ai/go/mailbox"
)

type fakeSendmuxAPI struct {
	mu                    sync.Mutex
	mailbox               sendmuxMailboxInfo
	conversationReference string
	data                  map[string][]sendmuxRawMessage
	sends                 []sendmuxSendRequest
	seen                  []string
}

func newFakeSendmuxAPI(downloadURL string) *fakeSendmuxAPI {
	base := time.Date(2026, time.August, 19, 10, 0, 0, 0, time.UTC)
	conversationReference := newConversationReference()
	return &fakeSendmuxAPI{
		mailbox:               sendmuxMailboxInfo{ID: "mbx-test", Email: "device@myagent.mx", Status: "active"},
		conversationReference: conversationReference,
		data: map[string][]sendmuxRawMessage{
			"thread-old": {
				{ID: "message-old-1", ThreadID: "thread-old", From: "sender@example.com", To: []string{"device@myagent.mx"}, Subject: "Old", Text: "first", ReceivedAt: base, Seen: true, RFCMessageIDs: []string{"<old-1@example.com>"}},
				{ID: "message-old-2", ThreadID: "thread-old", From: "sender@example.com", To: []string{"device@myagent.mx"}, Subject: "Old", HTML: replyHTML(appendConversationFooter("second", conversationReference)), ReceivedAt: base.Add(time.Hour), RFCMessageIDs: []string{"<old-2@example.com>"}, Attachments: []sendmuxRawAttachment{{ID: "attachment-1", Filename: "request.txt", ContentType: "text/plain", SizeBytes: 7, DownloadURL: downloadURL}}},
			},
			"thread-new":     {{ID: "message-new", ThreadID: "thread-new", From: "sender@example.com", To: []string{"device@myagent.mx"}, ReplyTo: []string{"sender@example.com"}, Subject: "New", Text: "newest", ReceivedAt: base.Add(2 * time.Hour), RFCMessageIDs: []string{"<new@example.com>"}, References: []string{"<root@example.com>"}}},
			"thread-ignored": {{ID: "message-ignored", ThreadID: "thread-ignored", From: "stranger@example.net", To: []string{"device@myagent.mx"}, Subject: "Ignore", Text: "ignore", ReceivedAt: base.Add(90 * time.Minute), RFCMessageIDs: []string{"<ignored@example.net>"}}},
			"thread-contaminated": {
				{ID: "message-contaminated-1", ThreadID: "thread-contaminated", From: "stranger@example.net", To: []string{"device@myagent.mx"}, ReceivedAt: base.Add(100 * time.Minute)},
				{ID: "message-contaminated-2", ThreadID: "thread-contaminated", From: "sender@example.com", To: []string{"device@myagent.mx"}, ReceivedAt: base.Add(110 * time.Minute)},
			},
		},
	}
}

func (fake *fakeSendmuxAPI) ResolveMailbox(context.Context, string) (sendmuxMailboxInfo, error) {
	return fake.mailbox, nil
}

func (fake *fakeSendmuxAPI) ListUnread(_ context.Context, _ string) ([]string, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	var ids []string
	for _, threadID := range []string{"thread-old", "thread-ignored", "thread-contaminated", "thread-new"} {
		for _, message := range fake.data[threadID] {
			if !message.Seen {
				ids = append(ids, message.ID)
			}
		}
	}
	return ids, nil
}

func (fake *fakeSendmuxAPI) Message(_ context.Context, _, messageID string) (sendmuxRawMessage, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.find(messageID)
}

func (fake *fakeSendmuxAPI) Thread(_ context.Context, _, threadID string) ([]sendmuxRawMessage, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]sendmuxRawMessage(nil), fake.data[threadID]...), nil
}

func (fake *fakeSendmuxAPI) Send(_ context.Context, _ string, request sendmuxSendRequest, _ string) (string, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.sends = append(fake.sends, request)
	inbound, err := fake.find(request.ReplyToMessageID)
	if err != nil {
		return "", err
	}
	receipt := "outbound-1"
	fake.data[inbound.ThreadID] = append(fake.data[inbound.ThreadID], sendmuxRawMessage{
		ID: receipt, ThreadID: inbound.ThreadID, From: fake.mailbox.Email, To: request.To,
		Subject: request.Subject, Text: request.Text, HTML: request.HTML,
		SentAt: time.Date(2026, time.August, 19, 13, 0, 0, 0, time.UTC), Seen: true,
	})
	return receipt, nil
}

func (fake *fakeSendmuxAPI) MarkSeen(_ context.Context, _, messageID string) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for threadID, messages := range fake.data {
		for index := range messages {
			if messages[index].ID == messageID {
				messages[index].Seen = true
				fake.data[threadID] = messages
				fake.seen = append(fake.seen, messageID)
				return nil
			}
		}
	}
	return errors.New("missing message")
}

func (fake *fakeSendmuxAPI) find(messageID string) (sendmuxRawMessage, error) {
	for _, messages := range fake.data {
		for _, message := range messages {
			if message.ID == messageID {
				return message, nil
			}
		}
	}
	return sendmuxRawMessage{}, errors.New("missing message")
}

func TestSendmuxTransportContract(t *testing.T) {
	var downloadAuthorized bool
	download := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		downloadAuthorized = request.Header.Get("Authorization") != ""
		_, _ = io.WriteString(writer, "payload")
	}))
	defer download.Close()
	fake := newFakeSendmuxAPI(download.URL + "/attachment")
	transport, err := newSendmuxTransport(sendmuxTransportConfig{
		API: fake, Inbox: "device@myagent.mx", HTTPClient: download.Client(),
		AllowedFrom: []string{"sender@example.com"}, AllowedTo: []string{"sender@example.com"},
		AllowMutation: true, AllowInsecureURL: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	messages, err := transport.Poll(ctx)
	if err != nil || !slices.Equal(messageIDs(messages), []string{"message-old-2", "message-new"}) {
		t.Fatalf("Poll = %+v, %v", messages, err)
	}
	if messages[0].Body != "second" || !slices.Equal(messages[0].ConversationReferences, []string{fake.conversationReference}) || len(messages[0].Attachments) != 1 {
		t.Fatalf("normalized message = %+v", messages[0])
	}

	thread, err := transport.Thread(ctx, "thread-old")
	if err != nil || !slices.Equal(messageIDs(thread), []string{"message-old-1", "message-old-2"}) {
		t.Fatalf("Thread = %+v, %v", thread, err)
	}
	attachmentID := messages[0].Attachments[0].AttachmentID
	contents, err := transport.FetchAttachment(ctx, attachmentID, 7)
	if err != nil || string(contents) != "payload" || downloadAuthorized {
		t.Fatalf("FetchAttachment = %q, authorized=%v, err=%v", contents, downloadAuthorized, err)
	}
	if _, err := transport.FetchAttachment(ctx, attachmentID, 6); !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("FetchAttachment too large = %v", err)
	}

	receipt, err := transport.Reply(ctx, "message-new", ReplyPayload{
		Text: "done", HTML: "<p>done</p>", Files: []OutboundFile{{Filename: "result.txt", ContentType: "text/plain", Contents: []byte("result")}},
	}, "idempotency-1")
	if err != nil || receipt != "outbound-1" {
		t.Fatalf("Reply = %q, %v", receipt, err)
	}
	fake.mu.Lock()
	sends := append([]sendmuxSendRequest(nil), fake.sends...)
	fake.mu.Unlock()
	if len(sends) != 1 || sends[0].Subject != "Re: New" || len(sends[0].CustomHeaders) != 0 || sends[0].Files[0].Content != base64.StdEncoding.EncodeToString([]byte("result")) {
		t.Fatalf("send request = %+v", sends)
	}
	inbound, _ := transport.Message(ctx, "message-new")
	gotReceipt, found, err := transport.ReplyReceipt(ctx, inbound)
	if err != nil || !found || gotReceipt != receipt {
		t.Fatalf("ReplyReceipt = %q, %v, %v", gotReceipt, found, err)
	}
	if err := transport.MarkProcessed(ctx, "message-old-2"); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	messages, err = transport.Poll(ctx)
	if err != nil || !slices.Equal(messageIDs(messages), []string{"message-new"}) {
		t.Fatalf("Poll after mark = %+v, %v", messages, err)
	}
}

func TestSendmuxAllowListAndMutationGatesFailClosed(t *testing.T) {
	fake := newFakeSendmuxAPI("")
	transport, err := newSendmuxTransport(sendmuxTransportConfig{
		API: fake, Inbox: "mbx-test", AllowedFrom: []string{"sender@example.com"},
		AllowedTo: []string{"sender@example.com"}, AllowMutation: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	messages, err := transport.Poll(context.Background())
	if err != nil || slices.Contains(messageIDs(messages), "message-ignored") || slices.Contains(messageIDs(messages), "message-contaminated-2") {
		t.Fatalf("Poll exposed unauthorized messages: %+v, %v", messages, err)
	}
	if _, err := transport.Message(context.Background(), "message-ignored"); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("Message unauthorized = %v", err)
	}
	if _, err := transport.Reply(context.Background(), "message-new", ReplyPayload{Text: "blocked"}, "key"); err == nil || !strings.Contains(err.Error(), "DEARMACHINE_LIVE_SENDMUX_APPLY") {
		t.Fatalf("Reply gate = %v", err)
	}
	if err := transport.MarkProcessed(context.Background(), "message-new"); err == nil || !strings.Contains(err.Error(), "inspect-only") {
		t.Fatalf("MarkProcessed gate = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.sends) != 0 || len(fake.seen) != 0 {
		t.Fatalf("gated operations mutated API: sends=%+v seen=%v", fake.sends, fake.seen)
	}
}

func TestNewSendmuxTransportLoadsCredentialFile(t *testing.T) {
	credentialPath := filepath.Join(t.TempDir(), "sendmux-api-key")
	if err := os.WriteFile(credentialPath, []byte("smx_mbx_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SENDMUX_MAILBOX_API_KEY", "")
	t.Setenv("SENDMUX_MAILBOX_API_KEY_FILE", credentialPath)
	t.Setenv("DEARMACHINE_SENDMUX_ALLOWED_FROM", "sender@example.com")
	t.Setenv("DEARMACHINE_SENDMUX_ALLOWED_TO", "sender@example.com")
	t.Setenv("DEARMACHINE_LIVE_SENDMUX", "1")
	t.Setenv("DEARMACHINE_LIVE_SENDMUX_APPLY", "1")
	transport, err := NewSendmuxTransport("mbx-test")
	if err != nil {
		t.Fatalf("NewSendmuxTransport: %v", err)
	}
	if !transport.allowMutation {
		t.Fatal("constructor did not enable explicitly opted-in mutations")
	}
}

func TestNewSendmuxTransportRejectsMissingOrMultilineCredential(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SENDMUX_MAILBOX_API_KEY", "")
	t.Setenv("SENDMUX_MAILBOX_API_KEY_FILE", "")
	t.Setenv("DEARMACHINE_SENDMUX_ALLOWED_FROM", "sender@example.com")
	t.Setenv("DEARMACHINE_SENDMUX_ALLOWED_TO", "sender@example.com")
	if _, err := NewSendmuxTransport("mbx-test"); err == nil || !strings.Contains(err.Error(), "SENDMUX_MAILBOX_API_KEY or SENDMUX_MAILBOX_API_KEY_FILE is required") {
		t.Fatalf("missing credential = %v", err)
	}
	credentialPath := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(credentialPath, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SENDMUX_MAILBOX_API_KEY_FILE", credentialPath)
	if _, err := NewSendmuxTransport("mbx-test"); err == nil || !strings.Contains(err.Error(), "exactly one line") {
		t.Fatalf("multiline credential = %v", err)
	}
}

func TestSendmuxUnexpectedResponseIncludesValidationDetails(t *testing.T) {
	response := (*mailbox.MailboxSendMessageBadRequest)(&mailbox.ApiError{
		Error: mailbox.ApiErrorError{
			Message: "Invalid request parameters.",
			Errors: []mailbox.ApiErrorDetail{{
				Code: "custom", Field: "custom_headers.In-Reply-To",
				Message: "standard headers are not accepted here",
			}},
		},
	})
	err := sendmuxUnexpectedResponse("send message", response)
	if err == nil || !strings.Contains(err.Error(), "custom_headers.In-Reply-To") ||
		!strings.Contains(err.Error(), "standard headers are not accepted here") {
		t.Fatalf("sendmuxUnexpectedResponse = %v", err)
	}
}

func TestMailboxAddressesRepresentMissingDisplayNameAsNull(t *testing.T) {
	addresses := mailboxAddresses([]string{"sender@example.com"})
	if len(addresses) != 1 || addresses[0].GetEmail() != "sender@example.com" ||
		!addresses[0].GetName().IsNull() {
		t.Fatalf("mailboxAddresses = %+v", addresses)
	}
}
