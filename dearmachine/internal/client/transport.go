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
	MessageID string
	ThreadID  string
	From      string
	To        []string
	Timestamp time.Time
	CreatedAt time.Time
	Subject   string
	Body      string
	// RawBody preserves the transport-normalized message before DearMachine
	// removes reply history and its own footer metadata. It is used when a
	// forwarded message must be delivered to the agent as ordinary content.
	RawBody                string
	InReplyTo              string
	References             []string
	ConversationReferences []string
	Labels                 []string
	Attachments            []AttachmentRef
	// Authority metadata is assigned locally after routing and is never read
	// from provider-controlled message fields.
	Authority              string
	ControllingParticipant string
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
	// To, CC, and BCC are an explicit envelope override. When To is non-empty,
	// adapters must address exactly these recipients while retaining the
	// provider thread selected by messageID. CC and BCC are intentionally
	// explicit so privacy-sensitive control mail cannot inherit recipients.
	To  []string
	CC  []string
	BCC []string
	// IncludeQuotedContent is false for Dear Machine generated mail. Keeping it
	// in the cross-provider contract makes the no-quotation privacy property
	// testable instead of relying on provider defaults.
	IncludeQuotedContent bool
}

// Transport is the email surface used by the dearmachine orchestrator.
type Transport interface {
	Poll(ctx context.Context) ([]Message, error)
	Thread(ctx context.Context, threadID string) ([]Message, error)
	Message(ctx context.Context, messageID string) (Message, error)
	Reply(ctx context.Context, messageID string, payload ReplyPayload, idempotencyKey string) (string, error)
	// ReplyReceipt finds the provider receipt for a reply to message. Recipient
	// is empty for the normal reply target and explicit for a private override.
	ReplyReceipt(ctx context.Context, message Message, recipient string) (string, bool, error)
	MarkProcessed(ctx context.Context, messageID string) error
	FetchAttachment(ctx context.Context, attachmentID string, maxBytes int64) ([]byte, error)
}

func replyReceiptRecipient(message Message, explicit string) string {
	if value := strings.TrimSpace(explicit); value != "" {
		return value
	}
	return message.From
}

func containsMessageAddress(values []string, target string) bool {
	canonical, err := canonicalMessageAddress(target)
	if err != nil {
		return containsFold(values, target)
	}
	return containsCanonicalAddress(values, canonical)
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
