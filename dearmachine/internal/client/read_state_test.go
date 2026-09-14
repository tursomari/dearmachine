package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenMailReadStateRemainsLocalThroughWrappers(t *testing.T) {
	// An unconfigured adapter would fail on any provider call. The capability
	// check must avoid even fetching the thread when no per-message update exists.
	raw := &OpenMailTransport{}
	for _, transport := range []Transport{raw, WithTransportRetries(raw), &pairEndpoint{router: &InboxRouter{raw: WithTransportRetries(raw)}}} {
		supported, err := SetMessageRead(context.Background(), transport, "message", true)
		if supported || err != nil {
			t.Fatalf("OpenMail read state: supported=%v err=%v", supported, err)
		}
	}
}

func TestSendmuxReadStateRemovesOnlySelectedMessageAndRestoresIt(t *testing.T) {
	api := newFakeSendmuxAPI("")
	transport, err := newSendmuxTransport(sendmuxTransportConfig{API: api, Inbox: "device@myagent.mx", AllowMutation: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, read := range []bool{true, false} {
		supported, err := SetMessageRead(context.Background(), WithTransportRetries(transport), "message-old-2", read)
		if err != nil || !supported {
			t.Fatalf("read=%v: supported=%v err=%v", read, supported, err)
		}
		selected, _ := api.find("message-old-2")
		other, _ := api.find("message-new")
		if selected.Seen != read || other.Seen {
			t.Fatal("read state changed the wrong messages")
		}
		messages, err := transport.Poll(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, message := range messages {
			found = found || message.MessageID == selected.ID
		}
		if found == read {
			t.Fatal("poll did not respect restored read state")
		}
	}
}

type readStateRecorder struct {
	*fakeTransport
	calls int
}

func (r *readStateRecorder) SetMessageRead(context.Context, string, bool) (bool, error) {
	r.calls++
	return true, nil
}

func TestReadStateRejectsAnotherOwnerMessage(t *testing.T) {
	_, raw, router, pair, inbox := newParticipantTestRig(t)
	recorder := &readStateRecorder{fakeTransport: raw}
	router.raw = WithTransportRetries(recorder)
	other := Message{MessageID: "other-owner-message", ThreadID: "unowned-thread", From: "other@example.test", To: []string{inbox.Address}, Body: "Private", Timestamp: time.Now().UTC()}
	raw.setThread(other.ThreadID, []Message{other})
	endpoint, err := router.Endpoint(pair.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, read := range []bool{true, false} {
		if _, err := SetMessageRead(context.Background(), endpoint, other.MessageID, read); err == nil {
			t.Fatal("another owner's message was allowed")
		}
	}
	if recorder.calls != 0 {
		t.Fatal("unauthorized provider mutation")
	}
}

func TestSkippedReadFailureRetainsSuppressionForRetry(t *testing.T) {
	rig := newTestRig(t)
	message := testMessage("skip-read-failure", "skip-thread", "Held")
	rig.mail.add(message)
	if _, err := rig.store.SkipMessages([]MessageRef{{MessageID: message.MessageID, ThreadID: message.ThreadID}}, "test"); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("read state unavailable")
	rig.app.transport = &failedReadTransport{Transport: rig.app.transport, failure: failure}
	if err := rig.app.ProcessOnce(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("ProcessOnce = %v", err)
	}
	if skipped, err := rig.store.IsSkipped(message.MessageID); !skipped || err != nil {
		t.Fatal("skip lost after read-state failure")
	}
	if _, err := os.Stat(filepath.Join(rig.captureDir, "count")); !os.IsNotExist(err) {
		t.Fatal("failed acknowledgement released skipped work")
	}
	rig.app.transport = rig.app.transport.(*failedReadTransport).Transport
	mustProcess(t, rig)
	if rig.mail.isUnread(message.MessageID) {
		t.Fatal("retry did not clear unread state")
	}
}

type failedReadTransport struct {
	Transport
	failure error
}

func (t *failedReadTransport) SetMessageRead(context.Context, string, bool) (bool, error) {
	return true, t.failure
}
