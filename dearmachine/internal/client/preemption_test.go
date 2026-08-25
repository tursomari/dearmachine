package client

import (
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"
)

func TestNewEmailStopsRunningSessionAndResumesWithNewPrompt(t *testing.T) {
	first := Message{MessageID: "message-1", ThreadID: "thread-1", From: "sender@example.com", Body: "first"}
	second := Message{MessageID: "message-2", ThreadID: "thread-1", From: "sender@example.com", Body: "second"}
	transport := newFakeTransport()
	transport.setPoll([]Message{first})
	started := make(chan gatedRun, 2)
	release := make(chan struct{})
	app, store := newInMemoryApp(t, transport, 1, gatedRunInvoker(started, release))
	app.pollInterval = time.Millisecond
	done := make(chan error, 1)
	go func() { done <- app.ProcessOnce(context.Background()) }()
	if got := awaitGatedRun(t, started); got.threadID != first.ThreadID || got.sequence != 1 {
		t.Fatalf("run 1 = %+v", got)
	}
	transport.setPoll([]Message{second})
	deadline := time.Now().Add(time.Second)
	for {
		if _, found, _ := store.PendingByID(second.MessageID); found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second email was not claimed")
		}
		time.Sleep(time.Millisecond)
	}
	transport.setPoll(nil)
	// The preempted invocation exits through the runner's graceful stop seam;
	// the release only permits a legacy gated invoker to complete if needed.
	release <- struct{}{}
	var secondRun gatedRun
	select {
	case secondRun = <-started:
	case err := <-done:
		t.Fatalf("ProcessOnce ended before run 2: %v", err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for run 2")
	}
	if got := secondRun; got.threadID != second.ThreadID || got.sequence != 2 {
		t.Fatalf("run 2 = %+v, want resumed follow-up", got)
	} else if commandArgument(got.args, "--resume") == "" || commandArgument(got.args, "--session-id") != "" {
		t.Fatalf("run 2 argv = %v, want --resume and no --session-id", got.args)
	}
	release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	if skipped, _ := store.IsSkipped(first.MessageID); !skipped {
		t.Fatal("preempted first email was not skipped")
	}
	transport.mu.Lock()
	replies := append([]fakeTransportReply(nil), transport.replies...)
	processed := append([]string(nil), transport.processed...)
	transport.mu.Unlock()
	if len(replies) != 1 || replies[0].MessageID != second.MessageID {
		t.Fatalf("replies = %+v, want only second", replies)
	}
	if len(processed) != 1 || processed[0] != second.MessageID {
		t.Fatalf("processed = %v, want only second", processed)
	}
}

func TestNewEmailDuringIdleThreadDoesNotStop(t *testing.T) {
	message := Message{MessageID: "message-1", ThreadID: "thread-1", From: "sender@example.com", Body: "one"}
	transport := newFakeTransport()
	transport.setPoll([]Message{message})
	started := make(chan gatedRun, 1)
	release := make(chan struct{})
	app, _ := newInMemoryApp(t, transport, 1, gatedRunInvoker(started, release))
	done := make(chan error, 1)
	go func() { done <- app.ProcessOnce(context.Background()) }()
	if got := awaitGatedRun(t, started); got.sequence != 1 {
		t.Fatalf("run = %+v", got)
	}
	release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if app.runner.Stop(message.ThreadID) {
		t.Fatal("Stop found an idle run")
	}
}

func TestNewEmailArrivingJustAsRunFinishesIsNotKilled(t *testing.T) {
	first := Message{MessageID: "message-1", ThreadID: "thread-1", From: "sender@example.com", Body: "one"}
	second := Message{MessageID: "message-2", ThreadID: "thread-1", From: "sender@example.com", Body: "two"}
	transport := newFakeTransport()
	transport.setPoll([]Message{first})
	started := make(chan gatedRun, 2)
	release := make(chan struct{})
	app, _ := newInMemoryApp(t, transport, 1, gatedRunInvoker(started, release))
	done := make(chan error, 1)
	go func() { done <- app.ProcessOnce(context.Background()) }()
	_ = awaitGatedRun(t, started)
	release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	transport.setPoll([]Message{second})
	work := newThreadWorkQueue()
	if err := app.pollAndClaim(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	if app.runner.Stop(first.ThreadID) {
		t.Fatal("finished run was stopped")
	}
	if item, ok := work.take(); !ok || item.pending.MessageID != second.MessageID {
		t.Fatalf("claimed work = %+v, %v", item, ok)
	}
}

func TestRepollOfRunningMessageDoesNotSelfPreempt(t *testing.T) {
	message := Message{MessageID: "message-1", ThreadID: "thread-1", From: "sender@example.com", Body: "one"}
	transport := newFakeTransport()
	transport.setPoll([]Message{message})
	started := make(chan gatedRun, 1)
	release := make(chan struct{})
	app, store := newInMemoryApp(t, transport, 1, gatedRunInvoker(started, release))
	var stopCalls int
	var stopCallsMu sync.Mutex
	app.runner.interrupt = func(*exec.Cmd) error {
		stopCallsMu.Lock()
		stopCalls++
		stopCallsMu.Unlock()
		return nil
	}
	work := newThreadWorkQueue()
	if err := app.pollAndClaim(context.Background(), work); err != nil {
		t.Fatalf("initial pollAndClaim: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- app.dispatch(context.Background(), work) }()
	if got := awaitGatedRun(t, started); got.threadID != message.ThreadID || got.sequence != 1 {
		t.Fatalf("run = %+v", got)
	}

	if err := app.pollAndClaim(context.Background(), work); err != nil {
		t.Fatalf("re-poll running message: %v", err)
	}
	stopCallsMu.Lock()
	gotStopCalls := stopCalls
	stopCallsMu.Unlock()
	if gotStopCalls != 0 {
		t.Fatalf("Stop delivered %d times, want 0", gotStopCalls)
	}
	if skipped, err := store.IsSkipped(message.MessageID); err != nil || skipped {
		t.Fatalf("IsSkipped = %v, %v; want false, nil", skipped, err)
	}
	if _, running := work.inFlightWork(message.ThreadID); !running {
		t.Fatal("re-poll did not preserve in-flight work")
	}

	release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

func TestRepollOfSeenMessageStillMarksProcessed(t *testing.T) {
	message := Message{MessageID: "message-1", ThreadID: "thread-1", From: "sender@example.com", Body: "one"}
	transport := newFakeTransport()
	transport.setPoll([]Message{message})
	app, store := newInMemoryApp(t, transport, 1, gatedRunInvoker(make(chan gatedRun), make(chan struct{})))
	pending, _, err := store.BeginMessage(message.MessageID, message.ThreadID, TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(pending.MessageID, "running"); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreResult(pending.MessageID, RunResult{Kind: ResultAnswer, Text: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(pending.MessageID, "completed", "reply-1"); err != nil {
		t.Fatal(err)
	}

	if err := app.pollAndClaim(context.Background(), newThreadWorkQueue()); err != nil {
		t.Fatalf("re-poll seen message: %v", err)
	}
	transport.mu.Lock()
	processed := append([]string(nil), transport.processed...)
	transport.mu.Unlock()
	if len(processed) != 1 || processed[0] != message.MessageID {
		t.Fatalf("processed = %v, want [%s]", processed, message.MessageID)
	}
}

func TestInterruptedEmailDoesNotBlockQueueOrReplay(t *testing.T) {
	store := openTestStore(t)
	first, _, err := store.BeginMessage("message-0", "thread-1", TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(first.MessageID, "first"); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreResult(first.MessageID, RunResult{Kind: ResultAnswer, Text: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(first.MessageID, "completed", "reply-0"); err != nil {
		t.Fatal(err)
	}
	interrupted, _, err := store.BeginMessage("message-1", "thread-1", TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(interrupted.MessageID, "partial"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SkipPreemptedMessage(MessageRef{MessageID: interrupted.MessageID, ThreadID: interrupted.ThreadID}, "preempted by newer email"); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.Pending(); err != nil || len(pending) != 0 {
		t.Fatalf("Pending after preemption = %+v, %v", pending, err)
	}
	if skipped, err := store.IsSkipped(interrupted.MessageID); err != nil || !skipped {
		t.Fatalf("IsSkipped = %v, %v", skipped, err)
	}
	transport := newFakeTransport()
	app, _ := newInMemoryApp(t, transport, 1, gatedRunInvoker(make(chan gatedRun), make(chan struct{})))
	app.store = store
	work := newThreadWorkQueue()
	if err := app.recoverPending(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	if _, ok := work.take(); ok {
		t.Fatal("preempted message resurfaced during recovery")
	}
}
