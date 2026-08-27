package transports

import (
	"context"
	"fmt"
	"net/mail"
	"strings"
	"sync"

	"github.com/dearmachine/dearmachine/internal/client"
)

// AllowList is the single, transport-independent paired-address policy.
type AllowList struct{ addresses map[string]struct{} }

// ParseAllowList parses the --allow / DEARMACHINE_ALLOW RFC 5322 address list.
func ParseAllowList(value string) (AllowList, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return AllowList{}, fmt.Errorf("allow list is required")
	}
	addresses, err := mail.ParseAddressList(value)
	if err != nil {
		return AllowList{}, fmt.Errorf("allow list must be a comma-separated email address list: %w", err)
	}
	allow := AllowList{addresses: make(map[string]struct{}, len(addresses))}
	for _, address := range addresses {
		canonical := strings.ToLower(strings.TrimSpace(address.Address))
		if canonical == "" {
			return AllowList{}, fmt.Errorf("allow list contains an empty address")
		}
		allow.addresses[canonical] = struct{}{}
	}
	return allow, nil
}

func (allow AllowList) authorized(message client.Message) bool {
	if len(allow.addresses) == 0 || len(message.To) == 0 {
		return false
	}
	if _, ok := allow.addresses[strings.ToLower(strings.TrimSpace(message.From))]; !ok {
		return false
	}
	for _, raw := range message.To {
		if _, ok := allow.addresses[strings.ToLower(strings.TrimSpace(raw))]; !ok {
			return false
		}
	}
	return true
}

type allowlistTransport struct {
	inner       client.Transport
	allow       AllowList
	mu          sync.Mutex
	attachments map[string]struct{}
}

func newAllowlistTransport(inner client.Transport, allow AllowList) (client.Transport, error) {
	if len(allow.addresses) == 0 {
		return nil, fmt.Errorf("allow list is required")
	}
	return &allowlistTransport{inner: inner, allow: allow, attachments: make(map[string]struct{})}, nil
}

func (transport *allowlistTransport) record(message client.Message) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	for _, attachment := range message.Attachments {
		transport.attachments[attachment.AttachmentID] = struct{}{}
	}
}
func (transport *allowlistTransport) permitted(message client.Message) bool {
	if !transport.allow.authorized(message) {
		return false
	}
	transport.record(message)
	return true
}
func (transport *allowlistTransport) Poll(ctx context.Context) ([]client.Message, error) {
	messages, err := transport.inner.Poll(ctx)
	if err != nil {
		return nil, err
	}
	return transport.filter(messages), nil
}
func (transport *allowlistTransport) Thread(ctx context.Context, id string) ([]client.Message, error) {
	messages, err := transport.inner.Thread(ctx, id)
	if err != nil {
		return nil, err
	}
	return transport.filter(messages), nil
}
func (transport *allowlistTransport) filter(messages []client.Message) []client.Message {
	allowed := make([]client.Message, 0, len(messages))
	for _, message := range messages {
		if transport.permitted(message) {
			allowed = append(allowed, message)
		}
	}
	return allowed
}
func (transport *allowlistTransport) Message(ctx context.Context, id string) (client.Message, error) {
	message, err := transport.inner.Message(ctx, id)
	if err != nil {
		return client.Message{}, err
	}
	if !transport.permitted(message) {
		return client.Message{}, fmt.Errorf("message %s is outside the configured allow list", id)
	}
	return message, nil
}
func (transport *allowlistTransport) authorizeID(ctx context.Context, id string) error {
	_, err := transport.Message(ctx, id)
	return err
}
func (transport *allowlistTransport) Reply(ctx context.Context, id string, payload client.ReplyPayload, key string) (string, error) {
	if err := transport.authorizeID(ctx, id); err != nil {
		return "", err
	}
	return transport.inner.Reply(ctx, id, payload, key)
}
func (transport *allowlistTransport) MarkProcessed(ctx context.Context, id string) error {
	if err := transport.authorizeID(ctx, id); err != nil {
		return err
	}
	return transport.inner.MarkProcessed(ctx, id)
}
func (transport *allowlistTransport) ReplyReceipt(ctx context.Context, message client.Message) (string, bool, error) {
	if err := transport.authorizeID(ctx, message.MessageID); err != nil {
		return "", false, err
	}
	return transport.inner.ReplyReceipt(ctx, message)
}
func (transport *allowlistTransport) FetchAttachment(ctx context.Context, id string, max int64) ([]byte, error) {
	transport.mu.Lock()
	_, ok := transport.attachments[id]
	transport.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("attachment %s is not associated with an authorized message", id)
	}
	return transport.inner.FetchAttachment(ctx, id, max)
}
