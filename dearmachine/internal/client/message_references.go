package client

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

const approvalReferencePrefix = "Dear Machine approval reference: "

var approvalReferenceLine = regexp.MustCompile(`^(?:>\s*)*Dear Machine approval reference: ([0-9a-f]{32})$`)

func approvalReferences(message Message) map[string]bool {
	body := message.RawBody
	if body == "" {
		body = message.Body
	}
	refs := make(map[string]bool)
	for _, line := range strings.Split(body, "\n") {
		if match := approvalReferenceLine.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			refs[match[1]] = true
		}
	}
	return refs
}

// Only the generated, unquoted final line identifies a stored prompt. The
// quoted guest preview may itself contain attacker-chosen reference lines.
func privateApprovalReference(message Message) string {
	body := message.RawBody
	if body == "" {
		body = message.Body
	}
	body = strings.TrimSpace(body)
	if at := strings.LastIndex(body, "\n"); at >= 0 {
		body = strings.TrimSpace(body[at+1:])
	}
	if !strings.HasPrefix(body, approvalReferencePrefix) {
		return ""
	}
	if match := approvalReferenceLine.FindStringSubmatch(body); match != nil {
		return match[1]
	}
	return ""
}

// Approval receipts are provider IDs. Some transports expose signed reply
// headers as Internet Message-IDs instead. Resolve those only against outbound
// records from the same scoped thread; never interpret an arbitrary signed
// header as an opaque provider ID.
func (a *App) participantControlReferences(ctx context.Context, message Message) (Message, error) {
	if message.RFCMessageID == "" || (message.InReplyTo == "" && len(message.References) == 0 && len(approvalReferences(message)) == 0) {
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
	tokens := make(map[string]string)
	owner, _ := canonicalMessageAddress(message.From)
	signedTokens := approvalReferences(message)
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
		// Sendmux's outbound relay rewrites Message-ID. A reference in the
		// authenticated owner's quoted body may identify only a private prompt
		// sent to that owner in this very inbox and thread. It confers no authority;
		// the caller still requires the controlling owner and a pending request.
		if message.authenticated && owner != "" && len(candidate.To) == 1 && len(candidate.CC) == 0 && len(candidate.BCC) == 0 && containsMessageAddress(candidate.To, owner) {
			token := privateApprovalReference(candidate)
			if token != "" && signedTokens[token] {
				if prior := tokens[token]; prior != "" && prior != candidate.MessageID {
					return Message{}, errors.New("ambiguous approval reference")
				}
				tokens[token] = candidate.MessageID
			}
		}
	}
	result := message
	result.InReplyTo = ids[strings.TrimSpace(message.InReplyTo)]
	result.References = nil
	for _, reference := range message.References {
		if id := ids[strings.TrimSpace(reference)]; id != "" {
			result.References = append(result.References, id)
		}
	}
	for _, id := range tokens {
		if !containsFold(result.References, id) {
			result.References = append(result.References, id)
		}
	}
	return result, nil
}
