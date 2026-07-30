package deviceclient

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	agentmail "github.com/agentmail-to/agentmail-go"
	"github.com/agentmail-to/agentmail-go/option"
)

type sentReply struct {
	MessageID      string
	Text           string
	IdempotencyKey string
}

type fakeAgentMail struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	messages map[string]agentmail.Message
	unread   map[string]bool
	replies  []sentReply
	polls    chan time.Time
}

type testRig struct {
	app        *App
	store      *Store
	mail       *fakeAgentMail
	captureDir string
	statusFile string
	answerFile string
}

func TestNewMessageCreatesSessionAndSendsAnswer(t *testing.T) {
	rig := newTestRig(t)
	message := testMessage(
		"msg-001",
		"thread-001",
		"Can you generate a sales report for Q3?",
	)
	rig.mail.add(message)
	rig.setAnswer("Here is the Q3 sales report.")

	if err := rig.app.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}

	session := rig.session("thread-001")
	if session.Sequence != 1 || session.Status != "completed" {
		t.Fatalf("unexpected session: %+v", session)
	}
	if got := rig.capture("session-env-1"); got != session.SessionID {
		t.Fatalf("MACHTIANI_SESSION_ID = %q, want %q", got, session.SessionID)
	}
	args := rig.captureLines("args-1")
	if slices.Contains(args, "--session-id") {
		t.Fatalf("new session unexpectedly passed --session-id: %v", args)
	}
	assertArg(t, args, "--model", "test-model")
	assertArg(t, args, "--final-file", "")
	for _, flag := range []string{"--no-banner", "--no-cursor"} {
		if !slices.Contains(args, flag) {
			t.Errorf("missing %s in %v", flag, args)
		}
	}

	prompt := rig.capture("text-1")
	for _, want := range []string{
		"[Thread: thread-001 | Session: " + session.SessionID + " | Sequence: 1]",
		"Can you generate a sales report for Q3?",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "[Previous messages in this thread:]") {
		t.Errorf("new-thread prompt unexpectedly includes history:\n%s", prompt)
	}

	replies := rig.mail.sentReplies()
	if len(replies) != 1 || replies[0].Text != "Here is the Q3 sales report." {
		t.Fatalf("unexpected replies: %+v", replies)
	}
	if replies[0].IdempotencyKey == "" {
		t.Fatal("reply omitted Idempotency-Key")
	}
}

func TestFollowUpResumesExistingSession(t *testing.T) {
	rig := newTestRig(t)
	rig.mail.add(testMessage("msg-001", "thread-001", "Build the Q3 report."))
	rig.setAnswer("Initial report.")
	mustProcess(t, rig)
	original := rig.session("thread-001")

	followUp := testMessage("msg-002", "thread-001", "Add a regional breakdown.")
	followUp.Subject = "Re: Q3 report"
	followUp.InReplyTo = "reply-001"
	rig.mail.add(followUp)
	rig.setAnswer("Here is the regional breakdown.")
	mustProcess(t, rig)

	resumed := rig.session("thread-001")
	if resumed.SessionID != original.SessionID {
		t.Fatalf("session changed: got %q, want %q", resumed.SessionID, original.SessionID)
	}
	if resumed.Sequence != 2 {
		t.Fatalf("sequence = %d, want 2", resumed.Sequence)
	}
	assertArg(t, rig.captureLines("args-2"), "--session-id", original.SessionID)
	if got := rig.capture("session-env-2"); got != "" {
		t.Fatalf("resumed run set MACHTIANI_SESSION_ID = %q", got)
	}
	if !strings.Contains(rig.capture("text-2"), "[Previous messages in this thread:]") {
		t.Fatalf("follow-up prompt omitted history:\n%s", rig.capture("text-2"))
	}

	replies := rig.mail.sentReplies()
	if got := replies[len(replies)-1].Text; got != "Here is the regional breakdown." {
		t.Fatalf("follow-up reply = %q", got)
	}
}

func TestAskUserThenResumeWithAnswer(t *testing.T) {
	rig := newTestRig(t)
	rig.mail.add(testMessage("msg-010", "thread-003", "Generate a report."))
	rig.setStatus(`{
		"status": "suspended_user_input",
		"suspended_user_input": {
			"question": "Which date range should I use?",
			"context": "Options: week, month, or quarter."
		}
	}`)
	mustProcess(t, rig)

	suspended := rig.session("thread-003")
	if suspended.Status != "suspended_user_input" {
		t.Fatalf("status = %q", suspended.Status)
	}
	firstReply := rig.mail.sentReplies()[0].Text
	for _, want := range []string{
		"Your computer has a question about your request:",
		"Which date range should I use?",
		"Options: week, month, or quarter.",
		"Reply to this email to answer.",
	} {
		if !strings.Contains(firstReply, want) {
			t.Errorf("clarification reply missing %q:\n%s", want, firstReply)
		}
	}

	rig.mail.add(testMessage("msg-011", "thread-003", "Last week, please."))
	rig.setStatus(`{"status":"success"}`)
	rig.setAnswer("Here is the report for last week.")
	mustProcess(t, rig)

	completed := rig.session("thread-003")
	if completed.Status != "completed" || completed.Sequence != 2 {
		t.Fatalf("unexpected completed session: %+v", completed)
	}
	assertArg(t, rig.captureLines("args-2"), "--session-id", completed.SessionID)
	replies := rig.mail.sentReplies()
	if len(replies) != 2 || replies[1].Text != "Here is the report for last week." {
		t.Fatalf("unexpected replies: %+v", replies)
	}
}

func TestQuoteBackMarkersArePreservedWithThreadContext(t *testing.T) {
	rig := newTestRig(t)
	rig.mail.add(testMessage("msg-020", "thread-004", "Draft a proposal."))
	rig.setAnswer("Here is the proposal draft.")
	mustProcess(t, rig)

	body := "Take a different approach.\n\n" +
		"> Here is the expanded Section 3:\n" +
		"> \n" +
		"> Prior cost analysis.\n\n" +
		"Focus Section 3 on costs."
	rig.mail.add(testMessage("msg-024", "thread-004", body))
	rig.setAnswer("I've updated section 3.")
	mustProcess(t, rig)

	prompt := rig.capture("text-2")
	for _, want := range []string{
		"[Previous messages in this thread:]",
		"Draft a proposal.",
		"> Here is the expanded Section 3:",
		"> Prior cost analysis.",
		"Focus Section 3 on costs.",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if got := rig.mail.sentReplies()[1].Text; got != "I've updated section 3." {
		t.Fatalf("reply = %q", got)
	}
}

func TestRunPollsAgainAfterConfiguredInterval(t *testing.T) {
	rig := newTestRig(t)
	rig.app.pollInterval = 15 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- rig.app.Run(ctx)
	}()

	var first time.Time
	select {
	case first = <-rig.mail.polls:
	case <-time.After(time.Second):
		t.Fatal("initial poll did not run immediately")
	}

	var second time.Time
	select {
	case second = <-rig.mail.polls:
	case <-time.After(time.Second):
		t.Fatal("second poll did not run")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := second.Sub(first); elapsed < rig.app.pollInterval {
		t.Fatalf("poll interval = %s, want at least %s", elapsed, rig.app.pollInterval)
	}
}

func newTestRig(t *testing.T) *testRig {
	t.Helper()
	mail := newFakeAgentMail(t)

	client := agentmail.NewClient(
		option.WithBaseURL(mail.server.URL+"/"),
		option.WithAPIKey("test-key"),
	)
	mailbox, err := NewMailbox(client, "test-inbox")
	if err != nil {
		t.Fatalf("NewMailbox: %v", err)
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "device-client.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
		mail.server.Close()
	})

	fixture, err := filepath.Abs(filepath.Join("testdata", "fake-mct-agent.sh"))
	if err != nil {
		t.Fatalf("fixture path: %v", err)
	}
	captureDir := t.TempDir()
	statusFile := filepath.Join(t.TempDir(), "status.json")
	answerFile := filepath.Join(t.TempDir(), "answer.md")
	t.Setenv("FAKE_MCT_CAPTURE", captureDir)
	t.Setenv("FAKE_MCT_STATUS", statusFile)
	t.Setenv("FAKE_MCT_ANSWER", answerFile)
	if err := os.WriteFile(statusFile, []byte(`{"status":"success"}`), 0o600); err != nil {
		t.Fatalf("write status: %v", err)
	}
	if err := os.WriteFile(answerFile, []byte("test answer"), 0o600); err != nil {
		t.Fatalf("write answer: %v", err)
	}

	runner, err := NewMCTRunner(fixture, t.TempDir(), "test-model")
	if err != nil {
		t.Fatalf("NewMCTRunner: %v", err)
	}
	app, err := New(
		mailbox,
		store,
		runner,
		time.Minute,
		log.New(io.Discard, "", 0),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &testRig{
		app:        app,
		store:      store,
		mail:       mail,
		captureDir: captureDir,
		statusFile: statusFile,
		answerFile: answerFile,
	}
}

func newFakeAgentMail(t *testing.T) *fakeAgentMail {
	t.Helper()
	fake := &fakeAgentMail{
		t:        t,
		messages: make(map[string]agentmail.Message),
		unread:   make(map[string]bool),
		polls:    make(chan time.Time, 16),
	}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serveHTTP))
	return fake
}

func (f *fakeAgentMail) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	writer.Header().Set("Content-Type", "application/json")
	path := request.URL.Path
	const prefix = "/v0/inboxes/test-inbox/"

	switch {
	case request.Method == http.MethodGet && path == prefix+"messages":
		f.serveList(writer)
	case request.Method == http.MethodGet && strings.HasPrefix(path, prefix+"messages/"):
		messageID := strings.TrimPrefix(path, prefix+"messages/")
		f.writeJSON(writer, f.messages[messageID])
	case request.Method == http.MethodPost &&
		strings.HasPrefix(path, prefix+"messages/") &&
		strings.HasSuffix(path, "/reply"):
		messageID := strings.TrimSuffix(strings.TrimPrefix(path, prefix+"messages/"), "/reply")
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			f.t.Errorf("decode reply: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		f.replies = append(f.replies, sentReply{
			MessageID:      messageID,
			Text:           body.Text,
			IdempotencyKey: request.Header.Get("Idempotency-Key"),
		})
		f.writeJSON(writer, map[string]string{
			"message_id": "reply-" + messageID,
			"thread_id":  f.messages[messageID].ThreadID,
		})
	case request.Method == http.MethodPatch && strings.HasPrefix(path, prefix+"messages/"):
		messageID := strings.TrimPrefix(path, prefix+"messages/")
		f.unread[messageID] = false
		f.writeJSON(writer, map[string]any{
			"message_id": messageID,
			"labels":     []string{"read"},
		})
	case request.Method == http.MethodGet && strings.HasPrefix(path, prefix+"threads/"):
		threadID := strings.TrimPrefix(path, prefix+"threads/")
		var messages []agentmail.Message
		for _, message := range f.messages {
			if message.ThreadID == threadID {
				messages = append(messages, message)
			}
		}
		f.writeJSON(writer, map[string]any{
			"thread_id": threadID,
			"messages":  messages,
		})
	default:
		f.t.Errorf("unexpected AgentMail request: %s %s", request.Method, path)
		writer.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeAgentMail) serveList(writer http.ResponseWriter) {
	f.polls <- time.Now()
	var messages []agentmail.Message
	for id, message := range f.messages {
		if f.unread[id] {
			messages = append(messages, message)
		}
	}
	slices.SortFunc(messages, func(a, b agentmail.Message) int {
		return a.Timestamp.Compare(b.Timestamp)
	})
	f.writeJSON(writer, map[string]any{
		"count":    len(messages),
		"messages": messages,
		"limit":    pageSize,
	})
}

func (f *fakeAgentMail) writeJSON(writer http.ResponseWriter, value any) {
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		f.t.Errorf("encode response: %v", err)
	}
}

func (f *fakeAgentMail) add(message agentmail.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages[message.MessageID] = message
	f.unread[message.MessageID] = true
}

func (f *fakeAgentMail) sentReplies() []sentReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.replies)
}

func testMessage(messageID, threadID, body string) agentmail.Message {
	index := len(messageID)
	timestamp := time.Date(2026, 7, 29, 10, index, 0, 0, time.UTC)
	return agentmail.Message{
		CreatedAt: timestamp,
		From:      "user@example.com",
		InboxID:   "test-inbox",
		Labels:    []string{"unread"},
		MessageID: messageID,
		ThreadID:  threadID,
		Timestamp: timestamp,
		To:        []string{"device@agentmail.to"},
		UpdatedAt: timestamp,
		Subject:   "Test message",
		Text:      body,
	}
}

func mustProcess(t *testing.T, rig *testRig) {
	t.Helper()
	if err := rig.app.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
}

func (r *testRig) setStatus(status string) {
	r.write(r.statusFile, status)
}

func (r *testRig) setAnswer(answer string) {
	r.write(r.answerFile, answer)
}

func (r *testRig) write(path, content string) {
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		panic(err)
	}
}

func (r *testRig) session(threadID string) Session {
	session, err := r.store.Session(threadID)
	if err != nil {
		panic(err)
	}
	return session
}

func (r *testRig) capture(name string) string {
	content, err := os.ReadFile(filepath.Join(r.captureDir, name))
	if err != nil {
		panic(err)
	}
	return string(content)
}

func (r *testRig) captureLines(name string) []string {
	return strings.Split(strings.TrimSpace(r.capture(name)), "\n")
}

func assertArg(t *testing.T, args []string, flag, value string) {
	t.Helper()
	index := slices.Index(args, flag)
	if index < 0 {
		t.Fatalf("missing %s in %v", flag, args)
	}
	if value != "" {
		if index+1 >= len(args) || args[index+1] != value {
			t.Fatalf("%s value in %v, want %q", flag, args, value)
		}
	}
}
