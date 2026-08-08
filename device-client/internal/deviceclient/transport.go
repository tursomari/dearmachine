package deviceclient

import (
	"context"
	"time"
)

// Message is the transport-neutral representation of an email message.
type Message struct {
	MessageID   string
	ThreadID    string
	From        string
	To          []string
	Timestamp   time.Time
	CreatedAt   time.Time
	Subject     string
	Body        string
	InReplyTo   string
	References  []string
	Labels      []string
	Attachments []AttachmentRef
}

// AttachmentRef describes an inbound attachment without loading its contents.
type AttachmentRef struct {
	AttachmentID string
	Filename     string
	ContentType  string
	SizeBytes    int64
}

// Transport is the email surface used by the device-client orchestrator.
type Transport interface {
	Poll(ctx context.Context) ([]Message, error)
	Thread(ctx context.Context, threadID string) ([]Message, error)
	Message(ctx context.Context, messageID string) (Message, error)
	Reply(ctx context.Context, messageID, text, idempotencyKey string) (string, error)
	ReplyReceipt(ctx context.Context, message Message) (string, bool, error)
	MarkProcessed(ctx context.Context, messageID string) error
	FetchAttachment(ctx context.Context, attachmentID string) ([]byte, error)
}
