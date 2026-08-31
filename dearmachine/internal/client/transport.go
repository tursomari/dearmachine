package client

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

var ErrAttachmentTooLarge = errors.New("attachment too large")

// Message is the transport-neutral representation of an email message.
type Message struct {
	MessageID              string
	ThreadID               string
	From                   string
	To                     []string
	Timestamp              time.Time
	CreatedAt              time.Time
	Subject                string
	Body                   string
	InReplyTo              string
	References             []string
	ConversationReferences []string
	Labels                 []string
	Attachments            []AttachmentRef
}

// AttachmentRef describes an inbound attachment without loading its contents.
type AttachmentRef struct {
	AttachmentID string
	Filename     string
	ContentType  string
	SizeBytes    int64
}

type OutboundFile struct {
	Filename    string
	ContentType string
	Contents    []byte
}

type ReplyPayload struct {
	Text  string
	HTML  string
	Files []OutboundFile
}

// Transport is the email surface used by the dearmachine orchestrator.
type Transport interface {
	Poll(ctx context.Context) ([]Message, error)
	Thread(ctx context.Context, threadID string) ([]Message, error)
	Message(ctx context.Context, messageID string) (Message, error)
	Reply(ctx context.Context, messageID string, payload ReplyPayload, idempotencyKey string) (string, error)
	ReplyReceipt(ctx context.Context, message Message) (string, bool, error)
	MarkProcessed(ctx context.Context, messageID string) error
	FetchAttachment(ctx context.Context, attachmentID string, maxBytes int64) ([]byte, error)
}

// PairAuthorizer is an optional provider capability used while creating a
// pair. Adapters with provider-side correspondent policy implement it so the
// sender is accepted before the local pair becomes reachable.
type PairAuthorizer interface {
	AuthorizePair(ctx context.Context, email string) error
}

func sortMessages(messages []Message) {
	sort.SliceStable(messages, func(i, j int) bool {
		return messageTime(messages[i]).Before(messageTime(messages[j]))
	})
}

func messageTime(message Message) time.Time {
	if !message.Timestamp.IsZero() {
		return message.Timestamp
	}
	return message.CreatedAt
}

func messageBody(message Message) string {
	return message.Body
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}
