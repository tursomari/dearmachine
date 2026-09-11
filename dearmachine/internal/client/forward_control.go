package client

import (
	"fmt"
	"strconv"
	"strings"
)

type forwardControlKind int

const (
	forwardControlInvalid forwardControlKind = iota
	forwardControlYes
	forwardControlNo
	forwardControlCancel
	forwardControlSelection
)

type forwardControl struct {
	Kind      forwardControlKind
	Selection int
}

func parseForwardControl(body string) forwardControl {
	value := strings.TrimSpace(body)
	if value == "" {
		return forwardControl{}
	}
	if strings.ContainsAny(value[len(value)-1:], ".,!") {
		value = strings.TrimSpace(value[:len(value)-1])
	}
	switch strings.ToLower(value) {
	case "yes":
		return forwardControl{Kind: forwardControlYes}
	case "no":
		return forwardControl{Kind: forwardControlNo}
	case "cancel":
		return forwardControl{Kind: forwardControlCancel}
	}
	selection, err := strconv.Atoi(value)
	if err == nil && selection > 0 {
		return forwardControl{Kind: forwardControlSelection, Selection: selection}
	}
	return forwardControl{}
}

func initialForwardPrompt(request ForwardRequest) string {
	if request.State == forwardAwaitingSelection {
		var prompt strings.Builder
		prompt.WriteString("I found more than one DearMachine session in the forwarded email. Reply with only the number of the session you mean:\n\n")
		for index, candidate := range request.CandidateIDs {
			fmt.Fprintf(&prompt, "%d. %s\n", index+1, candidate)
		}
		prompt.WriteString("\nYou can also reply with only No to process the email normally, or Cancel to discard it.")
		return prompt.String()
	}
	return forwardConfirmationPrompt(request.SelectedID)
}

func forwardConfirmationPrompt(sessionID string) string {
	return fmt.Sprintf(
		"I found DearMachine session %s in the forwarded email. Do you want to continue from it?\n\n"+
			"Reply with only:\n"+
			"Yes — fork the session's current state and process your request there.\n"+
			"No — process the complete forwarded email normally without using that session.\n"+
			"Cancel — discard this request.\n\n"+
			"If you forwarded an older message, Yes uses the session's current state, which may include newer turns.",
		sessionID,
	)
}

func invalidForwardPrompt(request ForwardRequest) string {
	if request.State == forwardAwaitingSelection {
		return initialForwardPrompt(request) +
			"\n\nFor example, reply with exactly 1, No., or Cancel! Do not add other text."
	}
	return forwardConfirmationPrompt(request.SelectedID) +
		"\n\nFor example, reply with exactly Yes!, No., or Cancel. Do not add other text."
}

func authoredControlBody(message Message) string {
	body := message.RawBody
	if body == "" {
		body = message.Body
	}
	body = stripReplyHistory(body)
	body, _ = stripConversationFooters(body)
	return strings.TrimSpace(body)
}
