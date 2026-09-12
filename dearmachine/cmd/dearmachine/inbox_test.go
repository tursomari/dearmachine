package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/dearmachine/dearmachine/internal/client"
)

const testCheckpointSessionID = "agent-20260821T141425-8795"

func TestInboxHelpAtEveryCommandLevel(t *testing.T) {
	for _, test := range []struct {
		args  []string
		wants []string
	}{
		{args: []string{"--help"}, wants: []string{"dearmachine inbox <command>"}},
		{args: []string{"help", "skip"}, wants: []string{"dearmachine inbox skip --pair", "--agent-bin <path>"}},
		{args: []string{"skip", "--help"}, wants: []string{"without changing the remote inbox", "--pair <selector>", "--agent-bin <path>"}},
		{args: []string{"abandon", "--help"}, wants: []string{"clean pre-run session checkpoint", "--agent-bin <path>"}},
		{args: []string{"unskip", "--help"}, wants: []string{"dearmachine inbox unskip"}},
		{args: []string{"skipped", "--help"}, wants: []string{"dearmachine inbox skipped"}},
	} {
		t.Run(strings.Join(test.args, "_"), func(t *testing.T) {
			var output strings.Builder
			err := runInbox(test.args, dependencies{flagOutput: &output})
			if err != nil && !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("runInbox(%v): %v", test.args, err)
			}
			for _, want := range test.wants {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("help output missing %q:\n%s", want, output.String())
				}
			}
		})
	}
}

func TestInboxSkipUsesRegisteredPairTransport(t *testing.T) {
	home := t.TempDir()
	state := makeInboxTestPair(t, home, "openmail")
	want := errors.New("selected OpenMail constructor reached")
	deps := dependencies{
		newRawTransport: func(transport, providerID string) (client.Transport, error) {
			if transport != "openmail" || providerID != state.Inbox.ProviderID {
				t.Fatalf("raw transport = %q/%q", transport, providerID)
			}
			return nil, want
		},
		flagOutput:  io.Discard,
		userHomeDir: func() (string, error) { return home, nil },
	}
	err := runInboxSkip(
		[]string{
			"--current",
			"--pair", state.Pair.UserEmail,
		},
		deps,
	)
	if !errors.Is(err, want) {
		t.Fatalf("runInboxSkip error = %v, want %v", err, want)
	}
}

func TestInboxSkipDeletesCanonicalOnDiskSession(t *testing.T) {
	home := t.TempDir()
	state := makeInboxTestPair(t, home, "agentmail")
	store, err := client.OpenPairStore(state.Path, state.Pair)
	if err != nil {
		t.Fatal(err)
	}
	pending, _, err := store.BeginMessage("message-1", "thread-1", client.TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	requireCanonicalSessionID(t, pending.Session.SessionID)
	if err := store.MarkRunning(pending.MessageID, "partial prompt"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	fixturePath, err := filepath.Abs(filepath.Join(
		"..", "..", "internal", "client", "testdata", "fake-agent.sh",
	))
	if err != nil {
		t.Fatal(err)
	}
	captureDir := t.TempDir()
	t.Setenv("FAKE_AGENT_CAPTURE", captureDir)
	t.Setenv("FAKE_AGENT_STATUS", filepath.Join(t.TempDir(), "unused-status"))
	t.Setenv("FAKE_AGENT_ANSWER", filepath.Join(t.TempDir(), "unused-answer"))
	transport := inboxTestTransport{message: client.Message{
		MessageID: pending.MessageID,
		ThreadID:  pending.ThreadID,
		From:      state.Pair.UserEmail,
		To:        []string{state.Inbox.Address},
	}}
	deps := dependencies{
		newRawTransport: func(string, string) (client.Transport, error) { return transport, nil },
		openPairStore:   client.OpenPairStore,
		stdout:          io.Discard,
		flagOutput:      io.Discard,
		userHomeDir:     func() (string, error) { return home, nil },
	}
	if err := runInboxSkip(
		[]string{
			"--pair", state.Pair.UserEmail,
			"--project", home,
			"--agent-bin", fixturePath,
			pending.MessageID,
		},
		deps,
	); err != nil {
		t.Fatalf("runInboxSkip: %v", err)
	}
	deleted, err := os.ReadFile(filepath.Join(captureDir, "deleted-sessions"))
	if err != nil || string(deleted) != pending.Session.SessionID+"\n" {
		t.Fatalf("deleted sessions = %q, %v; want canonical %q", deleted, err, pending.Session.SessionID)
	}
}

func TestInboxAbandonRestoresCheckpointOntoStableCanonicalSession(t *testing.T) {
	home := t.TempDir()
	state := makeInboxTestPair(t, home, "agentmail")
	store, err := client.OpenPairStore(state.Path, state.Pair)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := store.BeginMessage("message-1", "thread-1", client.TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	requireCanonicalSessionID(t, first.Session.SessionID)
	if err := store.MarkRunning(first.MessageID, "first prompt"); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreResult(
		first.MessageID,
		client.RunResult{Kind: client.ResultAnswer, Text: "done"},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(first.MessageID, "completed", "reply-1"); err != nil {
		t.Fatal(err)
	}
	second, _, err := store.BeginMessage("message-2", "thread-1", client.TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunningWithCheckpoint(
		second.MessageID,
		"partial follow-up",
		testCheckpointSessionID,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	fixturePath, err := filepath.Abs(filepath.Join(
		"..", "..", "internal", "client", "testdata", "fake-agent.sh",
	))
	if err != nil {
		t.Fatal(err)
	}
	captureDir := t.TempDir()
	t.Setenv("FAKE_AGENT_CAPTURE", captureDir)
	t.Setenv("FAKE_AGENT_STATUS", filepath.Join(t.TempDir(), "unused-status"))
	t.Setenv("FAKE_AGENT_ANSWER", filepath.Join(t.TempDir(), "unused-answer"))
	runner, err := client.NewAgentRunner(fixturePath, home, "")
	if err != nil {
		t.Fatal(err)
	}
	var stdout strings.Builder
	deps := dependencies{
		openPairStore: client.OpenPairStore,
		newRunner: func(string, string, string) (*client.AgentRunner, error) {
			return runner, nil
		},
		stdout:      &stdout,
		flagOutput:  io.Discard,
		userHomeDir: func() (string, error) { return home, nil },
	}
	if err := runInboxAbandon(
		[]string{
			"--pair", state.Pair.UserEmail,
			"--project", home,
			"--reason", "stuck disposable test",
			second.MessageID,
		},
		deps,
	); err != nil {
		t.Fatalf("runInboxAbandon: %v", err)
	}
	if !strings.Contains(stdout.String(), "Abandoned message message-2 locally") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	deleted, err := os.ReadFile(filepath.Join(captureDir, "deleted-sessions"))
	if err != nil || string(deleted) != first.Session.SessionID+"\n"+testCheckpointSessionID+"\n" {
		t.Fatalf("deleted sessions = %q, %v", deleted, err)
	}
	forked, err := os.ReadFile(filepath.Join(captureDir, "forked-sessions"))
	if err != nil || string(forked) != testCheckpointSessionID+"\n" {
		t.Fatalf("forked sessions = %q, %v", forked, err)
	}
	destinations, err := os.ReadFile(filepath.Join(captureDir, "forked-destinations"))
	if err != nil || string(destinations) != first.Session.SessionID+"\n" {
		t.Fatalf("fork destinations = %q, %v", destinations, err)
	}
	store, err = client.OpenPairStore(state.Path, state.Pair)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if skipped, err := store.IsSkipped(second.MessageID); err != nil || !skipped {
		t.Fatalf("IsSkipped = %v, %v", skipped, err)
	}
	session, err := store.Session(second.ThreadID)
	if err != nil || session.SessionID != first.Session.SessionID || session.Sequence != 1 {
		t.Fatalf("Session = %+v, %v", session, err)
	}
}

func TestInboxAbandonRemovesAttachmentStaging(t *testing.T) {
	home := t.TempDir()
	state := makeInboxTestPair(t, home, "agentmail")
	store, err := client.OpenPairStore(state.Path, state.Pair)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := store.BeginMessage("message-1", "thread-1", client.TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	requireCanonicalSessionID(t, first.Session.SessionID)
	if err := store.MarkRunning(first.MessageID, "first prompt"); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreResult(
		first.MessageID,
		client.RunResult{Kind: client.ResultAnswer, Text: "done"},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(first.MessageID, "completed", "reply-1"); err != nil {
		t.Fatal(err)
	}
	second, _, err := store.BeginMessage("message-2", "thread-1", client.TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunningWithCheckpoint(
		second.MessageID,
		"partial follow-up",
		testCheckpointSessionID,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	turnKey := client.TurnKey(second.Session.Sequence, second.MessageID)
	abandonInbox := filepath.Join(home, ".attachments-inbox", turnKey)
	abandonOutbox := filepath.Join(home, ".attachments-outbox", turnKey)
	if err := os.MkdirAll(abandonInbox, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(abandonInbox, "request.txt"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(abandonOutbox, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(abandonOutbox, "out.txt"), []byte("frozen"), 0o600); err != nil {
		t.Fatal(err)
	}
	bogusTurnKey := client.TurnKey(second.Session.Sequence, "unrelated-message")
	bogusDir := filepath.Join(home, ".attachments-inbox", bogusTurnKey)
	if err := os.MkdirAll(bogusDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bogusDir, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	fixturePath, err := filepath.Abs(filepath.Join(
		"..", "..", "internal", "client", "testdata", "fake-agent.sh",
	))
	if err != nil {
		t.Fatal(err)
	}
	captureDir := t.TempDir()
	t.Setenv("FAKE_AGENT_CAPTURE", captureDir)
	t.Setenv("FAKE_AGENT_STATUS", filepath.Join(t.TempDir(), "unused-status"))
	t.Setenv("FAKE_AGENT_ANSWER", filepath.Join(t.TempDir(), "unused-answer"))
	runner, err := client.NewAgentRunner(fixturePath, home, "")
	if err != nil {
		t.Fatal(err)
	}
	var stdout strings.Builder
	deps := dependencies{
		openPairStore: client.OpenPairStore,
		newRunner: func(string, string, string) (*client.AgentRunner, error) {
			return runner, nil
		},
		stdout:      &stdout,
		flagOutput:  io.Discard,
		userHomeDir: func() (string, error) { return home, nil },
	}
	if err := runInboxAbandon(
		[]string{
			"--pair", state.Pair.UserEmail,
			"--project", home,
			"--reason", "stuck disposable test",
			second.MessageID,
		},
		deps,
	); err != nil {
		t.Fatalf("runInboxAbandon: %v", err)
	}
	if !strings.Contains(stdout.String(), "Abandoned message message-2 locally") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	deleted, err := os.ReadFile(filepath.Join(captureDir, "deleted-sessions"))
	if err != nil || string(deleted) != first.Session.SessionID+"\n"+testCheckpointSessionID+"\n" {
		t.Fatalf("deleted sessions = %q, %v", deleted, err)
	}
	store, err = client.OpenPairStore(state.Path, state.Pair)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if skipped, err := store.IsSkipped(second.MessageID); err != nil || !skipped {
		t.Fatalf("IsSkipped = %v, %v", skipped, err)
	}
	session, err := store.Session(second.ThreadID)
	if err != nil || session.SessionID != first.Session.SessionID || session.Sequence != 1 {
		t.Fatalf("Session = %+v, %v", session, err)
	}
	if _, err := os.Stat(abandonInbox); !os.IsNotExist(err) {
		t.Fatalf("abandoned turn inbox staging dir remains: %v", err)
	}
	if _, err := os.Stat(abandonOutbox); !os.IsNotExist(err) {
		t.Fatalf("abandoned turn outbox staging dir remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bogusDir, "keep.txt")); err != nil {
		t.Fatalf("unrelated staging dir was removed: %v", err)
	}
}

func TestInboxAbandonRejectsLegacyRunningFollowupWithoutCheckpoint(t *testing.T) {
	home := t.TempDir()
	state := makeInboxTestPair(t, home, "agentmail")
	store, err := client.OpenPairStore(state.Path, state.Pair)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := store.BeginMessage("message-1", "thread-1", client.TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(first.MessageID, "first prompt"); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreResult(
		first.MessageID,
		client.RunResult{Kind: client.ResultAnswer, Text: "done"},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(first.MessageID, "completed", "reply-1"); err != nil {
		t.Fatal(err)
	}
	second, _, err := store.BeginMessage("message-2", "thread-1", client.TierPlain)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(second.MessageID, "partial follow-up"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	deps := dependencies{
		openPairStore: client.OpenPairStore,
		stdout:        io.Discard,
		flagOutput:    io.Discard,
		userHomeDir:   func() (string, error) { return home, nil },
	}
	err = runInboxAbandon(
		[]string{"--pair", state.Pair.UserEmail, "--project", home, second.MessageID},
		deps,
	)
	if err == nil || !strings.Contains(err.Error(), "no clean pre-run session checkpoint") {
		t.Fatalf("runInboxAbandon error = %v", err)
	}
	store, err = client.OpenPairStore(state.Path, state.Pair)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pending, err := store.Pending()
	if err != nil || len(pending) != 1 || pending[0].MessageID != second.MessageID {
		t.Fatalf("Pending after legacy rejection = %+v, %v", pending, err)
	}
	if skipped, err := store.IsSkipped(second.MessageID); err != nil || skipped {
		t.Fatalf("IsSkipped after legacy rejection = %v, %v", skipped, err)
	}
	session, err := store.Session(second.ThreadID)
	if err != nil || session.SessionID != first.Session.SessionID {
		t.Fatalf("Session after legacy rejection = %+v, %v", session, err)
	}
}

func TestInboxSkippedAndUnskipUseOnlyLocalState(t *testing.T) {
	home := t.TempDir()
	state := makeInboxTestPair(t, home, "agentmail")
	store, err := client.OpenPairStore(state.Path, state.Pair)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SkipMessages(
		[]client.MessageRef{{MessageID: "message-1", ThreadID: "thread-1"}},
		"stale",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout strings.Builder
	deps := dependencies{
		openPairStore: client.OpenPairStore,
		stdout:        &stdout,
		flagOutput:    io.Discard,
		userHomeDir:   func() (string, error) { return home, nil },
	}
	if err := runInboxSkipped([]string{"--pair", state.Pair.UserEmail, "--json"}, deps); err != nil {
		t.Fatalf("runInboxSkipped: %v", err)
	}
	for _, want := range []string{`"message_id": "message-1"`, `"reason": "stale"`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("JSON output missing %q:\n%s", want, stdout.String())
		}
	}

	stdout.Reset()
	if err := runInboxUnskip([]string{"--pair", state.Pair.UserEmail, "message-1"}, deps); err != nil {
		t.Fatalf("runInboxUnskip: %v", err)
	}
	if got := stdout.String(); got != "Unskipped 1 message(s).\n" {
		t.Fatalf("unskip output = %q", got)
	}
	stdout.Reset()
	if err := runInboxSkipped([]string{"--pair", state.Pair.UserEmail, "--json"}, deps); err != nil {
		t.Fatalf("runInboxSkipped after unskip: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, `"messages": []`) {
		t.Fatalf("empty JSON skip list = %q", got)
	}
	store, err = client.OpenPairStore(state.Path, state.Pair)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if skipped, err := store.IsSkipped("message-1"); err != nil || skipped {
		t.Fatalf("IsSkipped = %v, %v", skipped, err)
	}
}

func TestInboxMutationRefusesLiveDeviceClientPID(t *testing.T) {
	home := t.TempDir()
	state := makeInboxTestPair(t, home, "agentmail")
	pidfile, err := client.DefaultDaemonLockPath(func() (string, error) { return home, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pidfile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := client.OpenPairStore(state.Path, state.Pair)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SkipMessages(
		[]client.MessageRef{{MessageID: "message-1", ThreadID: "thread-1"}},
		"stale",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	deps := dependencies{
		openPairStore: client.OpenPairStore,
		stdout:        io.Discard,
		flagOutput:    io.Discard,
		userHomeDir:   func() (string, error) { return home, nil },
	}
	err = runInboxUnskip(
		[]string{"--pair", state.Pair.UserEmail, "message-1"},
		deps,
	)
	if err == nil || !strings.Contains(err.Error(), "is still running") {
		t.Fatalf("runInboxUnskip error = %v", err)
	}
	store, err = client.OpenPairStore(state.Path, state.Pair)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if skipped, err := store.IsSkipped("message-1"); err != nil || !skipped {
		t.Fatalf("IsSkipped after refusal = %v, %v", skipped, err)
	}
}

func TestInboxSkippedUsesOnlyRegisteredPairState(t *testing.T) {
	home := t.TempDir()
	homeDir := func() (string, error) { return home, nil }
	inbox, err := client.RegisterInbox(homeDir, client.Inbox{Transport: "agentmail", ProviderID: "inbox", Address: "machine@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := client.CreatePair(homeDir, client.Pair{UserEmail: "user@example.test", InboxID: inbox.ID})
	if err != nil {
		t.Fatal(err)
	}
	path, err := client.DefaultPairDatabasePath(homeDir, pair.ID)
	if err != nil {
		t.Fatal(err)
	}
	store, err := client.OpenPairStore(path, pair)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SkipMessages([]client.MessageRef{{MessageID: "pair-message", ThreadID: "pair-thread"}}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var output strings.Builder
	deps := dependencies{
		openPairStore: client.OpenPairStore,
		stdout:        &output,
		flagOutput:    io.Discard,
		userHomeDir:   homeDir,
	}
	if err := runInboxSkipped(nil, deps); err != nil {
		t.Fatalf("runInboxSkipped: %v", err)
	}
	if !strings.Contains(output.String(), "pair-message") {
		t.Fatalf("skipped output = %q", output.String())
	}
}

type inboxTestTransport struct {
	message client.Message
}

func makeInboxTestPair(t *testing.T, home, transport string) client.PairState {
	t.Helper()
	homeDir := func() (string, error) { return home, nil }
	inbox, err := client.RegisterInbox(homeDir, client.Inbox{
		Transport: transport, ProviderID: "provider-inbox", Address: "machine@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := client.CreatePair(homeDir, client.Pair{UserEmail: "user@example.test", InboxID: inbox.ID})
	if err != nil {
		t.Fatal(err)
	}
	state, err := client.ResolvePairState(homeDir, pair.ID)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func (f inboxTestTransport) Poll(context.Context) ([]client.Message, error) {
	return []client.Message{f.message}, nil
}

func (f inboxTestTransport) Thread(context.Context, string) ([]client.Message, error) {
	return nil, nil
}

func (f inboxTestTransport) Message(context.Context, string) (client.Message, error) {
	return f.message, nil
}

func (f inboxTestTransport) Reply(
	context.Context,
	string,
	client.ReplyPayload,
	string,
) (string, error) {
	return "", nil
}

func (f inboxTestTransport) ReplyReceipt(
	context.Context,
	client.Message,
	string,
) (string, bool, error) {
	return "", false, nil
}

func (f inboxTestTransport) MarkProcessed(context.Context, string) error {
	return nil
}

func (f inboxTestTransport) FetchAttachment(context.Context, string, int64) ([]byte, error) {
	return nil, nil
}

func requireCanonicalSessionID(t *testing.T, sessionID string) {
	t.Helper()
	if !regexp.MustCompile(`^dm1-[0-9a-hjkmnp-tv-z]{5}-[0-9a-hjkmnp-tv-z]{6}$`).MatchString(sessionID) {
		t.Fatalf("SessionID = %q, want short canonical dm1 conversation reference", sessionID)
	}
}
