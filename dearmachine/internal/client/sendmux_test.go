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

type fakeSendmuxOutboundAPI struct {
	from    string
	request sendmuxSendRequest
	key     string
}

func (fake *fakeSendmuxOutboundAPI) Send(_ context.Context, from string, request sendmuxSendRequest, key string) (string, error) {
	fake.from, fake.request, fake.key = from, request, key
	return "outbound-send-api", nil
}

func newFakeSendmuxAPI(downloadURL string) *fakeSendmuxAPI {
	base := time.Date(2026, time.August, 19, 10, 0, 0, 0, time.UTC)
	conversationReference := newConversationReference()
	return &fakeSendmuxAPI{
		mailbox:               sendmuxMailboxInfo{ID: "mbx-test", Email: "device@myagent.mx", Status: "active", SentFolderIDs: []string{"folder-sent"}},
		conversationReference: conversationReference,
		data: map[string][]sendmuxRawMessage{
			"thread-old": {
				{ID: "message-old-1", ThreadID: "thread-old", From: "sender@example.com", To: []string{"device@myagent.mx"}, Subject: "Old", Text: "first", ReceivedAt: base, Seen: true, RFCMessageIDs: []string{"<old-1@example.com>"}},
				{ID: "message-old-2", ThreadID: "thread-old", From: "sender@example.com", To: []string{"device@myagent.mx"}, Subject: "Old", HTML: replyHTML(appendConversationFooter("second", conversationReference, nil)), ReceivedAt: base.Add(time.Hour), RFCMessageIDs: []string{"<old-2@example.com>"}, Attachments: []sendmuxRawAttachment{{ID: "attachment-1", Filename: "request.txt", ContentType: "text/plain", SizeBytes: 7, DownloadURL: downloadURL}}},
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
		ID: receipt, FolderIDs: []string{"folder-sent"}, ThreadID: inbound.ThreadID, From: fake.mailbox.Email, To: request.To,
		Subject: request.Subject, Text: request.Text, HTML: request.HTML, InReplyTo: request.ParentRFCMessageID,
		SentAt: time.Date(2026, time.August, 19, 13, 0, 0, 0, time.UTC), Seen: true,
	})
	return receipt, nil
}

func (fake *fakeSendmuxAPI) SetSeen(_ context.Context, _, messageID string, seen bool) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for threadID, messages := range fake.data {
		for index := range messages {
			if messages[index].ID == messageID {
				messages[index].Seen = seen
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
		AllowMutation: true, AllowInsecureURL: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	messages, err := transport.Poll(ctx)
	if err != nil || !slices.Equal(messageIDs(messages), []string{"message-old-2", "message-ignored", "message-contaminated-1", "message-contaminated-2", "message-new"}) {
		t.Fatalf("Poll = %+v, %v", messages, err)
	}
	wantReference := canonicalInboundReference(shortConversationReference(fake.conversationReference))
	if messages[0].Body != "second" || !slices.Equal(messages[0].ConversationReferences, []string{wantReference}) || len(messages[0].Attachments) != 1 {
		t.Fatalf("normalized message = %+v", messages[0])
	}

	thread, err := transport.Thread(ctx, "thread-old")
	if err != nil || !slices.Equal(messageIDs(thread), []string{"message-old-1", "message-old-2"}) {
		t.Fatalf("Thread = %+v, %v", thread, err)
	}
	attachmentID := messages[0].Attachments[0].AttachmentID
	if _, err := transport.FetchAttachment(ctx, attachmentID, 7); !errors.Is(err, ErrMessageUnauthenticated) {
		t.Fatalf("unauthenticated attachment: %v", err)
	}
	if matched, err := transport.verifyReceiptAttachment(ctx, attachmentID, []byte("payload")); err != nil || !matched || downloadAuthorized {
		t.Fatalf("receipt attachment matched=%v, authorized=%v, err=%v", matched, downloadAuthorized, err)
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
	gotReceipt, found, err := transport.ReplyReceipt(ctx, inbound, "")
	if err != nil || !found || gotReceipt != receipt {
		t.Fatalf("ReplyReceipt = %q, %v, %v", gotReceipt, found, err)
	}
	if err := transport.MarkProcessed(ctx, "message-old-2"); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	messages, err = transport.Poll(ctx)
	if err != nil || !slices.Equal(messageIDs(messages), []string{"message-ignored", "message-contaminated-1", "message-contaminated-2", "message-new"}) {
		t.Fatalf("Poll after mark = %+v, %v", messages, err)
	}
}

func TestSendmuxNormalizePreservesRecipientRolesAndDelivery(t *testing.T) {
	transport := &SendmuxTransport{resolved: sendmuxMailboxInfo{
		ID: "mbx-test", Email: "device@myagent.mx",
	}}
	normalized := transport.normalize(sendmuxRawMessage{
		ID: "message-multi", ThreadID: "thread-existing",
		From: "controller@example.test", To: []string{"primary@example.test"},
		CC: []string{"Device <device@myagent.mx>"}, BCC: []string{"audit@example.test"},
	})
	if !slices.Equal(normalized.To, []string{"primary@example.test"}) ||
		!slices.Equal(normalized.CC, []string{"Device <device@myagent.mx>"}) ||
		!slices.Equal(normalized.BCC, []string{"audit@example.test"}) ||
		normalized.Delivery.InboxID != "mbx-test" ||
		normalized.Delivery.Recipient != "device@myagent.mx" ||
		normalized.Delivery.Role != DeliveryRoleCC ||
		normalized.Delivery.ReadState != MessageReadStateUnread {
		t.Fatalf("normalized delivery = %+v", normalized)
	}
}

func TestSendmuxMutationsHonorInjectedReadOnlyConfiguration(t *testing.T) {
	fake := newFakeSendmuxAPI("")
	transport, err := newSendmuxTransport(sendmuxTransportConfig{
		API: fake, Inbox: "mbx-test", AllowMutation: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	messages, err := transport.Poll(context.Background())
	if err != nil || !slices.Contains(messageIDs(messages), "message-ignored") {
		t.Fatalf("Poll = %+v, %v", messages, err)
	}
	if _, err := transport.Message(context.Background(), "message-ignored"); err != nil {
		t.Fatalf("Message: %v", err)
	}
	if _, err := transport.Reply(context.Background(), "message-new", ReplyPayload{Text: "blocked"}, "key"); err == nil || !strings.Contains(err.Error(), "disabled by adapter configuration") {
		t.Fatalf("Reply gate = %v", err)
	}
	if err := transport.MarkProcessed(context.Background(), "message-new"); err == nil || !strings.Contains(err.Error(), "disabled by adapter configuration") {
		t.Fatalf("MarkProcessed gate = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.sends) != 0 || len(fake.seen) != 0 {
		t.Fatalf("gated operations mutated API: sends=%+v seen=%v", fake.sends, fake.seen)
	}
}

func TestSendmuxTransportUsesConfiguredSendingAPIForReplies(t *testing.T) {
	fake := newFakeSendmuxAPI("")
	outbound := &fakeSendmuxOutboundAPI{}
	transport, err := newSendmuxTransport(sendmuxTransportConfig{
		API: fake, Outbound: outbound, Inbox: "mbx-test", AllowMutation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := transport.Reply(context.Background(), "message-new", ReplyPayload{Text: "done"}, "reply-key")
	if err != nil || receipt != "outbound-send-api" {
		t.Fatalf("Reply = %q, %v", receipt, err)
	}
	if outbound.from != fake.mailbox.Email || outbound.request.To[0] != "sender@example.com" || outbound.key != "reply-key" {
		t.Fatalf("sending API request = %+v from %q key %q", outbound.request, outbound.from, outbound.key)
	}
	if outbound.request.HTML != "<pre>done</pre>" {
		t.Fatalf("sending API html body = %q, want plain-text fallback", outbound.request.HTML)
	}
	if len(fake.sends) != 0 {
		t.Fatalf("mailbox send should not have been used: %+v", fake.sends)
	}
}

func TestSendmuxReplyPreservesThreadWithPrivateRecipient(t *testing.T) {
	fake := newFakeSendmuxAPI("")
	transport, err := newSendmuxTransport(sendmuxTransportConfig{
		API: fake, Inbox: "mbx-test", AllowMutation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := transport.Reply(context.Background(), "message-new", ReplyPayload{
		Text: "private control", To: []string{"controller@example.test"},
	}, "private-key")
	if err != nil || receipt != "outbound-1" {
		t.Fatalf("Reply = %q, %v", receipt, err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.sends) != 1 {
		t.Fatalf("private sends = %+v", fake.sends)
	}
	send := fake.sends[0]
	if !slices.Equal(send.To, []string{"controller@example.test"}) ||
		send.ReplyToMessageID != "message-new" {
		t.Fatalf("private send envelope/thread = %+v", send)
	}
	fake.mu.Unlock()
	recovered, found, err := transport.ReplyReceipt(context.Background(), Message{
		MessageID: "message-new", ThreadID: "thread-new", From: "sender@example.com",
	}, "controller@example.test")
	fake.mu.Lock()
	if err != nil || !found || recovered != receipt {
		t.Fatalf("private ReplyReceipt = %q, %v, %v; want %q, true, nil", recovered, found, err, receipt)
	}
}

func TestNewSendmuxTransportLoadsCredentialFile(t *testing.T) {
	credentialPath := filepath.Join(t.TempDir(), "sendmux-api-key")
	if err := os.WriteFile(credentialPath, []byte("smx_mbx_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SENDMUX_MAILBOX_API_KEY", "")
	t.Setenv("SENDMUX_MAILBOX_API_KEY_FILE", credentialPath)
	transport, err := NewSendmuxTransport("mbx-test")
	if err != nil {
		t.Fatalf("NewSendmuxTransport: %v", err)
	}
	if !transport.allowMutation {
		t.Fatal("constructor did not enable normal runtime mutations")
	}
}

func TestLoadSendmuxSendCredential(t *testing.T) {
	t.Setenv("SENDMUX_SEND_API_KEY", "")
	t.Setenv("SENDMUX_SEND_API_KEY_FILE", "")
	credential, configured, err := loadSendmuxSendCredential()
	if err != nil || configured || credential != "" {
		t.Fatalf("unset credential = %q, %v, %v", credential, configured, err)
	}
	credentialPath := filepath.Join(t.TempDir(), "sendmux-send-api-key")
	if err := os.WriteFile(credentialPath, []byte("smx_snd_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SENDMUX_SEND_API_KEY_FILE", credentialPath)
	credential, configured, err = loadSendmuxSendCredential()
	if err != nil || !configured || credential != "smx_snd_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("file credential = %q, %v, %v", credential, configured, err)
	}
	if err := os.WriteFile(credentialPath, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadSendmuxSendCredential(); err == nil || !strings.Contains(err.Error(), "exactly one line") {
		t.Fatalf("multiline send credential = %v", err)
	}
}

func TestNewSendmuxTransportRejectsMissingOrMultilineCredential(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SENDMUX_MAILBOX_API_KEY", "")
	t.Setenv("SENDMUX_MAILBOX_API_KEY_FILE", "")
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
