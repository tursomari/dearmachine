package client

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var _ Transport = (*fakeTransport)(nil)

type fakeTransportErrors struct {
	Poll          error
	Thread        error
	Message       error
	Reply         error
	ReplyReceipt  error
	MarkProcessed error
}

type fakeTransportReply struct {
	MessageID      string
	Text           string
	IdempotencyKey string
	ReceiptID      string
}

type fakeTransport struct {
	mu               sync.Mutex
	poll             []Message
	threads          map[string][]Message
	messages         map[string]Message
	replies          []fakeTransportReply
	processed        []string
	attachments      map[string][]byte
	attachmentErrors map[string]error
	errors           fakeTransportErrors
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{
		threads:          make(map[string][]Message),
		messages:         make(map[string]Message),
		attachments:      make(map[string][]byte),
		attachmentErrors: make(map[string]error),
	}
}

func newFakeTransportFixture() *fakeTransport {
	transport := newFakeTransport()
	createdAt := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	message := Message{
		MessageID: "inbound-1",
		ThreadID:  "thread-1",
		From:      "sender@example.com",
		To:        []string{"device@example.com"},
		CreatedAt: createdAt,
		Subject:   "Request",
		Body:      "Please handle this request.",
		Labels:    []string{"unread"},
		Attachments: []AttachmentRef{{
			AttachmentID: "attachment-1",
			Filename:     "request.txt",
			ContentType:  "text/plain",
			SizeBytes:    7,
		}},
	}
	transport.setThread(message.ThreadID, []Message{message})
	transport.setPoll([]Message{message})
	transport.attachments["attachment-1"] = []byte("payload")
	return transport
}

func (f *fakeTransport) setPoll(messages []Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.poll = cloneMessages(messages)
	for _, message := range messages {
		f.messages[message.MessageID] = cloneMessage(message)
	}
}

func (f *fakeTransport) setThread(threadID string, messages []Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.threads[threadID] = cloneMessages(messages)
	for _, message := range messages {
		f.messages[message.MessageID] = cloneMessage(message)
	}
}

func (f *fakeTransport) Poll(context.Context) ([]Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errors.Poll != nil {
		return nil, f.errors.Poll
	}
	return cloneMessages(f.poll), nil
}

func (f *fakeTransport) Thread(_ context.Context, threadID string) ([]Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errors.Thread != nil {
		return nil, f.errors.Thread
	}
	return cloneMessages(f.threads[threadID]), nil
}

func (f *fakeTransport) Message(_ context.Context, messageID string) (Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errors.Message != nil {
		return Message{}, f.errors.Message
	}
	message, ok := f.messages[messageID]
	if !ok {
		return Message{}, fmt.Errorf("message %s not found", messageID)
	}
	return cloneMessage(message), nil
}

func (f *fakeTransport) Reply(
	_ context.Context,
	messageID,
	text,
	idempotencyKey string,
) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errors.Reply != nil {
		return "", f.errors.Reply
	}
	inbound, ok := f.messages[messageID]
	if !ok {
		return "", fmt.Errorf("message %s not found", messageID)
	}
	receiptID := fmt.Sprintf("outbound-%d", len(f.replies)+1)
	f.replies = append(f.replies, fakeTransportReply{
		MessageID:      messageID,
		Text:           text,
		IdempotencyKey: idempotencyKey,
		ReceiptID:      receiptID,
	})
	outbound := Message{
		MessageID: receiptID,
		ThreadID:  inbound.ThreadID,
		From:      "device@example.com",
		To:        []string{inbound.From},
		Timestamp: time.Now(),
		Body:      text,
		InReplyTo: messageID,
		Labels:    []string{"sent"},
	}
	f.messages[receiptID] = outbound
	f.threads[inbound.ThreadID] = append(f.threads[inbound.ThreadID], outbound)
	return receiptID, nil
}

func (f *fakeTransport) ReplyReceipt(
	_ context.Context,
	message Message,
) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errors.ReplyReceipt != nil {
		return "", false, f.errors.ReplyReceipt
	}
	for _, candidate := range f.threads[message.ThreadID] {
		if candidate.MessageID == message.MessageID {
			continue
		}
		isOutbound := containsFold(candidate.Labels, "sent") ||
			containsFold(candidate.To, message.From)
		if candidate.InReplyTo == message.MessageID && isOutbound {
			return candidate.MessageID, true, nil
		}
	}
	return "", false, nil
}

func (f *fakeTransport) MarkProcessed(_ context.Context, messageID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errors.MarkProcessed != nil {
		return f.errors.MarkProcessed
	}
	message, ok := f.messages[messageID]
	if !ok {
		return fmt.Errorf("message %s not found", messageID)
	}
	labels := make([]string, 0, len(message.Labels)+1)
	for _, label := range message.Labels {
		if !containsFold([]string{label}, "unread") {
			labels = append(labels, label)
		}
	}
	if !containsFold(labels, "read") {
		labels = append(labels, "read")
	}
	message.Labels = labels
	f.messages[messageID] = message
	for index, candidate := range f.poll {
		if candidate.MessageID == messageID {
			f.poll[index] = cloneMessage(message)
		}
	}
	for index, candidate := range f.threads[message.ThreadID] {
		if candidate.MessageID == messageID {
			f.threads[message.ThreadID][index] = cloneMessage(message)
		}
	}
	f.processed = append(f.processed, messageID)
	return nil
}

func (f *fakeTransport) FetchAttachment(
	_ context.Context,
	attachmentID string,
	maxBytes int64,
) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.attachmentErrors[attachmentID]; err != nil {
		return nil, err
	}
	contents, ok := f.attachments[attachmentID]
	if !ok {
		return nil, errors.New("attachment not found")
	}
	if int64(len(contents)) > maxBytes {
		return nil, fmt.Errorf("fetch attachment %s: %w", attachmentID, ErrAttachmentTooLarge)
	}
	return append([]byte(nil), contents...), nil
}

func cloneMessages(messages []Message) []Message {
	cloned := make([]Message, len(messages))
	for index, message := range messages {
		cloned[index] = cloneMessage(message)
	}
	return cloned
}

func cloneMessage(message Message) Message {
	message.To = append([]string(nil), message.To...)
	message.References = append([]string(nil), message.References...)
	message.Labels = append([]string(nil), message.Labels...)
	message.Attachments = append([]AttachmentRef(nil), message.Attachments...)
	return message
}
