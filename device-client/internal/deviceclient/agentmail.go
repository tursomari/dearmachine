package deviceclient

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	agentmail "github.com/agentmail-to/agentmail-go"
	"github.com/agentmail-to/agentmail-go/option"
)

const pageSize = 100

// Mailbox calls AgentMail REST endpoints directly through the Go SDK.
type Mailbox struct {
	client  agentmail.Client
	inboxID string
}

func NewMailbox(client agentmail.Client, inboxID string) (*Mailbox, error) {
	if strings.TrimSpace(inboxID) == "" {
		return nil, fmt.Errorf("inbox ID is required")
	}
	return &Mailbox{client: client, inboxID: inboxID}, nil
}

func (m *Mailbox) Poll(ctx context.Context) ([]agentmail.Message, error) {
	var messages []agentmail.Message
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
			messages = append(messages, *message)
		}

		pageToken = page.NextPageToken
		if pageToken == "" {
			break
		}
	}

	sort.SliceStable(messages, func(i, j int) bool {
		return messageTime(messages[i]).Before(messageTime(messages[j]))
	})
	return messages, nil
}

func (m *Mailbox) Thread(ctx context.Context, threadID string) ([]agentmail.Message, error) {
	thread, err := m.client.Inboxes.Threads.Get(
		ctx,
		threadID,
		agentmail.InboxThreadGetParams{InboxID: m.inboxID},
	)
	if err != nil {
		return nil, fmt.Errorf("get AgentMail thread %s: %w", threadID, err)
	}
	return thread.Messages, nil
}

func (m *Mailbox) Reply(ctx context.Context, messageID, text, idempotencyKey string) error {
	_, err := m.client.Inboxes.Messages.Reply(
		ctx,
		messageID,
		agentmail.InboxMessageReplyParams{
			InboxID: m.inboxID,
			Text:    agentmail.String(text),
		},
		option.WithHeader("Idempotency-Key", idempotencyKey),
	)
	if err != nil {
		return fmt.Errorf("reply to AgentMail message %s: %w", messageID, err)
	}
	return nil
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

func messageTime(message agentmail.Message) time.Time {
	if !message.Timestamp.IsZero() {
		return message.Timestamp
	}
	return message.CreatedAt
}

func messageBody(message agentmail.Message) string {
	if message.Text != "" {
		return message.Text
	}
	if message.ExtractedText != "" {
		return message.ExtractedText
	}
	return message.Preview
}
