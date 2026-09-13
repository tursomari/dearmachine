package client

import (
	"strings"
	"testing"
)

func TestParticipantControlWithAgentMailBranding(t *testing.T) {
	for _, choice := range []string{"Yes", "No", "Other"} {
		for _, newline := range []string{"\n", "\r\n"} {
			body := choice + "\n\n--\nSent via AgentMail\n\nOn Mon, Jan 2, 2006 at 1:29 AM UTC Owner <owner@example.test> wrote:\n> Yes\n> No\n"
			message := Message{RawBody: strings.ReplaceAll(body, "\n", newline), Body: "untrusted extracted text"}
			if got := authoredControlBody(message); got != choice {
				t.Fatalf("authored control = %q, want %q", got, choice)
			}
			if parseParticipantControl(authoredControlBody(message)) != parseParticipantControl(choice) {
				t.Fatal("provider branding changed the owner's decision")
			}
		}
	}
}

func TestParticipantControlCaseInsensitive(t *testing.T) {
	for _, choice := range []struct {
		text string
		want participantControl
	}{
		{"Yes", participantControlYes},
		{"No", participantControlNo},
		{"Other", participantControlOther},
	} {
		for _, body := range []string{choice.text, strings.ToLower(choice.text), strings.ToUpper(choice.text), " \t" + choice.text + "\r\n"} {
			if got := parseParticipantControl(body); got != choice.want {
				t.Fatalf("decision for %q = %v, want %v", body, got, choice.want)
			}
		}
	}
	if got := parseParticipantControl("oThEr"); got != participantControlOther {
		t.Fatalf("mixed-case choice = %v, want Other", got)
	}
	for _, body := range []string{"yes please", "YES\nNO", "no, run this instead", "other instruction", "yes."} {
		if parseParticipantControl(body) != participantControlInvalid {
			t.Fatalf("additional text accepted as a decision: %q", body)
		}
	}
}
