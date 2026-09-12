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
	CC        []string
	BCC       []string
	Delivery  MessageDelivery
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

// DeliveryRole records which RFC recipient field contained the provider inbox.
// It is intentionally separate from To, CC, and BCC: those fields preserve the
// message headers, while Delivery identifies the inbox through which the
// provider returned the message.
type DeliveryRole string

const (
	DeliveryRoleUnknown DeliveryRole = ""
	DeliveryRoleTo      DeliveryRole = "to"
	DeliveryRoleCC      DeliveryRole = "cc"
	DeliveryRoleBCC     DeliveryRole = "bcc"
)

// MessageReadState avoids treating an absent provider read-state signal as
// equivalent to a message that the provider explicitly reports as read.
type MessageReadState string

const (
	MessageReadStateUnknown MessageReadState = ""
	MessageReadStateRead    MessageReadState = "read"
	MessageReadStateUnread  MessageReadState = "unread"
)

// MessageDelivery is trusted adapter metadata. InboxID is the exact provider
// inbox used for the API request; Recipient is its canonical email address when
// the provider exposes it. Neither value is reconstructed from message headers.
type MessageDelivery struct {
	InboxID   string
	Recipient string
	Role      DeliveryRole
	ReadState MessageReadState
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

func normalizeMessageDelivery(
	inboxID, recipient string,
	to, cc, bcc []string,
	providerRole string,
	readState MessageReadState,
) MessageDelivery {
	delivery := MessageDelivery{
		InboxID:   strings.TrimSpace(inboxID),
		Recipient: canonicalAddressOrLower(recipient),
		Role:      normalizedDeliveryRole(providerRole),
		ReadState: readState,
	}
	if delivery.Role == DeliveryRoleUnknown && delivery.Recipient != "" {
		switch {
		case containsMessageAddress(to, delivery.Recipient):
			delivery.Role = DeliveryRoleTo
		case containsMessageAddress(cc, delivery.Recipient):
			delivery.Role = DeliveryRoleCC
		case containsMessageAddress(bcc, delivery.Recipient):
			delivery.Role = DeliveryRoleBCC
		}
	}
	return delivery
}

func normalizedDeliveryRole(value string) DeliveryRole {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(DeliveryRoleTo):
		return DeliveryRoleTo
	case string(DeliveryRoleCC):
		return DeliveryRoleCC
	case string(DeliveryRoleBCC):
		return DeliveryRoleBCC
	default:
		return DeliveryRoleUnknown
	}
}

func canonicalAddressOrLower(value string) string {
	if canonical, err := canonicalMessageAddress(value); err == nil {
		return canonical
	}
	return strings.ToLower(strings.TrimSpace(value))
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

// finalizePolledMessages is the common eligibility seam for every provider.
// Adapters may use provider-side unread filters as an optimization, but the
// provider-neutral delivery state remains the final authority. Sent messages
// stay visible so the inbox router can acknowledge the mailbox's own output.
func finalizePolledMessages(messages []Message) []Message {
	seen := make(map[string]struct{}, len(messages))
	eligible := make([]Message, 0, len(messages))
	for _, message := range messages {
		if message.Delivery.ReadState != MessageReadStateUnread &&
			!containsFold(message.Labels, "sent") {
			continue
		}
		if _, duplicate := seen[message.MessageID]; duplicate {
			continue
		}
		seen[message.MessageID] = struct{}{}
		eligible = append(eligible, message)
	}
	sortMessages(eligible)
	return eligible
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
