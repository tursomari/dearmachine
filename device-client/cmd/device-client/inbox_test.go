package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dearmachine/dearmachine/internal/deviceclient"
)

func TestInboxHelpAtEveryCommandLevel(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"--help"}, want: "device-client inbox <command>"},
		{args: []string{"help", "skip"}, want: "device-client inbox skip --current"},
		{args: []string{"skip", "--help"}, want: "without changing AgentMail"},
		{args: []string{"abandon", "--help"}, want: "clean pre-run session checkpoint"},
		{args: []string{"unskip", "--help"}, want: "device-client inbox unskip"},
		{args: []string{"skipped", "--help"}, want: "device-client inbox skipped"},
	} {
		t.Run(strings.Join(test.args, "_"), func(t *testing.T) {
			var output strings.Builder
			err := runInbox(
				test.args,
				func(string) string { return "" },
				dependencies{flagOutput: &output},
			)
			if err != nil && !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("runInbox(%v): %v", test.args, err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("help output missing %q:\n%s", test.want, output.String())
			}
		})
	}
}

func TestInboxAbandonRestoresCheckpointAndRemapsRunningFollowup(t *testing.T) {
	home := t.TempDir()
	dbPath := filepath.Join(home, "state", "device-client.db")
	store, err := deviceclient.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := store.BeginMessage("message-1", "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(first.MessageID, "first prompt"); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreResult(
		first.MessageID,
		deviceclient.RunResult{Kind: deviceclient.ResultAnswer, Text: "done"},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(first.MessageID, "completed", "reply-1"); err != nil {
		t.Fatal(err)
	}
	second, _, err := store.BeginMessage("message-2", "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunningWithCheckpoint(
		second.MessageID,
		"partial follow-up",
		"replacement-session",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	fixturePath, err := filepath.Abs(filepath.Join(
		"..", "..", "internal", "deviceclient", "testdata", "fake-mct-agent.sh",
	))
	if err != nil {
		t.Fatal(err)
	}
	captureDir := t.TempDir()
	t.Setenv("FAKE_MCT_CAPTURE", captureDir)
	t.Setenv("FAKE_MCT_STATUS", filepath.Join(t.TempDir(), "unused-status"))
	t.Setenv("FAKE_MCT_ANSWER", filepath.Join(t.TempDir(), "unused-answer"))
	t.Setenv("FAKE_MCT_FORK_ID", "replacement-session")
	runner, err := deviceclient.NewMCTRunner(fixturePath, home, "")
	if err != nil {
		t.Fatal(err)
	}
	var stdout strings.Builder
	deps := dependencies{
		openStore: deviceclient.OpenStore,
		newRunner: func(string, string, string) (*deviceclient.MCTRunner, error) {
			return runner, nil
		},
		stdout:      &stdout,
		flagOutput:  io.Discard,
		userHomeDir: func() (string, error) { return home, nil },
	}
	if err := runInboxAbandon(
		[]string{
			"--db", dbPath,
			"--pidfile", filepath.Join(home, "missing.pid"),
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
	if err != nil || string(deleted) != first.Session.SessionID+"\n" {
		t.Fatalf("deleted sessions = %q, %v", deleted, err)
	}
	store, err = deviceclient.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if skipped, err := store.IsSkipped(second.MessageID); err != nil || !skipped {
		t.Fatalf("IsSkipped = %v, %v", skipped, err)
	}
	session, err := store.Session(second.ThreadID)
	if err != nil || session.SessionID != "replacement-session" || session.Sequence != 1 {
		t.Fatalf("Session = %+v, %v", session, err)
	}
}

func TestInboxAbandonRejectsLegacyRunningFollowupWithoutCheckpoint(t *testing.T) {
	home := t.TempDir()
	dbPath := filepath.Join(home, "state", "device-client.db")
	store, err := deviceclient.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := store.BeginMessage("message-1", "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(first.MessageID, "first prompt"); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreResult(
		first.MessageID,
		deviceclient.RunResult{Kind: deviceclient.ResultAnswer, Text: "done"},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(first.MessageID, "completed", "reply-1"); err != nil {
		t.Fatal(err)
	}
	second, _, err := store.BeginMessage("message-2", "thread-1")
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
		openStore:   deviceclient.OpenStore,
		stdout:      io.Discard,
		flagOutput:  io.Discard,
		userHomeDir: func() (string, error) { return home, nil },
	}
	err = runInboxAbandon(
		[]string{"--db", dbPath, "--project", home, second.MessageID},
		deps,
	)
	if err == nil || !strings.Contains(err.Error(), "no clean pre-run session checkpoint") {
		t.Fatalf("runInboxAbandon error = %v", err)
	}
	store, err = deviceclient.OpenStore(dbPath)
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
	dbPath := filepath.Join(home, "state", "device-client.db")
	store, err := deviceclient.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SkipMessages(
		[]deviceclient.MessageRef{{MessageID: "message-1", ThreadID: "thread-1"}},
		"stale",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout strings.Builder
	deps := dependencies{
		openStore:   deviceclient.OpenStore,
		stdout:      &stdout,
		flagOutput:  io.Discard,
		userHomeDir: func() (string, error) { return home, nil },
	}
	if err := runInboxSkipped([]string{"--db", dbPath, "--json"}, deps); err != nil {
		t.Fatalf("runInboxSkipped: %v", err)
	}
	for _, want := range []string{`"message_id": "message-1"`, `"reason": "stale"`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("JSON output missing %q:\n%s", want, stdout.String())
		}
	}

	stdout.Reset()
	if err := runInboxUnskip([]string{"--db", dbPath, "message-1"}, deps); err != nil {
		t.Fatalf("runInboxUnskip: %v", err)
	}
	if got := stdout.String(); got != "Unskipped 1 message(s).\n" {
		t.Fatalf("unskip output = %q", got)
	}
	stdout.Reset()
	if err := runInboxSkipped([]string{"--db", dbPath, "--json"}, deps); err != nil {
		t.Fatalf("runInboxSkipped after unskip: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "[]" {
		t.Fatalf("empty JSON skip list = %q, want []", got)
	}
	store, err = deviceclient.OpenStore(dbPath)
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
	dbPath := filepath.Join(home, "device-client.db")
	pidfile := filepath.Join(home, "device-client.pid")
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := deviceclient.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SkipMessages(
		[]deviceclient.MessageRef{{MessageID: "message-1", ThreadID: "thread-1"}},
		"stale",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	deps := dependencies{
		openStore:   deviceclient.OpenStore,
		stdout:      io.Discard,
		flagOutput:  io.Discard,
		userHomeDir: func() (string, error) { return home, nil },
	}
	err = runInboxUnskip(
		[]string{"--db", dbPath, "--pidfile", pidfile, "message-1"},
		deps,
	)
	if err == nil || !strings.Contains(err.Error(), "is still running") {
		t.Fatalf("runInboxUnskip error = %v", err)
	}
	store, err = deviceclient.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if skipped, err := store.IsSkipped("message-1"); err != nil || !skipped {
		t.Fatalf("IsSkipped after refusal = %v, %v", skipped, err)
	}
}
