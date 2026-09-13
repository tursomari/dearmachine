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
