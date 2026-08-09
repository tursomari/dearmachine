package deviceclient

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type gatedRun struct {
	threadID string
	sequence int
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
			_, err := fmt.Fprintf(command.Stdout, "checkpoint-%s\n", command.Args[3])
			return err
		case len(command.Args) > 2 && command.Args[1] == "session" && command.Args[2] == "delete":
			return nil
		default:
			return fmt.Errorf("unexpected mct-agent invocation: %v", command.Args)
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
			pending, existed, err := store.BeginMessage("message-1", "thread-1")
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
			return fmt.Errorf("unexpected mct-agent invocation: %v", command.Args)
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
		t.Fatalf("skipped message reached mct-agent: %+v", invocation)
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
	runner, err := NewMCTRunner("mct-agent", t.TempDir(), "")
	if err != nil {
		t.Fatalf("NewMCTRunner: %v", err)
	}
	if err := runner.ConfigureAgentManaged([]string{"codex"}, "/test/agent-manager"); err != nil {
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
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return app, store
}

func parseGatedRun(args []string) (gatedRun, error) {
	prompt := commandArgument(args, "--text")
	match := promptSessionLine.FindStringSubmatch(prompt)
	if len(match) != 3 {
		return gatedRun{}, fmt.Errorf("prompt session line not found in %q", prompt)
	}
	sequence, err := strconv.Atoi(match[2])
	if err != nil {
		return gatedRun{}, fmt.Errorf("parse sequence %q: %w", match[2], err)
	}
	return gatedRun{threadID: match[1], sequence: sequence}, nil
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
		t.Fatal("timed out waiting for mct-agent run")
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
