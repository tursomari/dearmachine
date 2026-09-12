package client

import (
	"context"
	"errors"
	"strings"
)

// Approval receipts are provider IDs. Some transports expose signed reply
// headers as Internet Message-IDs instead. Resolve those only against outbound
// records from the same scoped thread; never interpret an arbitrary signed
// header as an opaque provider ID.
func (a *App) participantControlReferences(ctx context.Context, message Message) (Message, error) {
	if message.RFCMessageID == "" || (message.InReplyTo == "" && len(message.References) == 0) {
		return message, nil
	}
	transport := a.transport
	if endpoint, ok := transport.(*pairEndpoint); ok {
		// Read provider metadata only. Other inbound thread entries are not needed
		// for this mapping, and may have unsupported MIME content.
		transport = endpoint.router.raw
	}
	thread, err := transport.Thread(ctx, message.ThreadID)
	if err != nil {
		return Message{}, err
	}
	return resolveControlReferences(message, thread)
}

func resolveControlReferences(message Message, thread []Message) (Message, error) {
	ids := make(map[string]string)
	for _, candidate := range thread {
		if candidate.ThreadID != message.ThreadID || candidate.Delivery.InboxID != message.Delivery.InboxID || !containsFold(candidate.Labels, "sent") || candidate.RFCMessageID == "" || candidate.MessageID == "" {
			continue
		}
		if prior, exists := ids[candidate.RFCMessageID]; exists && prior != candidate.MessageID {
			return Message{}, errors.New("ambiguous outbound email reference")
		}
		sender, err := canonicalMessageAddress(candidate.From)
		if err != nil || !strings.EqualFold(sender, message.Delivery.Recipient) {
			continue
		}
		ids[candidate.RFCMessageID] = candidate.MessageID
	}
	result := message
	result.InReplyTo = ids[strings.TrimSpace(message.InReplyTo)]
	result.References = nil
	for _, reference := range message.References {
		if id := ids[strings.TrimSpace(reference)]; id != "" {
			result.References = append(result.References, id)
		}
	}
	return result, nil
}
