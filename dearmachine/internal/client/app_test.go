package client

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
	"strconv"
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

type fakeHTTPResponse struct {
	status int
	body   string
	delay  time.Duration
}

type fakeAgentMail struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	messages map[string]agentmail.Message
	unread   map[string]bool
	replies  []sentReply
	replyIDs map[string]string
	polls    chan time.Time
	failures map[string]fakeHTTPResponse
}

type testRig struct {
	app        *App
	store      *Store
	mail       *fakeAgentMail
	captureDir string
	statusFile string
	answerFile string
	dbPath     string
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
	assertArg(t, args, "--mode", "agent-managed")
	assertArg(t, args, "--final-file", "")
	if got := rig.capture("backends-env-1"); got != `["codex"]` {
		t.Fatalf("DEARMACHINE_BACKENDS = %q", got)
	}
	if got := rig.capture("backend-env-1"); got != "" {
		t.Fatalf("stale DEARMACHINE_BACKEND leaked into machtiani: %q", got)
	}
	if got := rig.capture("manager-env-1"); got != "/test/agent-manager" {
		t.Fatalf("AGENT_MANAGER_PATH = %q", got)
	}
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
	if got := rig.capture("sync-count"); got != "1" {
		t.Fatalf("sync count = %q, want 1", got)
	}
	if got := rig.capture("events"); got != "sync\nrun\n" {
		t.Fatalf("machtiani call order = %q, want sync then run", got)
	}
}

func TestFollowUpResumesExistingSession(t *testing.T) {
	t.Setenv("MACHTIANI_SESSION_ID", "outer-session")
	rig := newTestRig(t)
	rig.mail.add(testMessage("msg-001", "thread-001", "Build the Q3 report."))
	rig.setAnswer("Initial report.")
	mustProcess(t, rig)
	original := rig.session("thread-001")
	rig.mail.fail(
		http.MethodGet,
		fakeInboxPrefix+"threads/thread-001",
		fakeHTTPResponse{status: http.StatusServiceUnavailable},
	)

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
	if got := rig.capture("forked-sessions"); got != original.SessionID+"\n" {
		t.Fatalf("checkpointed sessions = %q, want original session", got)
	}
	if got := rig.capture("deleted-sessions"); got != "forked-session\n" {
		t.Fatalf("cleaned checkpoints = %q, want completed checkpoint", got)
	}
	prompt := rig.capture("text-2")
	if !strings.Contains(prompt, "Add a regional breakdown.") {
		t.Fatalf("follow-up prompt omitted latest message:\n%s", prompt)
	}
	for _, unwanted := range []string{
		"[Previous messages in this thread:]",
		"Build the Q3 report.",
		"Initial report.",
	} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("follow-up prompt contains prior context %q:\n%s", unwanted, prompt)
		}
	}

	replies := rig.mail.sentReplies()
	if got := replies[len(replies)-1].Text; got != "Here is the regional breakdown." {
		t.Fatalf("follow-up reply = %q", got)
	}
}

func TestFollowUpCheckpointFailureLeavesMessageReceived(t *testing.T) {
	rig := newTestRig(t)
	rig.mail.add(testMessage("msg-001", "thread-001", "Initial request."))
	rig.setAnswer("Initial answer.")
	mustProcess(t, rig)
	original := rig.session("thread-001")

	rig.mail.add(testMessage("msg-002", "thread-001", "Follow-up request."))
	t.Setenv("FAKE_AGENT_FORK_EXIT", "17")
	t.Setenv("FAKE_AGENT_FORK_ERROR", "checkpoint failed")
	err := rig.app.ProcessOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "checkpoint committed agent session") {
		t.Fatalf("ProcessOnce error = %v", err)
	}
	pending, err := rig.store.Pending()
	if err != nil || len(pending) != 1 {
		t.Fatalf("Pending = %+v, %v", pending, err)
	}
	if pending[0].MessageID != "msg-002" || pending[0].State != messageReceived ||
		pending[0].CheckpointSessionID != "" {
		t.Fatalf("pending after checkpoint failure = %+v", pending[0])
	}
	session := rig.session("thread-001")
	if session.SessionID != original.SessionID || session.Sequence != 1 {
		t.Fatalf("session after checkpoint failure = %+v", session)
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

func TestFollowUpUsesExtractedTextWithoutQuotedHistory(t *testing.T) {
	rig := newTestRig(t)
	rig.mail.add(testMessage("msg-020", "thread-004", "Draft a proposal."))
	rig.setAnswer("Here is the proposal draft.")
	mustProcess(t, rig)

	message := testMessage(
		"msg-024",
		"thread-004",
		"Focus Section 3 on costs.\n\n"+strings.Repeat("> quoted history\n", 10_000),
	)
	message.ExtractedText = "Focus Section 3 on costs."
	rig.mail.add(message)
	rig.setAnswer("I've updated section 3.")
	mustProcess(t, rig)

	prompt := rig.capture("text-2")
	if !strings.Contains(prompt, "Focus Section 3 on costs.") {
		t.Fatalf("prompt omitted extracted text:\n%s", prompt)
	}
	for _, unwanted := range []string{
		"[Previous messages in this thread:]",
		"Draft a proposal.",
		"Here is the proposal draft.",
		"> quoted history",
	} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt contains unwanted context %q", unwanted)
		}
	}
	if len(prompt) > 1_000 {
		t.Fatalf("prompt length = %d, want compact extracted message", len(prompt))
	}
	if got := rig.mail.sentReplies()[1].Text; got != "I've updated section 3." {
		t.Fatalf("reply = %q", got)
	}
}

func TestInterruptedMessageReplaysOnceWithoutSequenceGap(t *testing.T) {
	rig := newTestRig(t)
	rig.mail.add(testMessage(
		"msg-before-interruption",
		"thread-interrupted",
		"Prepare the initial report.",
	))
	rig.setAnswer("Initial report.")
	mustProcess(t, rig)

	message := testMessage(
		"msg-interrupted",
		"thread-interrupted",
		"Prepare the interrupted report.",
	)
	rig.mail.add(message)
	rig.setAnswer("Recovered report.")

	pending, existed, err := rig.store.BeginMessage(
		message.MessageID,
		message.ThreadID,
	)
	if err != nil {
		t.Fatalf("BeginMessage: %v", err)
	}
	if existed {
		t.Fatal("new inbound message reported as existing")
	}
	if pending.Session.Sequence != 2 {
		t.Fatalf("pending sequence = %d, want 2", pending.Session.Sequence)
	}
	if session := rig.session(message.ThreadID); session.Sequence != 1 {
		t.Fatalf("committed sequence before reply = %d, want 1", session.Sequence)
	}
	prompt := formatPrompt(rig.app.transport.(*Mailbox).normalize(message), pending.Session)
	if err := rig.store.MarkRunning(message.MessageID, prompt); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	rig.restartStore(t)
	mustProcess(t, rig)

	session := rig.session(message.ThreadID)
	if session.Sequence != 2 || session.Status != "completed" {
		t.Fatalf("unexpected recovered session: %+v", session)
	}
	if got := rig.capture("count"); got != "2" {
		t.Fatalf("machtiani run count = %q, want 2", got)
	}
	replayedPrompt := rig.capture("text-2")
	if count := strings.Count(replayedPrompt, "Prepare the interrupted report."); count != 1 {
		t.Fatalf("inbound prompt count = %d, want 1:\n%s", count, replayedPrompt)
	}
	if replayedPrompt != prompt {
		t.Fatalf("replayed prompt changed:\ngot:\n%s\nwant:\n%s", replayedPrompt, prompt)
	}
	if replies := rig.mail.sentReplies(); len(replies) != 2 {
		t.Fatalf("reply count = %d, want 2: %+v", len(replies), replies)
	}
	pendingMessages, err := rig.store.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pendingMessages) != 0 {
		t.Fatalf("pending messages after recovery: %+v", pendingMessages)
	}
}

func TestRestartRecoversAcceptedAgentResultWithoutDuplicatePrompt(t *testing.T) {
	rig := newTestRig(t)
	rig.mail.add(testMessage(
		"msg-before-accepted",
		"thread-accepted",
		"Prepare the initial report.",
	))
	rig.setAnswer("Initial report.")
	mustProcess(t, rig)

	message := testMessage(
		"msg-accepted",
		"thread-accepted",
		"Add the accepted follow-up.",
	)
	rig.mail.add(message)
	pending, _, err := rig.store.BeginMessage(message.MessageID, message.ThreadID)
	if err != nil {
		t.Fatalf("BeginMessage: %v", err)
	}
	prompt := formatPrompt(rig.app.transport.(*Mailbox).normalize(message), pending.Session)
	if err := rig.store.MarkRunning(message.MessageID, prompt); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	finalPath := recoveryResultPath(pending.Session.SessionID, message.MessageID)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		t.Fatalf("create recovery result directory: %v", err)
	}
	if err := os.WriteFile(finalPath, []byte("Recovered accepted result."), 0o600); err != nil {
		t.Fatalf("write recovery result: %v", err)
	}
	state, err := json.Marshal(sessionState{
		Status: "success",
		Goal:   prompt,
	})
	if err != nil {
		t.Fatalf("marshal session state: %v", err)
	}
	rig.setStatus(string(state))

	rig.restartStore(t)
	mustProcess(t, rig)

	session := rig.session(message.ThreadID)
	if session.Sequence != 2 || session.Status != "completed" {
		t.Fatalf("unexpected recovered session: %+v", session)
	}
	if got := rig.capture("count"); got != "1" {
		t.Fatalf("machtiani run count = %q, want initial run only", got)
	}
	replies := rig.mail.sentReplies()
	if len(replies) != 2 || replies[1].Text != "Recovered accepted result." {
		t.Fatalf("unexpected replies: %+v", replies)
	}
}

func TestRestartRecordsExistingOutboundReceiptWithoutRerun(t *testing.T) {
	rig := newTestRig(t)
	message := testMessage(
		"msg-receipted",
		"thread-receipted",
		"Prepare the receipted report.",
	)
	rig.mail.add(message)

	pending, _, err := rig.store.BeginMessage(message.MessageID, message.ThreadID)
	if err != nil {
		t.Fatalf("BeginMessage: %v", err)
	}
	if err := rig.store.MarkRunning(message.MessageID, "persisted prompt"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	result := RunResult{Kind: ResultAnswer, Text: "Already sent report."}
	if err := rig.store.StoreResult(message.MessageID, result); err != nil {
		t.Fatalf("StoreResult: %v", err)
	}
	if _, err := rig.app.transport.Reply(
		context.Background(),
		message.MessageID,
		ReplyPayload{Text: result.Text},
		idempotencyKey(pending.Session.SessionID, message.MessageID),
	); err != nil {
		t.Fatalf("Reply: %v", err)
	}

	rig.restartStore(t)
	mustProcess(t, rig)

	session := rig.session(message.ThreadID)
	if session.Sequence != 1 || session.Status != "completed" {
		t.Fatalf("unexpected recovered session: %+v", session)
	}
	if _, err := os.Stat(filepath.Join(rig.captureDir, "count")); !os.IsNotExist(err) {
		t.Fatalf("machtiani unexpectedly ran; stat error = %v", err)
	}
	if replies := rig.mail.sentReplies(); len(replies) != 1 {
		t.Fatalf("reply count = %d, want 1: %+v", len(replies), replies)
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
	if got := rig.capture("sync-count"); got != "1" {
		t.Fatalf("startup sync count = %q, want 1", got)
	}
	if got := rig.capture("events"); got != "sync\n" {
		t.Fatalf("startup machtiani calls = %q, want one sync", got)
	}
}

func TestRunLogsStartupAndGracefulShutdownCounts(t *testing.T) {
	rig := newTestRig(t)
	rig.mail.add(testMessage(
		"msg-lifecycle",
		"thread-lifecycle",
		"Exercise lifecycle logging.",
	))
	rig.setAnswer("Lifecycle logging exercised.")
	rig.app.pollInterval = time.Hour

	var logs strings.Builder
	rig.app.logger = log.New(&logs, "", 0)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- rig.app.Run(ctx)
	}()

	deadline := time.Now().Add(time.Second)
	for rig.mail.isUnread("msg-lifecycle") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if rig.mail.isUnread("msg-lifecycle") {
		cancel()
		t.Fatal("message was not processed before shutdown")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}

	output := logs.String()
	started := "DearMachine Client started. Polling test-inbox every 1h0m0s. Project: " +
		rig.app.runner.projectDir + "."
	for _, want := range []string{
		started,
		"DearMachine Client shutting down. Processed 1 messages across 1 threads.",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("lifecycle log missing %q:\n%s", want, output)
		}
	}
}

func TestRunCreatesAndRemovesPIDFile(t *testing.T) {
	rig := newTestRig(t)
	rig.app.pollInterval = time.Hour
	pidfile := filepath.Join(t.TempDir(), "run", "dearmachine.pid")
	rig.app.pidfile = pidfile

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- rig.app.Run(ctx)
	}()

	select {
	case <-rig.mail.polls:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("initial poll did not run")
	}
	content, err := os.ReadFile(pidfile)
	if err != nil {
		cancel()
		t.Fatalf("read pidfile: %v", err)
	}
	if got, want := string(content), strconv.Itoa(os.Getpid())+"\n"; got != want {
		cancel()
		t.Fatalf("pidfile content = %q, want %q", got, want)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	if _, err := os.Stat(pidfile); !os.IsNotExist(err) {
		t.Fatalf("pidfile remains after shutdown; stat error = %v", err)
	}
}

func TestRunDoesNotWritePIDFileWhenInitialSyncFails(t *testing.T) {
	rig := newTestRig(t)
	pidfile := filepath.Join(t.TempDir(), "run", "dearmachine.pid")
	rig.app.pidfile = pidfile
	rig.app.runner.binary = filepath.Join(t.TempDir(), "missing-machtiani")

	if err := rig.app.Run(context.Background()); err == nil {
		t.Fatal("Run succeeded with a missing machtiani binary")
	}
	if _, err := os.Stat(pidfile); !os.IsNotExist(err) {
		t.Fatalf("pidfile created before successful sync; stat error = %v", err)
	}
}

func TestRunOnceSyncsAndCleansUpPIDFile(t *testing.T) {
	rig := newTestRig(t)
	pidfile := filepath.Join(t.TempDir(), "run-once", "dearmachine.pid")
	rig.app.pidfile = pidfile

	if err := rig.app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := rig.capture("sync-count"); got != "1" {
		t.Fatalf("sync count = %q, want 1", got)
	}
	if _, err := os.Stat(filepath.Dir(pidfile)); err != nil {
		t.Fatalf("pidfile parent was not created: %v", err)
	}
	if _, err := os.Stat(pidfile); !os.IsNotExist(err) {
		t.Fatalf("pidfile remains after one-shot exit; stat error = %v", err)
	}
}

func TestVerboseLogsPollCyclesWhileDefaultIsSilent(t *testing.T) {
	rig := newTestRig(t)
	var logs strings.Builder
	rig.app.logger = log.New(&logs, "", 0)

	if err := rig.app.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("default ProcessOnce: %v", err)
	}
	if got := logs.String(); got != "" {
		t.Fatalf("default idle poll logged %q", got)
	}

	rig.app.verbose = true
	if err := rig.app.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("verbose ProcessOnce: %v", err)
	}
	if got := logs.String(); !strings.Contains(got, "poll: 0 unread messages") {
		t.Fatalf("verbose poll log = %q", got)
	}
}

func TestSkippedMessageRemainsUnreadUntilUnskipped(t *testing.T) {
	rig := newTestRig(t)
	message := testMessage("msg-skipped", "thread-skipped", "Do not run this yet.")
	rig.mail.add(message)
	if _, err := rig.store.SkipMessages(
		[]MessageRef{{MessageID: message.MessageID, ThreadID: message.ThreadID}},
		"live test",
	); err != nil {
		t.Fatalf("SkipMessages: %v", err)
	}

	mustProcess(t, rig)
	mustProcess(t, rig)
	if !rig.mail.isUnread(message.MessageID) {
		t.Fatal("skipped message was changed on AgentMail")
	}
	if replies := rig.mail.sentReplies(); len(replies) != 0 {
		t.Fatalf("skipped message received replies: %+v", replies)
	}
	if _, err := os.Stat(filepath.Join(rig.captureDir, "count")); !os.IsNotExist(err) {
		t.Fatalf("machtiani ran for skipped message; stat error = %v", err)
	}
	if _, err := rig.store.Session(message.ThreadID); err == nil {
		t.Fatal("skipped message created a thread session")
	}

	if err := rig.store.UnskipMessages([]string{message.MessageID}); err != nil {
		t.Fatalf("UnskipMessages: %v", err)
	}
	rig.setAnswer("Processed after unskip.")
	mustProcess(t, rig)
	if rig.mail.isUnread(message.MessageID) {
		t.Fatal("unskipped processed message remains unread")
	}
	if replies := rig.mail.sentReplies(); len(replies) != 1 {
		t.Fatalf("reply count after unskip = %d, want 1: %+v", len(replies), replies)
	}
	if got := rig.capture("count"); got != "1" {
		t.Fatalf("machtiani run count = %q, want 1", got)
	}
}

func newTestRig(t *testing.T) *testRig {
	return newTestRigWithModel(t, "test-model")
}

func TestEmptyModelUsesProjectDefault(t *testing.T) {
	rig := newTestRigWithModel(t, "  ")
	rig.mail.add(testMessage("msg-default", "thread-default", "Use the default model."))
	rig.setAnswer("Used the project default.")

	mustProcess(t, rig)

	args := rig.captureLines("args-1")
	if slices.Contains(args, "--model") {
		t.Fatalf("empty model unexpectedly forwarded --model: %v", args)
	}
}

func newTestRigWithModel(t *testing.T, model string) *testRig {
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
	dbPath := filepath.Join(t.TempDir(), "dearmachine.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
		mail.server.Close()
	})

	fixture, err := filepath.Abs(filepath.Join("testdata", "fake-agent.sh"))
	if err != nil {
		t.Fatalf("fixture path: %v", err)
	}
	captureDir := t.TempDir()
	statusFile := filepath.Join(t.TempDir(), "status.json")
	answerFile := filepath.Join(t.TempDir(), "answer.md")
	t.Setenv("FAKE_AGENT_CAPTURE", captureDir)
	t.Setenv("FAKE_AGENT_STATUS", statusFile)
	t.Setenv("FAKE_AGENT_ANSWER", answerFile)
	if err := os.WriteFile(statusFile, []byte(`{"status":"success"}`), 0o600); err != nil {
		t.Fatalf("write status: %v", err)
	}
	if err := os.WriteFile(answerFile, []byte("test answer"), 0o600); err != nil {
		t.Fatalf("write answer: %v", err)
	}

	runner, err := NewAgentRunner(fixture, t.TempDir(), model)
	if err != nil {
		t.Fatalf("NewAgentRunner: %v", err)
	}
	if err := runner.ConfigureAgentManaged([]string{"codex"}, "/test/agent-manager", nil); err != nil {
		t.Fatalf("ConfigureAgentManaged: %v", err)
	}
	app, err := New(
		mailbox,
		store,
		runner,
		nil,
		3,
		time.Minute,
		log.New(io.Discard, "", 0),
		false,
		"",
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
		dbPath:     dbPath,
	}
}

func newFakeAgentMail(t *testing.T) *fakeAgentMail {
	t.Helper()
	fake := &fakeAgentMail{
		t:        t,
		messages: make(map[string]agentmail.Message),
		unread:   make(map[string]bool),
		replyIDs: make(map[string]string),
		polls:    make(chan time.Time, 16),
		failures: make(map[string]fakeHTTPResponse),
	}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serveHTTP))
	return fake
}

func (f *fakeAgentMail) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	writer.Header().Set("Content-Type", "application/json")
	path := request.URL.Path
	if response, ok := f.failures[request.Method+" "+path]; ok {
		if response.delay > 0 {
			time.Sleep(response.delay)
		}
		if response.status != 0 {
			writer.WriteHeader(response.status)
		}
		if response.body != "" {
			_, _ = writer.Write([]byte(response.body))
		}
		return
	}
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
		idempotencyKey := request.Header.Get("Idempotency-Key")
		if receiptID, ok := f.replyIDs[idempotencyKey]; ok {
			f.writeJSON(writer, map[string]string{
				"message_id": receiptID,
				"thread_id":  f.messages[messageID].ThreadID,
			})
			return
		}
		receiptID := "reply-" + messageID
		f.replies = append(f.replies, sentReply{
			MessageID:      messageID,
			Text:           body.Text,
			IdempotencyKey: idempotencyKey,
		})
		f.replyIDs[idempotencyKey] = receiptID
		inbound := f.messages[messageID]
		f.messages[receiptID] = agentmail.Message{
			CreatedAt:  inbound.Timestamp.Add(time.Second),
			From:       "test-inbox",
			InReplyTo:  messageID,
			InboxID:    "test-inbox",
			Labels:     []string{"sent"},
			MessageID:  receiptID,
			References: append(slices.Clone(inbound.References), messageID),
			Subject:    "Re: " + inbound.Subject,
			Text:       body.Text,
			ThreadID:   inbound.ThreadID,
			Timestamp:  inbound.Timestamp.Add(time.Second),
			To:         []string{inbound.From},
			UpdatedAt:  inbound.Timestamp.Add(time.Second),
		}
		f.writeJSON(writer, map[string]string{
			"message_id": receiptID,
			"thread_id":  inbound.ThreadID,
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

func (f *fakeAgentMail) fail(method, path string, response fakeHTTPResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[method+" "+path] = response
}

func (f *fakeAgentMail) sentReplies() []sentReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.replies)
}

func (f *fakeAgentMail) isUnread(messageID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unread[messageID]
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

func (r *testRig) restartStore(t *testing.T) {
	t.Helper()
	if err := r.store.Close(); err != nil {
		t.Fatalf("close store before restart: %v", err)
	}
	store, err := OpenStore(r.dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
	})
	r.store = store
	r.app.store = store
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
