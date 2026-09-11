package client

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseForwardControlAcceptsOnlyExactCaseInsensitiveChoices(t *testing.T) {
	tests := map[string]forwardControlKind{
		"yes":     forwardControlYes,
		"YES!":    forwardControlYes,
		" Yes. ":  forwardControlYes,
		"no,":     forwardControlNo,
		"Cancel!": forwardControlCancel,
		"2.":      forwardControlSelection,
	}
	for input, want := range tests {
		got := parseForwardControl(input)
		if got.Kind != want {
			t.Errorf("parseForwardControl(%q) = %+v, want kind %v", input, got, want)
		}
	}
	for _, input := range []string{"", "yes please", "yes!!", "no thanks", "cancel now", "0", "1 2"} {
		if got := parseForwardControl(input); got.Kind != forwardControlInvalid {
			t.Errorf("parseForwardControl(%q) = %+v, want invalid", input, got)
		}
	}
}

func TestInlineForwardReferencesAcrossClientFixtures(t *testing.T) {
	entries, err := filepath.Glob(filepath.Join("testdata", "forwards", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 7 {
		t.Fatalf("forward fixture count = %d, want at least 7", len(entries))
	}
	for _, path := range entries {
		t.Run(filepath.Base(path), func(t *testing.T) {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := inlineForwardReferences(string(body)); !slices.Equal(got, []string{testCanonicalConversationReference}) {
				t.Fatalf("references = %v", got)
			}
		})
	}
}

func TestInlineForwardReferencesRejectsAuthoredSessionMention(t *testing.T) {
	for _, body := range []string{
		"Please inspect session dm1-kyf1e-4cze7x in the database.",
		"Here is the exact text:\n\nsession dm1-kyf1e-4cze7x",
		"Dear Machines:\nsession dm1-kyf1e-4cze7x",
	} {
		if got := inlineForwardReferences(body); len(got) != 0 {
			t.Fatalf("authored body %q produced references %v", body, got)
		}
	}
}

func TestForwardedEMLFindsNestedHTMLFooter(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "forwards", "gmail-attached.eml"))
	if err != nil {
		t.Fatal(err)
	}
	bodies, err := embeddedMessageBodies(raw, 0)
	if err != nil {
		t.Fatalf("embeddedMessageBodies: %v", err)
	}
	var references []string
	for _, body := range bodies {
		_, found := stripConversationFooters(body)
		references = mergeConversationReferences(references, found)
	}
	if !slices.Equal(references, []string{testCanonicalConversationReference}) {
		t.Fatalf("references = %v", references)
	}
}

func TestForwardConfirmationYesForksWithoutSendingControlMailToAgent(t *testing.T) {
	rig, transport, source := forwardTestRig(t)
	forward := Message{
		MessageID: "forward-request",
		ThreadID:  "forward-thread",
		From:      "user@example.com",
		Body: "Please adapt this for the new customer.\n\n---------- Forwarded message ---------\n" +
			"From: Dear Machine <machine@example.com>\nDate: Today\nTo: User <user@example.com>\nSubject: Plan\n\n" +
			"Earlier answer.\n\n" + conversationFooter(source.SessionID),
	}
	transport.setPoll([]Message{forward})
	mustProcess(t, rig)
	if got := len(transport.replies); got != 2 {
		t.Fatalf("reply count after detection = %d, want source answer and confirmation", got)
	}
	if !strings.Contains(transport.replies[1].Text, "Reply with only") {
		t.Fatalf("confirmation = %q", transport.replies[1].Text)
	}
	if got := rig.capture("count"); got != "1" {
		t.Fatalf("agent runs after detection = %q, want 1", got)
	}

	rig.setAnswer("Forked answer.")
	control := Message{MessageID: "forward-yes", ThreadID: forward.ThreadID, From: forward.From, Body: " yEs! "}
	transport.setPoll([]Message{control})
	mustProcess(t, rig)

	child := rig.session(forward.ThreadID)
	if child.SessionID == source.SessionID {
		t.Fatal("fork reused source session")
	}
	if got := strings.TrimSpace(rig.capture("forked-sessions")); got != source.SessionID {
		t.Fatalf("fork source = %q, want %q", got, source.SessionID)
	}
	if got := strings.TrimSpace(rig.capture("forked-destinations")); got != child.SessionID {
		t.Fatalf("fork destination = %q, want %q", got, child.SessionID)
	}
	prompt := rig.capture("text-2")
	if !strings.Contains(prompt, "Please adapt this for the new customer.") {
		t.Fatalf("fork prompt omitted authored request:\n%s", prompt)
	}
	for _, excluded := range []string{"yEs", "Reply with only", "Earlier answer", "Forwarded message", "session dm1-"} {
		if strings.Contains(prompt, excluded) {
			t.Fatalf("fork prompt contains control or forwarded text %q:\n%s", excluded, prompt)
		}
	}
	if !slices.Contains(rig.captureLines("args-2"), "--resume") {
		t.Fatalf("fork run did not resume child: %v", rig.captureLines("args-2"))
	}
}

func TestForwardConfirmationNoPreservesCompleteForward(t *testing.T) {
	rig, transport, source := forwardTestRig(t)
	body := "Please assess this.\n\nBegin forwarded message:\nFrom: Dear Machine <machine@example.com>\n" +
		"Date: Today\nTo: User <user@example.com>\nSubject: Details\n\nOriginal details.\n\n" +
		minimalConversationFooter(source.SessionID)
	forward := Message{MessageID: "forward-no-request", ThreadID: "forward-no-thread", From: "user@example.com", Body: body}
	transport.setPoll([]Message{forward})
	mustProcess(t, rig)

	rig.setAnswer("Ordinary answer.")
	transport.setPoll([]Message{{MessageID: "forward-no", ThreadID: forward.ThreadID, From: forward.From, Body: "NO,"}})
	mustProcess(t, rig)

	prompt := rig.capture("text-2")
	for _, included := range []string{"Begin forwarded message", "Original details.", minimalConversationFooter(source.SessionID)} {
		if !strings.Contains(prompt, included) {
			t.Fatalf("ordinary prompt omitted %q:\n%s", included, prompt)
		}
	}
	if _, err := os.Stat(filepath.Join(rig.captureDir, "forked-sessions")); !os.IsNotExist(err) {
		t.Fatalf("No decision unexpectedly forked a session: %v", err)
	}
}

func TestForwardConfirmationInvalidRepeatsAndCancelRunsNoAgentTurn(t *testing.T) {
	rig, transport, source := forwardTestRig(t)
	forward := Message{
		MessageID: "forward-cancel-request", ThreadID: "forward-cancel-thread", From: "user@example.com",
		Body: "Please use this.\n\n> Earlier answer.\n> " + strings.ReplaceAll(conversationFooter(source.SessionID), "\n", "\n> "),
	}
	transport.setPoll([]Message{forward})
	mustProcess(t, rig)
	transport.setPoll([]Message{{MessageID: "forward-invalid", ThreadID: forward.ThreadID, From: forward.From, Body: "yes please"}})
	mustProcess(t, rig)
	if got := transport.replies[len(transport.replies)-1].Text; !strings.Contains(got, "exactly Yes!") {
		t.Fatalf("invalid reply prompt = %q", got)
	}
	transport.setPoll([]Message{{MessageID: "forward-cancel", ThreadID: forward.ThreadID, From: forward.From, Body: "Cancel."}})
	mustProcess(t, rig)
	if got := rig.capture("count"); got != "1" {
		t.Fatalf("agent runs = %q, want only source turn", got)
	}
	if _, found, err := rig.store.ForwardRequestForThread(forward.ThreadID); err != nil || found {
		t.Fatalf("forward request after cancel: found=%v err=%v", found, err)
	}
}

func TestForwardFromAnotherUserPreservesCompleteMessageWithoutConfirmation(t *testing.T) {
	transport := newFakeTransport()
	rig := newTestRigTransport(t, TierPlain, transport, "test-model")
	foreign := newConversationReference()
	body := "Please summarize this.\n\n----- Forwarded Message -----\n" +
		"From: Someone Else <other@example.com>\nDate: Today\nTo: User <user@example.com>\nSubject: Their session\n\n" +
		"Their complete message.\n\n" + conversationFooter(foreign)
	transport.setPoll([]Message{{MessageID: "foreign-forward", ThreadID: "foreign-thread", From: "user@example.com", Body: body}})
	rig.setAnswer("Summary.")
	mustProcess(t, rig)

	if got := len(transport.replies); got != 1 {
		t.Fatalf("reply count = %d, want ordinary agent reply only", got)
	}
	prompt := rig.capture("text-1")
	for _, included := range []string{"Forwarded Message", "Their complete message.", conversationFooter(foreign)} {
		if !strings.Contains(prompt, included) {
			t.Fatalf("foreign forward omitted %q:\n%s", included, prompt)
		}
	}
}

func TestForwardMultipleSessionsRequiresSelectionBeforeConfirmation(t *testing.T) {
	transport := newFakeTransport()
	rig := newTestRigTransport(t, TierPlain, transport, "test-model")
	firstMessage := Message{MessageID: "source-a", ThreadID: "source-thread-a", From: "user@example.com", Body: "First source."}
	secondMessage := Message{MessageID: "source-b", ThreadID: "source-thread-b", From: "user@example.com", Body: "Second source."}
	rig.setAnswer("Source answer.")
	transport.setPoll([]Message{firstMessage})
	mustProcess(t, rig)
	transport.setPoll([]Message{secondMessage})
	mustProcess(t, rig)
	first := rig.session(firstMessage.ThreadID)
	second := rig.session(secondMessage.ThreadID)

	forward := Message{
		MessageID: "multiple-forward", ThreadID: "multiple-thread", From: "user@example.com",
		Body: "Compare these.\n\n> First.\n> " + strings.ReplaceAll(conversationFooter(first.SessionID), "\n", "\n> ") +
			"\n\n>> Second.\n>> " + strings.ReplaceAll(conversationFooter(second.SessionID), "\n", "\n>> "),
	}
	transport.setPoll([]Message{forward})
	mustProcess(t, rig)
	selectionPrompt := transport.replies[len(transport.replies)-1].Text
	if !strings.Contains(selectionPrompt, "1. "+first.SessionID) || !strings.Contains(selectionPrompt, "2. "+second.SessionID) {
		t.Fatalf("selection prompt = %q", selectionPrompt)
	}

	selection := Message{MessageID: "multiple-select", ThreadID: forward.ThreadID, From: forward.From, Body: "2."}
	transport.setPoll([]Message{selection})
	mustProcess(t, rig)
	confirmation := transport.replies[len(transport.replies)-1].Text
	if !strings.Contains(confirmation, second.SessionID) || !strings.Contains(confirmation, "Reply with only") {
		t.Fatalf("selected confirmation = %q", confirmation)
	}
	if got := rig.capture("count"); got != "2" {
		t.Fatalf("selection invoked agent: count=%q", got)
	}

	transport.setPoll([]Message{{MessageID: "multiple-yes", ThreadID: forward.ThreadID, From: forward.From, Body: "YES"}})
	mustProcess(t, rig)
	if got := strings.TrimSpace(rig.capture("forked-sessions")); got != second.SessionID {
		t.Fatalf("fork source = %q, want selected %q", got, second.SessionID)
	}
}

func TestAttachedEMLTriggersConfirmation(t *testing.T) {
	transport := newFakeTransport()
	rig := newTestRigTransport(t, TierPlain, transport, "test-model")
	source := Message{MessageID: "eml-source", ThreadID: "eml-source-thread", From: "user@example.com", Body: "Source."}
	transport.setPoll([]Message{source})
	mustProcess(t, rig)
	session := rig.session(source.ThreadID)
	raw := strings.ReplaceAll(string(mustReadTestFile(t, filepath.Join("testdata", "forwards", "gmail-attached.eml"))), testCanonicalConversationReference, session.SessionID)
	forward := Message{
		MessageID: "eml-forward", ThreadID: "eml-forward-thread", From: "user@example.com", Body: "Please use the attached email.",
		Attachments: []AttachmentRef{{AttachmentID: "attached-eml", Filename: "message.eml", ContentType: "message/rfc822", SizeBytes: int64(len(raw))}},
	}
	transport.attachments["attached-eml"] = []byte(raw)
	transport.setPoll([]Message{forward})
	mustProcess(t, rig)
	if got := transport.replies[len(transport.replies)-1].Text; !strings.Contains(got, session.SessionID) || !strings.Contains(got, "Reply with only") {
		t.Fatalf("EML confirmation = %q", got)
	}
}

func TestForwardConfirmationSurvivesStoreRestart(t *testing.T) {
	rig, transport, source := forwardTestRig(t)
	forward := Message{
		MessageID: "restart-forward", ThreadID: "restart-forward-thread", From: "user@example.com",
		Body: "Continue after restart.\n\n> Earlier answer.\n> " + strings.ReplaceAll(conversationFooter(source.SessionID), "\n", "\n> "),
	}
	transport.setPoll([]Message{forward})
	mustProcess(t, rig)
	rig.restartStore(t)

	transport.setPoll([]Message{{MessageID: "restart-yes", ThreadID: forward.ThreadID, From: forward.From, Body: "yes."}})
	mustProcess(t, rig)
	child := rig.session(forward.ThreadID)
	if child.SessionID == source.SessionID || child.Sequence != 1 {
		t.Fatalf("restarted fork session = %+v, source = %+v", child, source)
	}
}

func TestConfirmedForkTakesSourceLaneBeforeQueuedSourceFollowUp(t *testing.T) {
	queue := newThreadWorkQueue()
	source := messageWork{pending: PendingMessage{MessageID: "source-active", ThreadID: "source-thread", State: messageReceived, Session: Session{Sequence: 4}}}
	followUp := messageWork{pending: PendingMessage{MessageID: "source-next", ThreadID: "source-thread", State: messageReceived, Session: Session{Sequence: 5}}}
	fork := messageWork{pending: PendingMessage{MessageID: "confirmed-fork", ThreadID: "child-thread", ForkedFromThreadID: "source-thread"}}
	queue.enqueue(source)
	active, ok := queue.take()
	if !ok || active.pending.MessageID != source.pending.MessageID {
		t.Fatalf("active work = %+v", active)
	}
	queue.enqueue(followUp)
	queue.enqueuePriority(fork)
	if _, ok := queue.take(); ok {
		t.Fatal("source lane admitted concurrent work")
	}
	queue.finish(source.lane())
	next, ok := queue.take()
	if !ok || next.pending.MessageID != fork.pending.MessageID {
		t.Fatalf("next work = %+v, want confirmed fork", next)
	}
}

func TestRecoveredRunningSourceFinishesBeforeConfirmedFork(t *testing.T) {
	queue := newThreadWorkQueue()
	fork := messageWork{pending: PendingMessage{MessageID: "confirmed-fork", ThreadID: "child-thread", State: messageReceived, ForkedFromThreadID: "source-thread"}}
	nextSource := messageWork{pending: PendingMessage{MessageID: "source-next", ThreadID: "source-thread", State: messageReceived}}
	runningSource := messageWork{pending: PendingMessage{MessageID: "source-running", ThreadID: "source-thread", State: messageRunning}}
	queue.enqueue(fork)
	queue.enqueue(nextSource)
	queue.enqueue(runningSource)

	first, ok := queue.take()
	if !ok || first.pending.MessageID != runningSource.pending.MessageID {
		t.Fatalf("first recovered work = %+v, want running source", first)
	}
	queue.finish(first.lane())
	second, ok := queue.take()
	if !ok || second.pending.MessageID != fork.pending.MessageID {
		t.Fatalf("second recovered work = %+v, want confirmed fork", second)
	}
}

func mustReadTestFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func forwardTestRig(t *testing.T) (*testRig, *fakeTransport, Session) {
	t.Helper()
	transport := newFakeTransport()
	rig := newTestRigTransport(t, TierPlain, transport, "test-model")
	sourceMessage := Message{MessageID: "source-request", ThreadID: "source-thread", From: "user@example.com", Body: "Create the original plan."}
	transport.setPoll([]Message{sourceMessage})
	rig.setAnswer("Original answer.")
	mustProcess(t, rig)
	return rig, transport, rig.session(sourceMessage.ThreadID)
}
