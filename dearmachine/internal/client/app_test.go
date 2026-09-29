package client

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
	// omitUnread simulates AgentMail's live behavior where a message carrying
	// the unread label is absent from labels=unread results.
	omitUnread map[string]bool
	replies    []sentReply
	replyIDs   map[string]string
	polls      chan time.Time
	failures   map[string]fakeHTTPResponse
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

	var runEnv []string
	rig.app.runner.invoke = func(command *exec.Cmd) error {
		if len(command.Args) > 1 && command.Args[1] == "run" {
			runEnv = append([]string(nil), command.Env...)
		}
		return command.Run()
	}

	if err := rig.app.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}

	session := rig.session("thread-001")
	if session.Sequence != 1 || session.Status != "completed" {
		t.Fatalf("unexpected session: %+v", session)
	}
	if !validConversationReference(session.SessionID) {
		t.Fatalf("SessionID = %q, want short canonical conversation reference", session.SessionID)
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
	turnKey := TurnKey(session.Sequence, message.MessageID)
	dirs, err := stagePaths(rig.app.runner.projectDir, turnKey)
	if err != nil {
		t.Fatalf("stagePaths: %v", err)
	}
	wantInbox := filepath.Join(dirs.Inbox, InboundManifestName())
	wantOutbox := dirs.Outbox
	if !strings.HasSuffix(wantInbox, InboundManifestName()) {
		t.Fatalf("inbox path %q does not end with %s", wantInbox, InboundManifestName())
	}
	if got := lookupEnv(runEnv, "DEARMACHINE_ATTACHMENTS_INBOX"); got != wantInbox {
		t.Fatalf("DEARMACHINE_ATTACHMENTS_INBOX = %q, want %q", got, wantInbox)
	}
	if got := lookupEnv(runEnv, "DEARMACHINE_ATTACHMENTS_OUTBOX"); got != wantOutbox {
		t.Fatalf("DEARMACHINE_ATTACHMENTS_OUTBOX = %q, want %q", got, wantOutbox)
	}
	if got := lookupEnv(runEnv, "DEARMACHINE_ATTACHMENTS_MANIFEST"); got != wantInbox {
		t.Fatalf("DEARMACHINE_ATTACHMENTS_MANIFEST = %q, want %q", got, wantInbox)
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
	if len(replies) != 1 {
		t.Fatalf("unexpected replies: %+v", replies)
	}
	assertReplyText(t, replies[0].Text, "Here is the Q3 sales report.")
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

func TestReplyFooterRendersFreshMagnificaHumanitasQuote(t *testing.T) {
	rig := newTestRig(t)
	rig.mail.add(testMessage("msg-magnifica", "thread-magnifica", "Answer humanely."))
	rig.setAnswer("A considered answer.")
	state, err := json.Marshal(sessionState{
		Status: "success",
		MagnificaHumanitas: &MagnificaHumanitas{
			Paragraph: 8,
			Line:      2,
			Quote:     "  Humanity is our finest work.  ",
		},
	})
	if err != nil {
		t.Fatalf("marshal session state: %v", err)
	}
	rig.setStatus(string(state))

	mustProcess(t, rig)

	replies := rig.mail.sentReplies()
	if len(replies) != 1 {
		t.Fatalf("replies = %+v, want one", replies)
	}
	wantFooter := conversationFooter(rig.session("thread-magnifica").SessionID) +
		"\n\n" + conversationFooterMotto + " quote:\n\"Humanity is our finest work.\""
	if !strings.HasSuffix(replies[0].Text, wantFooter) {
		t.Fatalf("reply footer = %q, want suffix %q", replies[0].Text, wantFooter)
	}
	if !strings.Contains(replies[0].Text, conversationFooterMotto) {
		t.Fatalf("reply omitted motto: %q", replies[0].Text)
	}
}

func TestMinimalFooterKeepsOnlySessionReference(t *testing.T) {
	rig := newTestRig(t)
	rig.app.SetMinimalFooter(true)
	rig.mail.add(testMessage("msg-minimal-footer", "thread-minimal-footer", "Answer briefly."))
	rig.setAnswer("A brief answer.")
	rig.setStatus(`{"status":"success","magnifica_humanitas":{"paragraph":1,"line":1,"quote":"Hidden quote."}}`)

	mustProcess(t, rig)

	reply := rig.mail.sentReplies()[0].Text
	want := "A brief answer.\n\n" + minimalConversationFooter(rig.session("thread-minimal-footer").SessionID)
	if reply != want {
		t.Fatalf("minimal reply = %q, want %q", reply, want)
	}
	for _, hidden := range []string{"Dear Machine:", conversationFooterRule, conversationFooterMotto, "Hidden quote"} {
		if strings.Contains(reply, hidden) {
			t.Fatalf("minimal footer retained %q: %q", hidden, reply)
		}
	}
}

func TestClaimSkipsOwnOutboundReply(t *testing.T) {
	rig := newTestRig(t)
	rig.mail.add(testMessage("msg-001", "thread-001", "Request."))
	rig.setAnswer("Answer.")
	mustProcess(t, rig)

	if replies := rig.mail.sentReplies(); len(replies) != 1 {
		t.Fatalf("replies = %d, want 1", len(replies))
	}
	rig.mail.unread["reply-msg-001"] = true

	if err := rig.app.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	if replies := rig.mail.sentReplies(); len(replies) != 1 {
		t.Fatalf("replies = %d, want 1", len(replies))
	}
	pending, err := rig.store.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %+v, want empty", pending)
	}
	seen, err := rig.store.Seen("reply-msg-001")
	if err != nil {
		t.Fatalf("Seen: %v", err)
	}
	if seen {
		t.Fatal("own outbound reply recorded as processed inbound message")
	}
	if rig.mail.isUnread("reply-msg-001") {
		t.Fatal("own outbound reply remains unread")
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
	assertArg(t, rig.captureLines("args-2"), "--resume", original.SessionID)
	if got := rig.capture("session-env-2"); got != "" {
		t.Fatalf("resumed run set MACHTIANI_SESSION_ID = %q", got)
	}
	if got := rig.capture("forked-sessions"); got != original.SessionID+"\n" {
		t.Fatalf("checkpointed sessions = %q, want original session", got)
	}
	if got := rig.capture("deleted-sessions"); got != os.Getenv("FAKE_AGENT_FORK_ID")+"\n" {
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
	assertReplyText(t, replies[len(replies)-1].Text, "Here is the regional breakdown.")
}

func TestShortSessionFooterWithoutAncestryRequiresForkConfirmation(t *testing.T) {
	rig := newTestRig(t)
	rig.mail.add(testMessage("msg-short-footer-1", "provider-thread-short-a", "Start the report."))
	rig.setAnswer("Initial report.")
	mustProcess(t, rig)

	first := rig.session("provider-thread-short-a")
	firstReply := rig.mail.sentReplies()[0].Text
	if !strings.Contains(firstReply, "\nDear Machine:\nsession dm1-") {
		t.Fatalf("first reply omitted short session footer:\n%s", firstReply)
	}

	rig.restartStore(t)
	reply := testMessage(
		"msg-short-footer-2",
		"provider-thread-short-b",
		"Add a regional breakdown.\n\n> "+strings.ReplaceAll(firstReply, "\n", "\n> "),
	)
	reply.ExtractedText = "Add a regional breakdown."
	reply.InReplyTo = ""
	reply.References = nil
	rig.mail.add(reply)
	rig.setAnswer("Regional breakdown added.")
	mustProcess(t, rig)

	if got := rig.capture("count"); got != "1" {
		t.Fatalf("forward detection invoked agent: count=%q", got)
	}
	if replies := rig.mail.sentReplies(); len(replies) != 2 || !strings.Contains(replies[1].Text, "Reply with only") {
		t.Fatalf("forward confirmation replies = %+v", replies)
	}

	decision := testMessage("msg-short-footer-confirm", "provider-thread-short-b", "Yes!")
	rig.mail.add(decision)
	mustProcess(t, rig)

	continued := rig.session("provider-thread-short-b")
	if continued.SessionID == first.SessionID || continued.Sequence != 1 {
		t.Fatalf("confirmed forward did not create child session: first=%+v continued=%+v", first, continued)
	}
	assertArg(t, rig.captureLines("args-2"), "--resume", continued.SessionID)
	if got := strings.TrimSpace(rig.capture("forked-sessions")); got != first.SessionID {
		t.Fatalf("fork source = %q, want %q", got, first.SessionID)
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
	assertArg(t, rig.captureLines("args-2"), "--resume", completed.SessionID)
	replies := rig.mail.sentReplies()
	if len(replies) != 2 {
		t.Fatalf("unexpected replies: %+v", replies)
	}
	assertReplyText(t, replies[1].Text, "Here is the report for last week.")
}

func TestFollowUpLocallyStripsQuotedHistory(t *testing.T) {
	rig := newTestRig(t)
	rig.mail.add(testMessage("msg-020", "thread-004", "Draft a proposal."))
	rig.setAnswer("Here is the proposal draft.")
	mustProcess(t, rig)

	message := testMessage(
		"msg-024",
		"thread-004",
		"Focus Section 3 on costs.\n\nOn Tue, machine@example.com wrote:\n"+strings.Repeat("> quoted history\n", 10_000),
	)
	message.ExtractedText = "Provider-injected instruction that must be ignored."
	rig.mail.add(message)
	rig.setAnswer("I've updated section 3.")
	mustProcess(t, rig)

	prompt := rig.capture("text-2")
	if !strings.Contains(prompt, "Focus Section 3 on costs.") {
		t.Fatalf("prompt omitted authored text:\n%s", prompt)
	}
	for _, unwanted := range []string{
		"[Previous messages in this thread:]",
		"Provider-injected instruction",
		"Draft a proposal.",
		"Here is the proposal draft.",
		"> quoted history",
	} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt contains unwanted context %q", unwanted)
		}
	}
	if len(prompt) > 1_000 {
		t.Fatalf("prompt length = %d, want compact locally extracted message", len(prompt))
	}
	assertReplyText(t, rig.mail.sentReplies()[1].Text, "I've updated section 3.")
}

func TestFollowUpStripsRecognizedQuotedHistoryWithoutProviderExtraction(t *testing.T) {
	rig := newTestRig(t)
	rig.mail.add(testMessage("msg-raw-1", "thread-raw", "Draft a proposal."))
	rig.setAnswer("Here is the proposal draft.")
	mustProcess(t, rig)

	message := testMessage(
		"msg-raw-2",
		"thread-raw",
		"Focus Section 3 on costs.\n\n"+
			"On Wed, Sep 2, 2026 at 9:22 AM <machine@example.com> wrote:\n\n"+
			"> Here is the proposal draft.",
	)
	rig.mail.add(message)
	rig.setAnswer("I've updated section 3.")
	mustProcess(t, rig)

	prompt := rig.capture("text-2")
	if !strings.Contains(prompt, "Focus Section 3 on costs.") {
		t.Fatalf("prompt omitted new reply:\n%s", prompt)
	}
	for _, unwanted := range []string{"On Wed,", "machine@example.com", "Here is the proposal draft."} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt contains quoted history %q:\n%s", unwanted, prompt)
		}
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
		TierPlain,
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
	rig.setStatus(`{"status":"in_progress","goal":` + strconv.Quote(prompt) + `}`)
	rig.app.runner.invoke = func(command *exec.Cmd) error {
		if len(command.Args) > 1 && command.Args[1] == "run" {
			rig.setStatus(`{"status":"success"}`)
		}
		return command.Run()
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
	args := rig.captureLines("args-2")
	if !slices.Contains(args, "--resume") || slices.Contains(args, "--prompt") {
		t.Fatalf("recovery args = %v, want --resume without --prompt", args)
	}
	if got := rig.capture("text-2"); got != "" {
		t.Fatalf("recovery prompt = %q, want empty", got)
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
	pending, _, err := rig.store.BeginMessage(message.MessageID, message.ThreadID, TierPlain)
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
	if len(replies) != 2 {
		t.Fatalf("unexpected replies: %+v", replies)
	}
	assertReplyText(t, replies[1].Text, "Recovered accepted result.")
}

func TestRestartRecordsExistingOutboundReceiptWithoutRerun(t *testing.T) {
	rig := newTestRig(t)
	message := testMessage(
		"msg-receipted",
		"thread-receipted",
		"Prepare the receipted report.",
	)
	rig.mail.add(message)

	pending, _, err := rig.store.BeginMessage(message.MessageID, message.ThreadID, TierPlain)
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

func TestRestartResumesAttachmentTurnWithoutDuplicateRun(t *testing.T) {
	fake := newFakeTransportFixture()
	rig := newTestRigTransport(t, TierFormatted, fake, "test-model")
	message := fake.poll[0]
	rig.setAnswer("Formatted answer.")
	t.Setenv("FAKE_AGENT_OUTBOX_FILE", "out.txt")
	t.Setenv("FAKE_AGENT_OUTBOX_CONTENT", "frozen outbox data")

	fake.errors.Reply = errors.New("simulated outage")
	err := rig.app.ProcessOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "simulated outage") {
		t.Fatalf("first ProcessOnce error = %v, want simulated outage", err)
	}

	pending, found, err := rig.store.PendingByID(message.MessageID)
	if err != nil || !found {
		t.Fatalf("PendingByID = %+v, %v; want found pending", pending, err)
	}
	if pending.State != messageResultReady {
		t.Fatalf("pending state = %q, want %q", pending.State, messageResultReady)
	}
	if strings.TrimSpace(pending.ResultManifest) == "" {
		t.Fatal("pending result manifest is empty after the simulated crash")
	}
	if got := rig.capture("count"); got != "1" {
		t.Fatalf("machtiani run count = %q, want 1", got)
	}

	turnKey := TurnKey(pending.Session.Sequence, message.MessageID)
	inbox := filepath.Join(rig.app.runner.projectDir, ".attachments-inbox", turnKey)
	stagedReport, err := os.ReadFile(filepath.Join(inbox, "request.txt"))
	if err != nil {
		t.Fatalf("read staged request.txt: %v", err)
	}
	if string(stagedReport) != "payload" {
		t.Fatalf("staged request.txt = %q, want %q", stagedReport, "payload")
	}
	if _, err := os.Stat(filepath.Join(inbox, InboundManifestName())); err != nil {
		t.Fatalf("staged inbound manifest: %v", err)
	}

	fake.errors.Reply = nil
	rig.restartStore(t)
	mustProcess(t, rig)

	if len(fake.replies) != 1 {
		t.Fatalf("replies = %+v, want exactly 1", fake.replies)
	}
	reply := fake.replies[0]
	if len(reply.Files) != 1 {
		t.Fatalf("reply files = %+v, want 1", reply.Files)
	}
	if reply.Files[0].Filename != "out.txt" {
		t.Fatalf("outbound filename = %q, want out.txt", reply.Files[0].Filename)
	}
	if string(reply.Files[0].Contents) != "frozen outbox data" {
		t.Fatalf("outbound contents = %q, want %q", reply.Files[0].Contents, "frozen outbox data")
	}
	if got := rig.capture("count"); got != "1" {
		t.Fatalf("machtiani run count after restart = %q, want 1", got)
	}
	session := rig.session(message.ThreadID)
	if session.Sequence != 1 || session.Status != "completed" {
		t.Fatalf("recovered session = %+v, want sequence 1 completed", session)
	}
	if _, err := os.Stat(filepath.Join(rig.app.runner.projectDir, ".attachments-inbox", turnKey)); !os.IsNotExist(err) {
		t.Fatalf("inbox staging dir remains after completion; stat error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(rig.app.runner.projectDir, ".attachments-outbox", turnKey)); !os.IsNotExist(err) {
		t.Fatalf("outbox staging dir remains after completion; stat error = %v", err)
	}
}

func TestResultReadyFooterUsesDurableMagnificaHumanitas(t *testing.T) {
	tests := []struct {
		name      string
		magnifica *MagnificaHumanitas
		wantQuote string
	}{
		{
			name: "stored quote",
			magnifica: &MagnificaHumanitas{
				Paragraph: 12,
				Line:      1,
				Quote:     "  The recovery path keeps this line.  ",
			},
			wantQuote: "Magnifica Humanitas quote:\n\"The recovery path keeps this line.\"",
		},
		{
			name:      "malformed empty durable quote",
			magnifica: &MagnificaHumanitas{Paragraph: -1, Line: -1, Quote: " \t\n "},
		},
		{
			name: "nil durable quote",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeTransportFixture()
			rig := newTestRigTransport(t, TierPlain, fake, "test-model")
			rig.setAnswer("Recovered answer.")
			state, err := json.Marshal(sessionState{
				Status:             "success",
				MagnificaHumanitas: test.magnifica,
			})
			if err != nil {
				t.Fatalf("marshal session state: %v", err)
			}
			rig.setStatus(string(state))
			fake.errors.Reply = errors.New("simulated outage")
			if err := rig.app.ProcessOnce(context.Background()); err == nil ||
				!strings.Contains(err.Error(), "simulated outage") {
				t.Fatalf("first ProcessOnce error = %v, want simulated outage", err)
			}

			pending, found, err := rig.store.PendingByID(fake.poll[0].MessageID)
			if err != nil || !found || pending.State != messageResultReady {
				t.Fatalf("PendingByID = %+v, %v; want result_ready pending", pending, err)
			}
			rig.restartStore(t)
			fake.errors.Reply = nil
			mustProcess(t, rig)

			if len(fake.replies) != 1 {
				t.Fatalf("replies = %+v, want one recovered reply", fake.replies)
			}
			reply := fake.replies[0].Text
			if test.wantQuote != "" && !strings.HasSuffix(reply, "\n\n"+test.wantQuote) {
				t.Fatalf("recovered reply = %q, want quote suffix %q", reply, test.wantQuote)
			}
			if test.wantQuote == "" && strings.Contains(strings.ToLower(reply), "\nquote:") {
				t.Fatalf("recovered reply rendered empty quote label: %q", reply)
			}
			if gotMotto, wantMotto := strings.Contains(reply, conversationFooterMotto), test.wantQuote != ""; gotMotto != wantMotto {
				t.Fatalf("recovered reply motto presence = %t, want %t: %q", gotMotto, wantMotto, reply)
			}
		})
	}
}

func TestRestartFailsClosedOnTamperedOutbox(t *testing.T) {
	fake := newFakeTransportFixture()
	rig := newTestRigTransport(t, TierFormatted, fake, "test-model")
	message := fake.poll[0]
	rig.setAnswer("Formatted answer.")
	t.Setenv("FAKE_AGENT_OUTBOX_FILE", "out.txt")
	t.Setenv("FAKE_AGENT_OUTBOX_CONTENT", "frozen outbox data")

	fake.errors.Reply = errors.New("simulated outage")
	if err := rig.app.ProcessOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "simulated outage") {
		t.Fatalf("first ProcessOnce error = %v, want simulated outage", err)
	}
	pending, found, err := rig.store.PendingByID(message.MessageID)
	if err != nil || !found || pending.State != messageResultReady {
		t.Fatalf("PendingByID = %+v, %v; want result_ready pending", pending, err)
	}
	if strings.TrimSpace(pending.ResultManifest) == "" {
		t.Fatal("pending result manifest is empty after the simulated crash")
	}

	turnKey := TurnKey(pending.Session.Sequence, message.MessageID)
	outboxFile := filepath.Join(
		rig.app.runner.projectDir,
		".attachments-outbox",
		turnKey,
		"out.txt",
	)
	if err := os.WriteFile(outboxFile, []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper staged outbox file: %v", err)
	}

	fake.errors.Reply = nil
	rig.restartStore(t)
	err = rig.app.ProcessOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("recovery ProcessOnce error = %v, want outbox verification failure", err)
	}
	if len(fake.replies) != 0 {
		t.Fatalf("replies = %+v, want none after tampered outbox", fake.replies)
	}
	pending, found, err = rig.store.PendingByID(message.MessageID)
	if err != nil || !found || pending.State != messageResultReady {
		t.Fatalf("PendingByID after failed recovery = %+v, %v; want still result_ready", pending, err)
	}
}

func TestRestartReceiptPathSkipsRerunAndCleansStaging(t *testing.T) {
	fake := newFakeTransportFixture()
	rig := newTestRigTransport(t, TierFormatted, fake, "test-model")
	message := fake.poll[0]

	pending, _, err := rig.store.BeginMessage(message.MessageID, message.ThreadID, TierFormatted)
	if err != nil {
		t.Fatalf("BeginMessage: %v", err)
	}
	turnKey := TurnKey(pending.Session.Sequence, message.MessageID)
	dirs, err := stagePaths(rig.app.runner.projectDir, turnKey)
	if err != nil {
		t.Fatalf("stagePaths: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirs.Inbox, "request.txt"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	outbound := []byte("frozen outbox data")
	if err := os.WriteFile(filepath.Join(dirs.Outbox, "out.txt"), outbound, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := OutboundManifest{
		TurnKey: turnKey,
		Files: []StagedArtifact{{
			Filename:    "out.txt",
			ContentType: "text/plain",
			SizeBytes:   int64(len(outbound)),
			SHA256:      sha256Hex(outbound),
		}},
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal outbound manifest: %v", err)
	}
	if err := rig.store.MarkRunningWithCheckpoint(
		message.MessageID,
		"persisted prompt",
		newConversationReference(),
	); err != nil {
		t.Fatalf("MarkRunningWithCheckpoint: %v", err)
	}
	result := RunResult{Kind: ResultAnswer, Text: "Already sent report."}
	if err := rig.store.StoreResultWithManifest(message.MessageID, result, string(manifestJSON)); err != nil {
		t.Fatalf("StoreResultWithManifest: %v", err)
	}
	if _, err := rig.app.transport.Reply(
		context.Background(),
		message.MessageID,
		ReplyPayload{
			Text:  result.Text,
			HTML:  replyHTML(result.Text),
			Files: []OutboundFile{{Filename: "out.txt", ContentType: "text/plain", Contents: outbound}},
		},
		idempotencyKey(pending.Session.SessionID, message.MessageID),
	); err != nil {
		t.Fatalf("Reply: %v", err)
	}

	rig.restartStore(t)
	mustProcess(t, rig)

	session := rig.session(message.ThreadID)
	if session.Sequence != 1 || session.Status != "completed" {
		t.Fatalf("recovered session = %+v, want sequence 1 completed", session)
	}
	if len(fake.replies) != 1 {
		t.Fatalf("reply count = %d, want 1: %+v", len(fake.replies), fake.replies)
	}
	if _, err := os.Stat(filepath.Join(rig.captureDir, "count")); !os.IsNotExist(err) {
		t.Fatalf("machtiani ran during receipt recovery; stat error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(rig.app.runner.projectDir, ".attachments-inbox", turnKey)); !os.IsNotExist(err) {
		t.Fatalf("inbox staging dir remains after receipt recovery; stat error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(rig.app.runner.projectDir, ".attachments-outbox", turnKey)); !os.IsNotExist(err) {
		t.Fatalf("outbox staging dir remains after receipt recovery; stat error = %v", err)
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

func TestSkippedMessageIsMarkedReadAndCanBeRestored(t *testing.T) {
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
	if rig.mail.isUnread(message.MessageID) {
		t.Fatal("skipped message remains unread on AgentMail")
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

	if supported, err := SetMessageRead(context.Background(), rig.app.transport, message.MessageID, false); err != nil || !supported {
		t.Fatalf("restore unread: supported=%v err=%v", supported, err)
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

func TestFormatPromptIncludesTierAndAttachmentMetadata(t *testing.T) {
	message := Message{
		From:     "sender@example.com",
		ThreadID: "thread-1",
		Subject:  "Request",
		Body:     "Please review.",
		Attachments: []AttachmentRef{{
			AttachmentID: "a-1",
			Filename:     "report.pdf",
			ContentType:  "application/pdf",
			SizeBytes:    42,
		}},
	}
	session := Session{
		ThreadID:     "thread-1",
		SessionID:    testCanonicalConversationReference,
		Sequence:     2,
		ResponseTier: TierFormatted,
	}
	prompt := formatPrompt(message, session)
	for _, want := range []string{
		"[Response tier: formatted]",
		"[Attachment: report.pdf (application/pdf, 42 bytes)]",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("formatted prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "not staged") {
		t.Errorf("formatted prompt unexpectedly mentions unstaged bodies:\n%s", prompt)
	}

	session.ResponseTier = TierPlain
	plain := formatPrompt(message, session)
	if !strings.Contains(plain, "[Attachment bodies are not staged for the plain response tier]") {
		t.Fatalf("plain prompt missing unstaged-body note:\n%s", plain)
	}
}

func TestFormatPromptHeaderUsesShortCanonicalSessionID(t *testing.T) {
	sessionID := testCanonicalConversationReference
	prompt := formatPrompt(
		Message{MessageID: "message-1", ThreadID: "thread-1", Body: "Please continue."},
		Session{ThreadID: "thread-1", SessionID: sessionID, Sequence: 2},
	)
	want := "[Thread: thread-1 | Session: " + sessionID + " | Sequence: 2]"
	if !strings.Contains(prompt, want) {
		t.Fatalf("prompt missing short canonical session header %q:\n%s", want, prompt)
	}
}

func TestReplyHTMLEscapesContent(t *testing.T) {
	got := replyHTML(`Hello & goodbye <script>alert("x")</script>`)
	if !strings.HasPrefix(got, "<!DOCTYPE html>") {
		t.Fatalf("replyHTML did not start with <!DOCTYPE html>:\n%s", got)
	}
	if !strings.Contains(got, "white-space: pre-wrap") {
		t.Fatalf("replyHTML missing white-space: pre-wrap:\n%s", got)
	}
	if !strings.Contains(got, "Hello &amp; goodbye") {
		t.Fatalf("replyHTML missing escaped ampersand:\n%s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;") {
		t.Fatalf("replyHTML missing escaped script:\n%s", got)
	}
	if strings.Contains(got, "<script>") {
		t.Fatalf("replyHTML leaked raw <script>:\n%s", got)
	}
}

func TestPlainTierSendsNoAttachmentsOrHTML(t *testing.T) {
	fake := newFakeTransport()
	message := Message{
		MessageID: "plain-1",
		ThreadID:  "thread-plain",
		From:      "sender@example.com",
		To:        []string{"device@example.com"},
		CreatedAt: time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC),
		Subject:   "Request",
		Body:      "Please handle this request.",
		Labels:    []string{"unread"},
		Attachments: []AttachmentRef{{
			AttachmentID: "missing-attachment",
			Filename:     "in.txt",
			ContentType:  "text/plain",
			SizeBytes:    3,
		}},
	}
	fake.setPoll([]Message{message})
	rig := newTestRigTransport(t, TierPlain, fake, "test-model")
	rig.setAnswer("plain answer")
	t.Setenv("FAKE_AGENT_OUTBOX_FILE", "out.txt")

	var inboxExists bool
	var inboxNames []string
	rig.app.runner.invoke = func(command *exec.Cmd) error {
		if err := command.Run(); err != nil {
			return err
		}
		if len(command.Args) > 1 && command.Args[1] == "run" {
			inbox := filepath.Join(
				rig.app.runner.projectDir,
				".attachments-inbox",
				TurnKey(1, message.MessageID),
			)
			if info, err := os.Stat(inbox); err == nil && info.IsDir() {
				inboxExists = true
			}
			entries, err := os.ReadDir(inbox)
			if err == nil {
				for _, entry := range entries {
					inboxNames = append(inboxNames, entry.Name())
				}
			}
		}
		return nil
	}

	if err := rig.app.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}

	if len(fake.replies) != 1 {
		t.Fatalf("replies = %+v, want exactly 1", fake.replies)
	}
	reply := fake.replies[0]
	assertReplyText(t, reply.Text, "plain answer")
	if reply.HTML != "" {
		t.Fatalf("plain reply HTML = %q, want empty", reply.HTML)
	}
	if len(reply.Files) != 0 {
		t.Fatalf("plain reply files = %+v, want none", reply.Files)
	}
	if got := rig.capture("count"); got != "1" {
		t.Fatalf("machtiani run count = %q, want 1", got)
	}
	if got := rig.capture("events"); got != "sync\nrun\n" {
		t.Fatalf("machtiani call order = %q, want sync then run", got)
	}
	if !inboxExists {
		t.Fatal("plain-tier inbox turn directory did not exist during the agent run")
	}
	if len(inboxNames) != 0 {
		t.Fatalf("plain-tier inbox entries during run = %v, want empty", inboxNames)
	}
	if found := inboundManifestPaths(t, rig.app.runner.projectDir); len(found) != 0 {
		t.Fatalf("plain tier wrote inbound manifests: %v", found)
	}
}

func TestFormattedTierStagesInboundAndAttachesOutboxFiles(t *testing.T) {
	fake := newFakeTransport()
	message := Message{
		MessageID: "formatted-1",
		ThreadID:  "thread-formatted",
		From:      "sender@example.com",
		To:        []string{"device@example.com"},
		CreatedAt: time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC),
		Subject:   "Request",
		Body:      "Please handle this request.",
		Labels:    []string{"unread"},
		Attachments: []AttachmentRef{{
			AttachmentID: "attachment-1",
			Filename:     "  report.txt ",
			ContentType:  "application/octet-stream",
			SizeBytes:    7,
		}},
	}
	fake.setPoll([]Message{message})
	fake.attachments["attachment-1"] = []byte("payload")
	rig := newTestRigTransport(t, TierFormatted, fake, "test-model")
	const answer = "answer <tag> & more"
	rig.setAnswer(answer)
	t.Setenv("FAKE_AGENT_OUTBOX_FILE", "result.txt")
	t.Setenv("FAKE_AGENT_OUTBOX_CONTENT", "outbox data")

	var stagedReport []byte
	var stagedManifest []byte
	rig.app.runner.invoke = func(command *exec.Cmd) error {
		if err := command.Run(); err != nil {
			return err
		}
		if len(command.Args) > 1 && command.Args[1] == "run" {
			inbox := filepath.Join(
				rig.app.runner.projectDir,
				".attachments-inbox",
				TurnKey(1, message.MessageID),
			)
			var err error
			stagedReport, err = os.ReadFile(filepath.Join(inbox, "report.txt"))
			if err != nil {
				t.Errorf("read staged report.txt: %v", err)
			}
			stagedManifest, err = os.ReadFile(filepath.Join(inbox, InboundManifestName()))
			if err != nil {
				t.Errorf("read staged inbound manifest: %v", err)
			}
		}
		return nil
	}

	if err := rig.app.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}

	if len(fake.replies) != 1 {
		t.Fatalf("replies = %+v, want exactly 1", fake.replies)
	}
	reply := fake.replies[0]
	assertReplyText(t, reply.Text, answer)
	if reply.HTML == "" {
		t.Fatal("formatted reply HTML is empty")
	}
	if !strings.Contains(reply.HTML, "answer &lt;tag&gt; &amp; more") {
		t.Fatalf("formatted reply HTML missing escaped answer:\n%s", reply.HTML)
	}
	if len(reply.Files) != 1 {
		t.Fatalf("formatted reply files = %+v, want 1", reply.Files)
	}
	if reply.Files[0].Filename != "result.txt" {
		t.Fatalf("outbound filename = %q, want result.txt", reply.Files[0].Filename)
	}
	if string(reply.Files[0].Contents) != "outbox data" {
		t.Fatalf("outbound contents = %q, want %q", reply.Files[0].Contents, "outbox data")
	}
	if reply.Files[0].ContentType != "text/plain" {
		t.Fatalf("outbound content type = %q, want text/plain", reply.Files[0].ContentType)
	}

	if string(stagedReport) != "payload" {
		t.Fatalf("staged report.txt = %q, want %q", stagedReport, "payload")
	}
	var manifest InboundManifest
	if err := json.Unmarshal(stagedManifest, &manifest); err != nil {
		t.Fatalf("parse inbound manifest: %v", err)
	}
	if len(manifest.Files) != 1 {
		t.Fatalf("inbound manifest files = %+v, want 1 entry", manifest.Files)
	}
	sum := sha256.Sum256([]byte("payload"))
	wantSHA := hex.EncodeToString(sum[:])
	if manifest.Files[0].SHA256 != wantSHA {
		t.Fatalf("inbound manifest SHA256 = %q, want %q", manifest.Files[0].SHA256, wantSHA)
	}
}

func TestCompleteTierPackagesOneDeterministicZIP(t *testing.T) {
	fake := newFakeTransport()
	message := Message{
		MessageID: "complete-1",
		ThreadID:  "thread-complete",
		From:      "sender@example.com",
		To:        []string{"device@example.com"},
		CreatedAt: time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC),
		Subject:   "Request",
		Body:      "Please handle this request.",
		Labels:    []string{"unread"},
	}
	fake.setPoll([]Message{message})
	rig := newTestRigTransport(t, TierComplete, fake, "test-model")
	rig.setAnswer("complete answer")
	t.Setenv("FAKE_AGENT_OUTBOX_FILE", "notes.txt")
	t.Setenv("FAKE_AGENT_OUTBOX_CONTENT", "zip me")

	if err := rig.app.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}

	if len(fake.replies) != 1 {
		t.Fatalf("replies = %+v, want exactly 1", fake.replies)
	}
	reply := fake.replies[0]
	if len(reply.Files) != 1 {
		t.Fatalf("complete reply files = %+v, want 1", reply.Files)
	}
	if reply.Files[0].Filename != AttachmentArchiveName() {
		t.Fatalf("archive filename = %q, want %q", reply.Files[0].Filename, AttachmentArchiveName())
	}
	if reply.Files[0].ContentType != "application/zip" {
		t.Fatalf("archive content type = %q, want application/zip", reply.Files[0].ContentType)
	}

	reader, err := zip.NewReader(bytes.NewReader(reply.Files[0].Contents), int64(len(reply.Files[0].Contents)))
	if err != nil {
		t.Fatalf("open attachment archive: %v", err)
	}
	wantNames := []string{"manifest.json", "notes.txt"}
	if len(reader.File) != len(wantNames) {
		t.Fatalf("archive entries = %d, want %d", len(reader.File), len(wantNames))
	}
	wantTime := time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	sum := sha256.Sum256([]byte("zip me"))
	wantSHA := hex.EncodeToString(sum[:])
	for i, file := range reader.File {
		if file.Name != wantNames[i] {
			t.Fatalf("entry[%d] = %q, want %q", i, file.Name, wantNames[i])
		}
		if !file.Modified.Equal(wantTime) {
			t.Fatalf("entry %s Modified = %v, want %v", file.Name, file.Modified, wantTime)
		}
		rc, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		contents, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", file.Name, err)
		}
		switch file.Name {
		case "manifest.json":
			var manifest OutboundManifest
			if err := json.Unmarshal(contents, &manifest); err != nil {
				t.Fatalf("parse archive manifest: %v", err)
			}
			if manifest.TurnKey != TurnKey(1, message.MessageID) {
				t.Fatalf("manifest turn key = %q, want %q", manifest.TurnKey, TurnKey(1, message.MessageID))
			}
			if len(manifest.Files) != 1 || manifest.Files[0].Filename != "notes.txt" {
				t.Fatalf("manifest files = %+v, want notes.txt", manifest.Files)
			}
			if manifest.Files[0].SHA256 != wantSHA {
				t.Fatalf("manifest SHA256 = %q, want %q", manifest.Files[0].SHA256, wantSHA)
			}
		case "notes.txt":
			if string(contents) != "zip me" {
				t.Fatalf("notes.txt = %q, want %q", contents, "zip me")
			}
		}
	}
}

func inboundManifestPaths(t *testing.T, projectDir string) []string {
	t.Helper()
	root := filepath.Join(projectDir, ".attachments-inbox")
	var found []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.Name() == InboundManifestName() {
			found = append(found, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walk inbox: %v", err)
	}
	return found
}

func newTestRig(t *testing.T) *testRig {
	return newTestRigTransport(t, TierPlain, nil, "test-model")
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
	return newTestRigTransport(t, TierPlain, nil, model)
}

func newTestRigTransport(t *testing.T, tier ResponseTier, transport Transport, model string) *testRig {
	t.Helper()
	var mail *fakeAgentMail
	if transport == nil {
		mail = newFakeAgentMail(t)
		client := agentmail.NewClient(
			option.WithBaseURL(mail.server.URL+"/"),
			option.WithAPIKey("test-key"),
		)
		mailbox, err := NewMailbox(client, "test-inbox")
		if err != nil {
			t.Fatalf("NewMailbox: %v", err)
		}
		transport = mailbox
	}
	dbPath := filepath.Join(t.TempDir(), "dearmachine.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
		if mail != nil {
			mail.server.Close()
		}
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
	t.Setenv("FAKE_AGENT_FORK_ID", newConversationReference())
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
		transport,
		store,
		runner,
		nil,
		3,
		time.Minute,
		log.New(io.Discard, "", 0),
		false,
		"",
		tier,
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
		t:          t,
		messages:   make(map[string]agentmail.Message),
		unread:     make(map[string]bool),
		omitUnread: make(map[string]bool),
		replyIDs:   make(map[string]string),
		polls:      make(chan time.Time, 16),
		failures:   make(map[string]fakeHTTPResponse),
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
		f.serveList(writer, request)
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
		var update struct {
			AddLabels    string `json:"add_labels"`
			RemoveLabels string `json:"remove_labels"`
		}
		if err := json.NewDecoder(request.Body).Decode(&update); err != nil {
			f.t.Fatal(err)
		}
		f.unread[messageID] = update.AddLabels == "unread"
		f.writeJSON(writer, map[string]any{
			"message_id": messageID,
			"labels":     []string{update.AddLabels},
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

func (f *fakeAgentMail) serveList(writer http.ResponseWriter, request *http.Request) {
	if values, present := request.URL.Query()["page_token"]; present &&
		(len(values) == 0 || strings.TrimSpace(values[0]) == "") {
		writer.WriteHeader(http.StatusBadRequest)
		f.writeJSON(writer, map[string]string{"error": "empty page token"})
		return
	}
	filteredUnread := request.URL.Query().Get("labels") != ""
	if filteredUnread {
		f.polls <- time.Now()
	}
	var messages []agentmail.Message
	for id, message := range f.messages {
		if containsFold(message.Labels, "unauthenticated") && request.URL.Query().Get("include_unauthenticated") != "true" {
			continue
		}
		if filteredUnread && (!f.unread[id] || f.omitUnread[id]) {
			continue
		}
		message.Labels = slices.DeleteFunc(slices.Clone(message.Labels), func(label string) bool {
			return strings.EqualFold(label, "read") || strings.EqualFold(label, "unread")
		})
		if f.unread[id] {
			message.Labels = append(message.Labels, "unread")
		} else {
			message.Labels = append(message.Labels, "read")
		}
		messages = append(messages, message)
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

func (f *fakeAgentMail) omitFromUnreadFilter(messageID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.omitUnread[messageID] = true
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
	if binder, ok := r.app.transport.(interface{ bindStore(*Store) }); ok {
		binder.bindStore(store)
	}
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

func assertReplyText(t *testing.T, reply, want string) {
	t.Helper()
	clean, references := stripConversationFooters(reply)
	if clean != want {
		t.Fatalf("reply body = %q, want %q", clean, want)
	}
	if len(references) != 1 || canonicalConversationReference(references[0]) == "" {
		t.Fatalf("reply references = %v, want one valid short conversation reference", references)
	}
}

func lookupEnv(environment []string, key string) string {
	prefix := key + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}
