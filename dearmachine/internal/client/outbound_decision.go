package client

import (
	"context"
	"errors"
	"slices"
	"strings"
)

func outboundTokens(message Message) map[string]bool {
	body := message.RawBody
	if body == "" {
		body = message.Body
	}
	tokens := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		for strings.HasPrefix(line, ">") {
			line = strings.TrimSpace(strings.TrimPrefix(line, ">"))
		}
		if strings.HasPrefix(line, outboundReferencePrefix) {
			tokens[strings.TrimPrefix(line, outboundReferencePrefix)] = true
		}
	}
	return tokens
}
func (a *App) handleOutboundDecision(ctx context.Context, message Message) (bool, error) {
	e, ok := a.transport.(*pairEndpoint)
	if !ok || containsFold(message.Labels, "sent") || canonicalAddressOrLower(message.From) == e.router.inbox.Address {
		return false, nil
	}
	a.store.outboundMu.Lock()
	defer a.store.outboundMu.Unlock()
	records, err := a.store.queryOutbound(`SELECT reference FROM outbound_approvals ORDER BY send_key, revision`)
	if err != nil {
		return false, err
	}
	owner := e.controllingParticipant()
	inScope := func(o outboundApproval) bool {
		return message.ThreadID != "" && o.Message.ThreadID == message.ThreadID &&
			o.PairID == e.pairID && o.InboxID == guestInboxKey(e.router.inbox) && o.Owner == owner
	}
	hasScope := false
	for _, o := range records {
		hasScope = hasScope || inScope(o)
	}
	tokens := outboundTokens(message)
	// An instruction approval reference never resolves to an outbound decision.
	control, err := a.participantControlReferences(ctx, message)
	if err != nil {
		return false, err
	}
	if _, _, found, err := a.store.ParticipantRequestForControl(control); err != nil {
		return false, err
	} else if found {
		return false, nil
	}
	var matches []int
	attempted := len(tokens) > 0
	references := message.References
	if message.InReplyTo != "" {
		references = []string{message.InReplyTo}
	}
	for i, o := range records {
		direct := o.PreviewID != "" && (slices.Contains(references, o.PreviewID) || o.PreviewRFCID != "" && slices.Contains(references, o.PreviewRFCID))
		token := tokens[o.Token]
		if !direct && !token && message.RFCMessageID != "" && len(references) > 0 && o.PreviewID != "" && o.Message.ThreadID == message.ThreadID {
			thread, err := e.router.raw.Thread(ctx, message.ThreadID)
			if err != nil {
				a.logger.Printf("outbound decision deferred: provider references unavailable")
				return true, nil
			}
			for _, m := range thread {
				if m.MessageID == o.PreviewID && m.ThreadID == o.Message.ThreadID && containsFold(m.Labels, "sent") && canonicalAddressOrLower(m.From) == e.router.inbox.Address && sameRecipientSet(m.To, []string{o.Owner}) && len(m.CC) == 0 && len(m.BCC) == 0 && m.RFCMessageID != "" && slices.Contains(references, m.RFCMessageID) {
					direct = true
				}
			}
		}
		if direct || token {
			attempted = true
			matches = append(matches, i)
		}
	}
	if !attempted {
		decision := strings.ToLower(strings.TrimSpace(authoredControlBody(message)))
		if e.isControllingParticipant(message) && len(references) > 0 && (decision == "yes" || decision == "no") {
			for _, o := range records {
				if !inScope(o) {
					continue
				}
				if o.State == "preview_sending" {
					a.logger.Printf("outbound decision deferred: preview issuance unresolved")
					return true, nil
				}
				if o.State == "prepared" || o.State == "pending" || o.State == "approved" {
					attempted = true
				}
			}
		}
		if !attempted {
			return false, nil
		}
	}
	seen, err := a.store.Seen(message.MessageID)
	if err != nil {
		return true, err
	}
	if seen {
		return true, a.transport.MarkProcessed(ctx, message.MessageID)
	}
	notice := "To approve a pending reply, answer its private preview. This message was not sent to Machtiani."
	canNotify := hasScope && e.isControllingParticipant(message)
	for _, i := range matches {
		canNotify = canNotify && inScope(records[i])
	}
	if len(matches) > 1 {
		notice = "That reply matched more than one preview; reply directly to one."
	}
	if len(matches) == 1 && canNotify {
		ref := records[matches[0]]
		full, err := a.store.queryOutbound(`SELECT record FROM outbound_approvals WHERE send_key=? AND revision=?`, ref.Key, ref.Revision)
		if err != nil {
			return true, err
		}
		if len(full) != 1 {
			return true, errors.New("outbound approval record missing")
		}
		o := &full[0]
		if inScope(*o) {
			// Reference allocation is not issuance. Recovery owns reconciliation;
			// leave this decision unseen until it establishes a preview receipt.
			// Do not advance here: transport or record failures must not interrupt
			// polling. Pre/post recovery persists holds and notifies independently.
			if o.State == "preview_sending" {
				a.logger.Printf("outbound decision deferred: preview issuance unresolved")
				return true, nil
			}
			switch {
			case o.State == "superseded":
				notice = "That preview was replaced; reply to the newer preview."
			case o.State == "rejected":
				notice = "That reply was already rejected; this decision did not send it."
			case o.State == "sent":
				notice = "That reply was already sent; this decision did not send it again."
			case o.State == "approved" || o.State == "sending":
				notice = "That reply was already approved; this decision did not start another send."
			default:
				notice = "Not recognised — reply with only yes or no to the preview."
			}
			// A crash after saving the decision but before marking the message
			// processed must not turn the original successful decision into feedback.
			if o.DecisionID == message.MessageID {
				notice = ""
			}
			decision := strings.ToLower(strings.TrimSpace(authoredControlBody(message)))
			if o.State == "pending" && o.PreviewID != "" && (decision == "yes" || decision == "no") {
				o.DecisionID, o.Decision = message.MessageID, decision
				o.DecisionSender, o.DecisionAuthenticated = canonicalAddressOrLower(message.From), message.authenticated
				o.State = "approved"
				if decision == "no" {
					o.State = "rejected"
				}
				notice = ""
				if err := a.store.saveOutbound(*o); err != nil {
					return true, err
				}
			}
		}
	}
	if canNotify && notice != "" {
		key := outboundNoticeKey("control", message.MessageID)
		// The helper durably records the attempt before provider I/O. Provider
		// failures return nil; database failures must leave this control unseen.
		if err := e.sendOutboundNotice(ctx, a.store, message, owner, key, notice); err != nil {
			return true, err
		}
	}
	if err := a.store.RecordControlMessage(message.MessageID, message.ThreadID, ""); err != nil {
		return true, err
	}
	return true, a.transport.MarkProcessed(ctx, message.MessageID)
}
