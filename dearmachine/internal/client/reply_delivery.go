package client

import (
	"context"
	"errors"
)

// ReplyDelivery distinguishes durable submission from confirmed delivery.
// Unknown is deliberate: a Sent copy or SMTP 250 Queued is not delivery proof.
type ReplyDelivery struct {
	MessageID    string              `json:"message_id"`
	SubmissionID string              `json:"submission_id,omitempty"`
	Status       string              `json:"status"`
	Recipients   []RecipientDelivery `json:"recipients"`
}
type RecipientDelivery struct {
	Address string `json:"address"`
	Status  string `json:"status"`
}
type replyDeliveryInspector interface {
	ReplyDelivery(context.Context, string) (ReplyDelivery, error)
}

func InspectReplyDelivery(ctx context.Context, transport Transport, messageID string) (ReplyDelivery, error) {
	if retry, ok := transport.(*retryTransport); ok {
		transport = retry.Transport
	}
	if inspector, ok := transport.(replyDeliveryInspector); ok {
		return inspector.ReplyDelivery(ctx, messageID)
	}
	return ReplyDelivery{}, errors.New("delivery inspection is not supported by this transport")
}
func (transport *SendmuxTransport) ReplyDelivery(ctx context.Context, messageID string) (ReplyDelivery, error) {
	mailbox, err := transport.mailbox(ctx)
	if err != nil {
		return ReplyDelivery{}, err
	}
	sender, ok := transport.outbound.(*sendmuxJMAPSender)
	if !ok {
		return ReplyDelivery{}, errors.New("Sendmux mailbox submission inspection is unavailable")
	}
	return sender.delivery(ctx, mailbox.Email, messageID)
}
func (s *sendmuxJMAPSender) delivery(ctx context.Context, from, messageID string) (ReplyDelivery, error) {
	result := ReplyDelivery{MessageID: messageID, Status: "no submission found", Recipients: []RecipientDelivery{}}
	if messageID == "" {
		return result, errors.New("outbound message ID is required")
	}
	session, err := s.session(ctx, from)
	if err != nil {
		return result, err
	}
	var found struct{ IDs []string }
	if err = s.call(ctx, from, session, "EmailSubmission/query", map[string]any{"filter": map[string]any{"emailIds": []string{messageID}}, "limit": 2}, &found); err != nil {
		return result, err
	}
	if len(found.IDs) == 0 {
		return result, nil
	}
	if len(found.IDs) > 1 {
		return result, errors.New("ambiguous Sendmux submissions")
	}
	var records struct {
		List []struct {
			ID, EmailID, UndoStatus string
			DeliveryStatus          map[string]struct{ Delivered string }
		}
	}
	if err = s.call(ctx, from, session, "EmailSubmission/get", map[string]any{"ids": found.IDs}, &records); err != nil {
		return result, err
	}
	if len(records.List) != 1 || records.List[0].ID != found.IDs[0] || records.List[0].EmailID != messageID {
		return result, errors.New("mismatched Sendmux delivery receipt")
	}
	record := records.List[0]
	result.SubmissionID = record.ID
	result.Status = "submitted; delivery unconfirmed"
	if record.UndoStatus == "pending" {
		result.Status = "queued; delivery unconfirmed"
	}
	if record.UndoStatus == "canceled" {
		result.Status = "canceled"
	}
	for address, status := range record.DeliveryStatus {
		recipient := RecipientDelivery{Address: address, Status: "unconfirmed"}
		switch status.Delivered {
		case "yes":
			recipient.Status = "delivered"
		case "no":
			recipient.Status = "failed"
		}
		result.Recipients = append(result.Recipients, recipient)
	}
	return result, nil
}
