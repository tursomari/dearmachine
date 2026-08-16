package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	agentmail "github.com/agentmail-to/agentmail-go"
	"github.com/agentmail-to/agentmail-go/option"
)

const pageSize = 100

var _ Transport = (*Mailbox)(nil)

// Mailbox calls AgentMail REST endpoints directly through the Go SDK.
type Mailbox struct {
	client             agentmail.Client
	inboxID            string
	attachmentMu       sync.RWMutex
	attachmentMessages map[string]string
}

func NewMailbox(client agentmail.Client, inboxID string) (*Mailbox, error) {
	if strings.TrimSpace(inboxID) == "" {
		return nil, fmt.Errorf("inbox ID is required")
	}
	return &Mailbox{
		client:             client,
		inboxID:            inboxID,
		attachmentMessages: make(map[string]string),
	}, nil
}

// NewAgentMailTransport constructs the production AgentMail adapter.
func NewAgentMailTransport(inboxID string) (*Mailbox, error) {
	return NewMailbox(agentmail.NewClient(), inboxID)
}

func (m *Mailbox) pollTarget() string {
	return m.inboxID
}

func (m *Mailbox) Poll(ctx context.Context) ([]Message, error) {
	var messages []Message
	pageToken := ""

	for {
		params := agentmail.InboxMessageListParams{
			Ascending: agentmail.Bool(true),
			Labels:    []string{"unread"},
			Limit:     agentmail.Int(pageSize),
		}
		if pageToken != "" {
			params.PageToken = agentmail.String(pageToken)
		}

		page, err := m.client.Inboxes.Messages.List(ctx, m.inboxID, params)
		if err != nil {
			return nil, fmt.Errorf("list AgentMail messages: %w", err)
		}

		for _, summary := range page.Messages {
			message, err := m.client.Inboxes.Messages.Get(
				ctx,
				summary.MessageID,
				agentmail.InboxMessageGetParams{InboxID: m.inboxID},
			)
			if err != nil {
				return nil, fmt.Errorf("get AgentMail message %s: %w", summary.MessageID, err)
			}
			messages = append(messages, m.normalize(*message))
		}

		pageToken = page.NextPageToken
		if pageToken == "" {
			break
		}
	}

	sortMessages(messages)
	return messages, nil
}

func (m *Mailbox) Thread(ctx context.Context, threadID string) ([]Message, error) {
	thread, err := m.client.Inboxes.Threads.Get(
		ctx,
		threadID,
		agentmail.InboxThreadGetParams{InboxID: m.inboxID},
	)
	if err != nil {
		return nil, fmt.Errorf("get AgentMail thread %s: %w", threadID, err)
	}
	messages := make([]Message, 0, len(thread.Messages))
	for _, message := range thread.Messages {
		messages = append(messages, m.normalize(message))
	}
	return messages, nil
}

func (m *Mailbox) Message(ctx context.Context, messageID string) (Message, error) {
	message, err := m.client.Inboxes.Messages.Get(
		ctx,
		messageID,
		agentmail.InboxMessageGetParams{InboxID: m.inboxID},
	)
	if err != nil {
		return Message{}, fmt.Errorf(
			"get AgentMail message %s: %w",
			messageID,
			err,
		)
	}
	return m.normalize(*message), nil
}

func (m *Mailbox) Reply(
	ctx context.Context,
	messageID string,
	payload ReplyPayload,
	idempotencyKey string,
) (string, error) {
	receipt, err := m.client.Inboxes.Messages.Reply(
		ctx,
		messageID,
		agentmail.InboxMessageReplyParams{
			InboxID: m.inboxID,
			Text:    agentmail.String(payload.Text),
		},
		option.WithHeader("Idempotency-Key", idempotencyKey),
	)
	if err != nil {
		return "", fmt.Errorf("reply to AgentMail message %s: %w", messageID, err)
	}
	if receipt.MessageID == "" {
		return "", fmt.Errorf("reply to AgentMail message %s returned no receipt", messageID)
	}
	return receipt.MessageID, nil
}

func (m *Mailbox) ReplyReceipt(
	ctx context.Context,
	message Message,
) (string, bool, error) {
	messages, err := m.Thread(ctx, message.ThreadID)
	if err != nil {
		return "", false, err
	}
	for _, candidate := range messages {
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

func (m *Mailbox) FetchAttachment(
	ctx context.Context,
	attachmentID string,
	maxBytes int64,
) ([]byte, error) {
	m.attachmentMu.RLock()
	messageID, ok := m.attachmentMessages[attachmentID]
	m.attachmentMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("fetch AgentMail attachment %s: message ID is unavailable", attachmentID)
	}

	attachment, err := m.client.Inboxes.Messages.GetAttachment(
		ctx,
		attachmentID,
		agentmail.InboxMessageGetAttachmentParams{
			InboxID:   m.inboxID,
			MessageID: messageID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("get AgentMail attachment %s: %w", attachmentID, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, attachment.DownloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("prepare AgentMail attachment %s download: %w", attachmentID, err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download AgentMail attachment %s: %w", attachmentID, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf(
			"download AgentMail attachment %s: unexpected status %s",
			attachmentID,
			response.Status,
		)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read AgentMail attachment %s: %w", attachmentID, err)
	}
	if int64(len(contents)) > maxBytes {
		return nil, fmt.Errorf(
			"download AgentMail attachment %s: %w",
			attachmentID,
			ErrAttachmentTooLarge,
		)
	}
	return contents, nil
}

func (m *Mailbox) MarkProcessed(ctx context.Context, messageID string) error {
	_, err := m.client.Inboxes.Messages.Update(
		ctx,
		messageID,
		agentmail.InboxMessageUpdateParams{
			InboxID: m.inboxID,
			UpdateMessage: agentmail.UpdateMessageParam{
				AddLabels: agentmail.UpdateMessageAddLabelsUnionParam{
					OfString: agentmail.String("read"),
				},
				RemoveLabels: agentmail.UpdateMessageRemoveLabelsUnionParam{
					OfString: agentmail.String("unread"),
				},
			},
		},
	)
	if err != nil {
		return fmt.Errorf("mark AgentMail message %s processed: %w", messageID, err)
	}
	return nil
}

func (m *Mailbox) normalize(message agentmail.Message) Message {
	body := message.ExtractedText
	if body == "" {
		body = message.Text
	}
	if body == "" {
		body = message.Preview
	}
	attachments := make([]AttachmentRef, 0, len(message.Attachments))
	for _, attachment := range message.Attachments {
		attachments = append(attachments, AttachmentRef{
			AttachmentID: attachment.AttachmentID,
			Filename:     attachment.Filename,
			ContentType:  attachment.ContentType,
			SizeBytes:    attachment.Size,
		})
	}
	normalized := Message{
		MessageID:   message.MessageID,
		ThreadID:    message.ThreadID,
		From:        message.From,
		To:          append([]string(nil), message.To...),
		Timestamp:   message.Timestamp,
		CreatedAt:   message.CreatedAt,
		Subject:     message.Subject,
		Body:        body,
		InReplyTo:   message.InReplyTo,
		References:  append([]string(nil), message.References...),
		Labels:      append([]string(nil), message.Labels...),
		Attachments: attachments,
	}
	if len(attachments) > 0 {
		m.attachmentMu.Lock()
		for _, attachment := range attachments {
			m.attachmentMessages[attachment.AttachmentID] = message.MessageID
		}
		m.attachmentMu.Unlock()
	}
	return normalized
}
