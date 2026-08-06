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
