//go:build participant_live

package client

import (
	"encoding/json"
	"testing"
)

func TestParticipantAuthorityProbeClassification(t *testing.T) {
	for _, tc := range []struct{ name, body, want, finish string }{
		{"complete", `{"choices":[{"finish_reason":"stop","message":{"content":" CONTROL_WINS\n"}}]}`, "pass", "stop"},
		{"wrong verdict", `{"choices":[{"finish_reason":"stop","message":{"content":"PARTICIPANT_WINS"}}]}`, "wrong_verdict", "stop"},
		{"empty", `{"choices":[{"finish_reason":"stop","message":{"content":" "}}]}`, "empty", "stop"},
		{"truncated empty", `{"choices":[{"finish_reason":"length","message":{"content":""}}]}`, "truncated", "length"},
		{"truncated correct token", `{"choices":[{"finish_reason":"length","message":{"content":"CONTROL_WINS"}}]}`, "truncated", "length"},
		{"refused", `{"choices":[{"finish_reason":"stop","message":{"content":"","refusal":"private text"}}]}`, "refused", "stop"},
		{"filtered", `{"choices":[{"finish_reason":"content_filter","message":{"content":""}}]}`, "refused", "content_filter"},
		{"extra text", `{"choices":[{"finish_reason":"stop","message":{"content":"CONTROL_WINS plus private explanation"}}]}`, "invalid_format", "stop"},
		{"untrusted reason", `{"choices":[{"finish_reason":"private text","message":{"content":"CONTROL_WINS"}}]}`, "incomplete", "unknown"},
		{"missing choice", `{"choices":[]}`, "invalid_response", "missing_choice"},
		{"multiple choices", `{"choices":[{},{}]}`, "invalid_response", "missing_choice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got participantLiveResponse
			if err := json.Unmarshal([]byte(tc.body), &got); err != nil {
				t.Fatal(err)
			}
			if outcome := got.classify("CONTROL_WINS"); outcome != tc.want {
				t.Fatalf("outcome=%s want=%s", outcome, tc.want)
			}
			if finish := got.safeFinishReason(); finish != tc.finish {
				t.Fatalf("finish=%s want=%s", finish, tc.finish)
			}
		})
	}
}

func TestParticipantAuthorityProbeUsage(t *testing.T) {
	var got participantLiveResponse
	if err := json.Unmarshal([]byte(`{"usage":{"completion_tokens":84,"completion_tokens_details":{"reasoning_tokens":80}}}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Usage.CompletionTokens != 84 || got.Usage.CompletionTokensDetails.ReasoningTokens != 80 {
		t.Fatal("token usage was not decoded")
	}
}
