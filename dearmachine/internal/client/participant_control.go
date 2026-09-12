package client

import (
	"fmt"
	"strings"
)

type participantTransport interface {
	controllingParticipant() string
	isControllingParticipant(Message) bool
}

func (a *App) threadGuestFlow() bool {
	_, ok := a.transport.(*pairEndpoint)
	return ok
}

func participantBoundary(transport Transport, message Message) (string, bool, bool) {
	boundary, ok := transport.(participantTransport)
	if !ok {
		return "", true, false
	}
	return boundary.controllingParticipant(), boundary.isControllingParticipant(message), true
}

type participantControl int

const (
	participantControlInvalid participantControl = iota
	participantControlYes
	participantControlNo
	participantControlOther
)

func parseParticipantControl(body string) participantControl {
	value := strings.TrimSpace(body)
	switch value {
	case "Yes":
		return participantControlYes
	case "No":
		return participantControlNo
	case "Other":
		return participantControlOther
	default:
		return participantControlInvalid
	}
}

type participantTrustControl int

const (
	participantTrustControlNone participantTrustControl = iota
	participantTrustControlGrant
	participantTrustControlRevoke
)

type parsedParticipantTrustControl struct {
	Kind      participantTrustControl
	Address   string
	Attempted bool
}

func parseParticipantTrustControl(body string) parsedParticipantTrustControl {
	fields := strings.Fields(strings.TrimSpace(body))
	if len(fields) == 0 {
		return parsedParticipantTrustControl{}
	}
	var kind participantTrustControl
	var address string
	switch {
	case fields[0] == "Trust":
		kind = participantTrustControlGrant
		if len(fields) == 2 {
			address = fields[1]
		}
	case len(fields) >= 2 && fields[0] == "Revoke" && fields[1] == "trust":
		kind = participantTrustControlRevoke
		if len(fields) == 3 {
			address = fields[2]
		}
	default:
		return parsedParticipantTrustControl{}
	}
	parsed := parsedParticipantTrustControl{Kind: kind, Attempted: true}
	if address == "" {
		return parsed
	}
	canonical, err := canonicalMessageAddress(address)
	if err == nil {
		parsed.Address = canonical
	}
	return parsed
}

func participantPrivatePayload(text, controller string) ReplyPayload {
	return ReplyPayload{
		Text:                 text,
		To:                   []string{controller},
		CC:                   []string{},
		BCC:                  []string{},
		IncludeQuotedContent: false,
	}
}

func participantApprovalPrompt(request ParticipantRequest) string {
	return fmt.Sprintf(
		"A non-paired participant (%s) sent an instruction in this thread. The instruction is held privately and has not been shown to the agent.\n\n"+
			"Dear Machine's interpretation: this is one lower-authority participant request. Planned action: run only this frozen provider message as one agent turn if you approve it.\n\n"+
			"Reply with only:\n"+
			"Yes — approve this one instruction.\n"+
			"No — discard this instruction.\n"+
			"Other — provide your own replacement instruction.\n\n"+
			"This decision applies only to provider message %s.",
		request.ParticipantAddress,
		request.RequestMessageID,
	)
}

func participantAdmissionPrompt(request ParticipantRequest) string {
	return fmt.Sprintf(
		"A non-paired sender (%s) requested admission to an existing controlled conversation. Their message is held privately and has not been shown to the agent.\n\n"+
			"Reply with only:\n"+
			"Yes — admit this participant. Admission does not approve their held instruction; a separate instruction confirmation follows.\n"+
			"No — reject admission and discard the held instruction.\n"+
			"Other — do not admit them and provide your own replacement instruction.\n\n"+
			"This decision applies only to participant %s and provider message %s. Admission does not create a pairing, increase authority, or grant trust.",
		request.ParticipantAddress,
		request.ParticipantAddress,
		request.RequestMessageID,
	)
}

func invalidParticipantAdmissionPrompt(request ParticipantRequest) string {
	return participantAdmissionPrompt(request) +
		"\n\nReply with exactly Yes, No, or Other and no additional text."
}

func unauthorizedTrustPrompt(address string) string {
	if address == "" {
		address = "the requested address"
	}
	return fmt.Sprintf(
		"A non-controlling participant attempted to change trust for %s. The attempt was rejected. Only the controlling participant may grant or revoke trust.",
		address,
	)
}

func trustControlResultPrompt(control parsedParticipantTrustControl, admitted bool) string {
	if control.Address == "" {
		return "Trust control was rejected. Use exactly `Trust participant@example.com` or `Revoke trust participant@example.com`."
	}
	if !admitted {
		return fmt.Sprintf("Trust control was rejected because %s is not admitted. Trust never grants admission or creates a pairing.", control.Address)
	}
	if control.Kind == participantTrustControlGrant {
		return fmt.Sprintf("Trust granted for %s. Routine instruction confirmations are disabled, but authority, pairing, routing, and scheduling priority are unchanged.", control.Address)
	}
	return fmt.Sprintf("Trust revoked for %s. Routine instruction confirmations are restored; authority, pairing, routing, and scheduling priority are unchanged.", control.Address)
}

func invalidParticipantApprovalPrompt(request ParticipantRequest) string {
	return participantApprovalPrompt(request) +
		"\n\nReply with exactly Yes, No, or Other and no additional text."
}

func participantReplacementPrompt(request ParticipantRequest) string {
	return fmt.Sprintf(
		"Reply with your replacement instruction for provider message %s. Only your newly authored text will be sent to the agent; quoted thread content and the participant's held instruction will not be included.",
		request.RequestMessageID,
	)
}

func participantReplacementBody(message Message) string {
	body := authoredControlBody(message)
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), ">") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}
