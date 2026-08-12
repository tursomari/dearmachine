package client

import (
	"testing"
	"time"
)

func TestMessageOrderingUsesTimestampBeforeCreatedAt(t *testing.T) {
	base := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	messages := []Message{
		{MessageID: "later", Timestamp: base.Add(2 * time.Minute), CreatedAt: base},
		{MessageID: "earlier", Timestamp: base.Add(time.Minute), CreatedAt: base.Add(3 * time.Minute)},
	}

	sortMessages(messages)

	if messages[0].MessageID != "earlier" || messages[1].MessageID != "later" {
		t.Fatalf("message order = %q, %q; want earlier, later", messages[0].MessageID, messages[1].MessageID)
	}
}

func TestMessageOrderingFallsBackToCreatedAt(t *testing.T) {
	base := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	messages := []Message{
		{MessageID: "later", CreatedAt: base.Add(2 * time.Minute)},
		{MessageID: "earlier", CreatedAt: base.Add(time.Minute)},
	}

	sortMessages(messages)

	if messages[0].MessageID != "earlier" || messages[1].MessageID != "later" {
		t.Fatalf("message order = %q, %q; want earlier, later", messages[0].MessageID, messages[1].MessageID)
	}
}

func TestMessageOrderingKeepsEqualTimestampsStable(t *testing.T) {
	timestamp := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	messages := []Message{
		{MessageID: "first", Timestamp: timestamp},
		{MessageID: "second", Timestamp: timestamp},
		{MessageID: "third", Timestamp: timestamp},
	}

	sortMessages(messages)

	for index, want := range []string{"first", "second", "third"} {
		if messages[index].MessageID != want {
			t.Fatalf("message %d = %q, want %q", index, messages[index].MessageID, want)
		}
	}
}
