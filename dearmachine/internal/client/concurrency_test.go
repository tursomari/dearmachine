package client

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dearmachine/dearmachine/internal/synctrigger"
)

type gatedRun struct {
	threadID string
	sequence int
	args     []string
}

type observedPollTransport struct {
	Transport
	polls chan []Message
}

func (t *observedPollTransport) Poll(ctx context.Context) ([]Message, error) {
	messages, err := t.Transport.Poll(ctx)
	select {
	case t.polls <- messages:
	default:
	}
	return messages, err
}

var promptSessionLine = regexp.MustCompile(
	`\[Thread: ([^ ]+) \| Session: [^ ]+ \| Sequence: ([0-9]+)\]`,
)

func TestProcessOnceLimitsConcurrentThreadsAndPreservesThreadFIFO(t *testing.T) {
	const (
		threadCount       = 3
		messagesPerThread = 3
		concurrency       = 2
	)
	transport := newFakeTransport()
	var messages []Message
	for thread := 1; thread <= threadCount; thread++ {
		threadID := fmt.Sprintf("thread-%d", thread)
		for sequence := 1; sequence <= messagesPerThread; sequence++ {
			messages = append(messages, Message{
				MessageID: fmt.Sprintf("message-%d-%d", thread, sequence),
				ThreadID:  threadID,
				From:      "sender@example.com",
				CreatedAt: time.Date(2026, time.August, 8, 12, sequence, 0, 0, time.UTC),
				Body:      fmt.Sprintf("request %d for %s", sequence, threadID),
				Labels:    []string{"unread"},
			})
		}
	}
	transport.setPoll(messages)

	started := make(chan gatedRun, len(messages))
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	app, store := newInMemoryApp(t, transport, concurrency, func(command *exec.Cmd) error {
		switch {
		case slices.Equal(command.Args[1:], []string{"sync"}):
			return nil
		case len(command.Args) > 1 && command.Args[1] == "run":
			invocation, err := parseGatedRun(command.Args)
			if err != nil {
				return err
			}
			current := active.Add(1)
			for {
				observed := maximum.Load()
				if current <= observed || maximum.CompareAndSwap(observed, current) {
					break
				}
			}
			started <- invocation
			<-release
			if err := os.WriteFile(commandArgument(command.Args, "--final-file"), []byte("done"), 0o600); err != nil {
				return err
			}
			active.Add(-1)
			return nil
		case len(command.Args) > 2 && command.Args[1] == "session" && command.Args[2] == "show":
			_, err := io.WriteString(command.Stdout, `{"status":"success"}`)
			return err
		case len(command.Args) > 2 && command.Args[1] == "session" && command.Args[2] == "fork":
			_, err := fmt.Fprintln(command.Stdout, newConversationReference())
			return err
		case len(command.Args) > 2 && command.Args[1] == "session" && command.Args[2] == "delete":
			return nil
		default:
			return fmt.Errorf("unexpected machtiani invocation: %v", command.Args)
		}
	})

	done := make(chan error, 1)
	go func() {
		done <- app.ProcessOnce(context.Background())
	}()

	order := make(map[string][]int, threadCount)
	for range concurrency {
		invocation := awaitGatedRun(t, started)
		order[invocation.threadID] = append(order[invocation.threadID], invocation.sequence)
	}
	select {
	case invocation := <-started:
		t.Fatalf("third run started before a worker was released: %+v", invocation)
	case <-time.After(50 * time.Millisecond):
	}
	for range concurrency {
		release <- struct{}{}
	}
	for completed := concurrency; completed < len(messages); completed++ {
		invocation := awaitGatedRun(t, started)
		order[invocation.threadID] = append(order[invocation.threadID], invocation.sequence)
		release <- struct{}{}
	}
	if err := <-done; err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	if got := maximum.Load(); got > concurrency {
		t.Fatalf("maximum active runs = %d, want <= %d", got, concurrency)
	}
	for thread := 1; thread <= threadCount; thread++ {
		threadID := fmt.Sprintf("thread-%d", thread)
		if got := order[threadID]; !slices.Equal(got, []int{1, 2, 3}) {
			t.Errorf("%s run order = %v, want [1 2 3]", threadID, got)
		}
		session, err := store.Session(threadID)
		if err != nil {
			t.Fatalf("Session(%s): %v", threadID, err)
		}
		if session.Sequence != messagesPerThread {
			t.Errorf("%s sequence = %d, want %d", threadID, session.Sequence, messagesPerThread)
		}
	}

	transport.mu.Lock()
	replies := append([]fakeTransportReply(nil), transport.replies...)
	processed := append([]string(nil), transport.processed...)
	transport.mu.Unlock()
	assertMessageIDsExactlyOnce(t, messages, replies, processed)
}

func TestProcessOnceClaimsMidRunArrivalWhileWorkerBusy(t *testing.T) {
	messageA := Message{
		MessageID: "message-a",
		ThreadID:  "thread-a",
		From:      "sender@example.com",
		Body:      "long-running request",
	}
	messageB := Message{
		MessageID: "message-b",
		ThreadID:  "thread-b",
		From:      "sender@example.com",
		Body:      "mid-run request",
	}
	transport := newFakeTransport()
	transport.setPoll([]Message{messageA})
	started := make(chan gatedRun, 2)
	release := make(chan struct{})
	app, store := newInMemoryApp(t, transport, 2, gatedRunInvoker(started, release))
	app.pollInterval = 10 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		done <- app.ProcessOnce(context.Background())
	}()
	first := awaitGatedRun(t, started)
	if first.threadID != messageA.ThreadID {
		close(release)
		<-done
		t.Fatalf("first run = %+v, want thread A", first)
	}

	transport.setPoll([]Message{messageB})
	claimed := false
	startedB := false
	var claimErr error
	deadline := time.NewTimer(time.Second)
	ticker := time.NewTicker(time.Millisecond)
observe:
	for !claimed || !startedB {
		if !claimed {
			_, claimed, claimErr = store.PendingByID(messageB.MessageID)
			if claimErr != nil {
				break
			}
		}
		select {
		case invocation := <-started:
			if invocation.threadID == messageB.ThreadID {
				startedB = true
			} else {
				t.Errorf("unexpected run while awaiting thread B: %+v", invocation)
			}
		case <-ticker.C:
		case <-deadline.C:
			break observe
		}
	}
	ticker.Stop()
	if !deadline.Stop() {
		select {
		case <-deadline.C:
		default:
		}
	}
	if !claimed {
		_, claimed, claimErr = store.PendingByID(messageB.MessageID)
	}
	if !startedB {
		select {
		case invocation := <-started:
			startedB = invocation.threadID == messageB.ThreadID
		default:
		}
	}

	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ProcessOnce: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ProcessOnce did not finish after releasing workers")
	}
	if claimErr != nil {
		t.Fatalf("PendingByID(%s): %v", messageB.MessageID, claimErr)
	}
	if !claimed {
		t.Errorf("thread B was not durably claimed while thread A was running")
	}
	if !startedB {
		t.Errorf("thread B's machtiani run did not start before thread A was released")
	}
}

func TestRunPollsWhileDispatchActive(t *testing.T) {
	messageA := Message{
		MessageID: "run-message-a",
		ThreadID:  "run-thread-a",
		From:      "sender@example.com",
		Body:      "long-running request",
	}
	messageB := Message{
		MessageID: "run-message-b",
		ThreadID:  "run-thread-b",
		From:      "sender@example.com",
		Body:      "request arriving during dispatch",
	}
	transport := newFakeTransport()
	transport.setPoll([]Message{messageA})
	started := make(chan gatedRun, 2)
	release := make(chan struct{})
	app, store := newInMemoryApp(t, transport, 2, gatedRunInvoker(started, release))
	app.pollInterval = 10 * time.Millisecond
	polls := make(chan []Message, 16)
	app.transport = &observedPollTransport{Transport: transport, polls: polls}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx)
	}()
	first := awaitGatedRun(t, started)
	if first.threadID != messageA.ThreadID {
		cancel()
		close(release)
		<-done
		t.Fatalf("first run = %+v, want thread A", first)
	}
	for {
		select {
		case <-polls:
		default:
			goto inject
		}
	}

inject:
	transport.setPoll([]Message{messageB})
	polledB := false
	claimedB := false
	startedB := false
	var claimErr error
	deadline := time.NewTimer(time.Second)
	ticker := time.NewTicker(time.Millisecond)
observe:
	for !polledB || !claimedB || !startedB {
		if !claimedB {
			_, claimedB, claimErr = store.PendingByID(messageB.MessageID)
			if claimErr != nil {
				break
			}
		}
		select {
		case messages := <-polls:
			for _, message := range messages {
				if message.MessageID == messageB.MessageID {
					polledB = true
				}
			}
		case invocation := <-started:
			if invocation.threadID == messageB.ThreadID {
				startedB = true
			} else {
				t.Errorf("unexpected run while awaiting thread B: %+v", invocation)
			}
		case <-ticker.C:
		case <-deadline.C:
			break observe
		}
	}
	ticker.Stop()
	if !deadline.Stop() {
		select {
		case <-deadline.C:
		default:
		}
	}
	if !claimedB {
		_, claimedB, claimErr = store.PendingByID(messageB.MessageID)
	}
	if !startedB {
		select {
		case invocation := <-started:
			startedB = invocation.threadID == messageB.ThreadID
		default:
		}
	}

	cancel()
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation and worker release")
	}
	if claimErr != nil {
		t.Fatalf("PendingByID(%s): %v", messageB.MessageID, claimErr)
	}
	if !polledB {
		t.Errorf("second poll did not observe thread B while thread A was running")
	}
	if !claimedB {
		t.Errorf("thread B was not durably claimed while thread A was running")
	}
	if !startedB {
		t.Errorf("thread B's machtiani run did not start before thread A was released")
	}
}

func TestRunPollsAndDurablyClaimsWhileMaintenanceActive(t *testing.T) {
	message := Message{
		MessageID: "maintenance-message",
		ThreadID:  "maintenance-thread",
		From:      "sender@example.com",
		Body:      "request arriving during maintenance",
	}
	transport := newFakeTransport()
	transport.setPoll(nil)
	app, store := newInMemoryApp(t, transport, 1, func(command *exec.Cmd) error {
		if slices.Equal(command.Args[1:], []string{"sync"}) {
			return nil
		}
		return fmt.Errorf("unexpected machtiani invocation: %v", command.Args)
	})
	app.pollInterval = 10 * time.Millisecond
	polls := make(chan []Message, 16)
	app.transport = &observedPollTransport{Transport: transport, polls: polls}

	maintenanceStarted := make(chan struct{})
	maintenanceRelease := make(chan struct{})
	baseTime := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	app.syncOrchestrator = &synctrigger.Orchestrator{
		RepoPath:           "/repo",
		AgentBinary:        "machtiani",
		PromptTemplatePath: "/prompt.md",
		StatePath:          filepath.Join(t.TempDir(), "sync-trigger.json"),
		Lister: func(context.Context, string) ([]synctrigger.SessionInfo, error) {
			return []synctrigger.SessionInfo{
				{SessionID: "reviewable", UpdatedAt: baseTime},
				{SessionID: "holdback", UpdatedAt: baseTime.Add(time.Hour)},
			}, nil
		},
		GitLastCommitTime: func(string) (time.Time, error) { return time.Time{}, nil },
		RunCommand: func(ctx context.Context, _ string, _ string, args ...string) ([]byte, error) {
			if strings.Join(args, " ") == "session fork reviewable" {
				select {
				case <-maintenanceStarted:
				default:
					close(maintenanceStarted)
				}
				select {
				case <-maintenanceRelease:
					return []byte("forked-reviewable\n"), nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return nil, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx)
	}()
	select {
	case <-maintenanceStarted:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("maintenance did not start")
	}

	transport.setPoll([]Message{message})
	claimed := false
	deadline := time.NewTimer(time.Second)
	ticker := time.NewTicker(time.Millisecond)
observe:
	for !claimed {
		var err error
		_, claimed, err = store.PendingByID(message.MessageID)
		if err != nil {
			cancel()
			close(maintenanceRelease)
			<-done
			t.Fatalf("PendingByID(%s): %v", message.MessageID, err)
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			break observe
		}
	}
	ticker.Stop()
	if !deadline.Stop() {
		select {
		case <-deadline.C:
		default:
		}
	}
	if !claimed {
		cancel()
		close(maintenanceRelease)
		<-done
		t.Fatal("message was not durably claimed while maintenance was active")
	}

	cancel()
	close(maintenanceRelease)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestConcurrentDuplicateClaimCreatesOnceAndRereads(t *testing.T) {
	store := openTestStore(t)
	start := make(chan struct{})
	type claimResult struct {
		pending PendingMessage
		existed bool
		err     error
	}
	results := make(chan claimResult, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			pending, existed, err := store.BeginMessage("message-1", "thread-1", TierPlain)
			results <- claimResult{pending: pending, existed: existed, err: err}
		}()
	}
	ready.Wait()
	close(start)

	claims := []claimResult{<-results, <-results}
	if claims[0].err != nil || claims[1].err != nil {
		t.Fatalf("claim errors = %v, %v", claims[0].err, claims[1].err)
	}
	if claims[0].existed == claims[1].existed {
		t.Fatalf("existing results = %v, %v; want one claim and one reread", claims[0].existed, claims[1].existed)
	}
	if claims[0].pending != claims[1].pending {
		t.Fatalf("reread changed claim: first=%+v second=%+v", claims[0].pending, claims[1].pending)
	}
	if claims[0].pending.Session.ResponseTier != TierPlain {
		t.Fatalf("claim response tier = %q, want %q", claims[0].pending.Session.ResponseTier, TierPlain)
	}
	pending, err := store.Pending()
	if err != nil || len(pending) != 1 {
		t.Fatalf("Pending = %+v, %v; want exactly one row", pending, err)
	}
}

func TestSkippedMessageIsNotProcessedDuringDispatchContention(t *testing.T) {
	transport := newFakeTransport()
	messages := []Message{
		{MessageID: "active-a", ThreadID: "thread-a", From: "sender@example.com", Body: "first"},
		{MessageID: "active-b", ThreadID: "thread-b", From: "sender@example.com", Body: "second"},
		{MessageID: "skip-me", ThreadID: "thread-c", From: "sender@example.com", Body: "third"},
	}
	transport.setPoll(messages)
	started := make(chan gatedRun, len(messages))
	release := make(chan struct{})
	app, store := newInMemoryApp(t, transport, 2, func(command *exec.Cmd) error {
		switch {
		case slices.Equal(command.Args[1:], []string{"sync"}):
			return nil
		case len(command.Args) > 1 && command.Args[1] == "run":
			invocation, err := parseGatedRun(command.Args)
			if err != nil {
				return err
			}
			started <- invocation
			<-release
			return os.WriteFile(
				commandArgument(command.Args, "--final-file"),
				[]byte("done"),
				0o600,
			)
		case len(command.Args) > 2 && command.Args[1] == "session" && command.Args[2] == "show":
			_, err := io.WriteString(command.Stdout, `{"status":"success"}`)
			return err
		default:
			return fmt.Errorf("unexpected machtiani invocation: %v", command.Args)
		}
	})

	done := make(chan error, 1)
	go func() {
		done <- app.ProcessOnce(context.Background())
	}()
	for range 2 {
		invocation := awaitGatedRun(t, started)
		if invocation.threadID == "thread-c" {
			t.Fatalf("skipped candidate started before contention: %+v", invocation)
		}
	}
	if _, err := store.SkipMessages(
		[]MessageRef{{MessageID: "skip-me", ThreadID: "thread-c"}},
		"contention test",
	); err != nil {
		t.Fatalf("SkipMessages: %v", err)
	}
	for range 2 {
		release <- struct{}{}
	}
	if err := <-done; err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	select {
	case invocation := <-started:
		t.Fatalf("skipped message reached machtiani: %+v", invocation)
	default:
	}
	if skipped, err := store.IsSkipped("skip-me"); err != nil || !skipped {
		t.Fatalf("IsSkipped(skip-me) = %v, %v", skipped, err)
	}
	if seen, err := store.Seen("skip-me"); err != nil || seen {
		t.Fatalf("Seen(skip-me) = %v, %v", seen, err)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if slices.Contains(transport.processed, "skip-me") {
		t.Fatalf("skipped message was remotely marked processed: %v", transport.processed)
	}
	for _, reply := range transport.replies {
		if reply.MessageID == "skip-me" {
			t.Fatalf("skipped message received reply: %+v", reply)
		}
	}
}

func newInMemoryApp(
	t *testing.T,
	transport *fakeTransport,
	concurrency int,
	invoke func(*exec.Cmd) error,
) (*App, *Store) {
	t.Helper()
	store := openTestStore(t)
	runner, err := NewAgentRunner("machtiani", t.TempDir(), "")
	if err != nil {
		t.Fatalf("NewAgentRunner: %v", err)
	}
	if err := runner.ConfigureAgentManaged([]string{"codex"}, "/test/agent-manager", nil); err != nil {
		t.Fatalf("ConfigureAgentManaged: %v", err)
	}
	runner.invoke = invoke
	app, err := New(
		transport,
		store,
		runner,
		nil,
		concurrency,
		time.Minute,
		log.New(io.Discard, "", 0),
		false,
		"",
		TierPlain,
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return app, store
}

func gatedRunInvoker(started chan<- gatedRun, release <-chan struct{}) func(*exec.Cmd) error {
	return func(command *exec.Cmd) error {
		switch {
		case slices.Equal(command.Args[1:], []string{"sync"}):
			return nil
		case len(command.Args) > 1 && command.Args[1] == "run":
			invocation, err := parseGatedRun(command.Args)
			if err != nil {
				return err
			}
			started <- invocation
			<-release
			return os.WriteFile(
				commandArgument(command.Args, "--final-file"),
				[]byte("done"),
				0o600,
			)
		case len(command.Args) > 2 && command.Args[1] == "session" && command.Args[2] == "show":
			_, err := io.WriteString(command.Stdout, `{"status":"success"}`)
			return err
		case len(command.Args) > 2 && command.Args[1] == "session" && command.Args[2] == "fork":
			_, err := fmt.Fprintln(command.Stdout, newConversationReference())
			return err
		case len(command.Args) > 2 && command.Args[1] == "session" && command.Args[2] == "delete":
			return nil
		default:
			return fmt.Errorf("unexpected machtiani invocation: %v", command.Args)
		}
	}
}

func parseGatedRun(args []string) (gatedRun, error) {
	prompt := commandArgument(args, "--prompt")
	match := promptSessionLine.FindStringSubmatch(prompt)
	if len(match) != 3 {
		return gatedRun{}, fmt.Errorf("prompt session line not found in %q", prompt)
	}
	sequence, err := strconv.Atoi(match[2])
	if err != nil {
		return gatedRun{}, fmt.Errorf("parse sequence %q: %w", match[2], err)
	}
	return gatedRun{threadID: match[1], sequence: sequence, args: append([]string(nil), args...)}, nil
}

func commandArgument(args []string, name string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1]
		}
	}
	return ""
}

func awaitGatedRun(t *testing.T, started <-chan gatedRun) gatedRun {
	t.Helper()
	select {
	case invocation := <-started:
		return invocation
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for machtiani run")
		return gatedRun{}
	}
}

func assertMessageIDsExactlyOnce(
	t *testing.T,
	messages []Message,
	replies []fakeTransportReply,
	processed []string,
) {
	t.Helper()
	replyCounts := make(map[string]int, len(replies))
	for _, reply := range replies {
		replyCounts[reply.MessageID]++
	}
	processedCounts := make(map[string]int, len(processed))
	for _, messageID := range processed {
		processedCounts[messageID]++
	}
	for _, message := range messages {
		if replyCounts[message.MessageID] != 1 || processedCounts[message.MessageID] != 1 {
			t.Errorf(
				"message %s reply/processed counts = %d/%d, want 1/1",
				message.MessageID,
				replyCounts[message.MessageID],
				processedCounts[message.MessageID],
			)
		}
	}
}
