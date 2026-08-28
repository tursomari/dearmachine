package agentmanager

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type completionMeta struct {
	Status        string `json:"status"`
	FailureReason string `json:"failure_reason"`
	StderrTail    string `json:"stderr_tail"`
	ExitCode      *int   `json:"exit_code"`
	Signal        string `json:"signal"`
}

func readCompletionMeta(t *testing.T, manager *Manager, id string) completionMeta {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(manager.TicketDir(id), "meta.json"))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var meta completionMeta
	if err := json.Unmarshal(content, &meta); err != nil {
		t.Fatalf("parse metadata: %v", err)
	}
	return meta
}

func TestLegacyCloseArtifactIsNeverOverwritten(t *testing.T) {
	manager := testManager(t, `
request=$(cat)
close_path=$(printf '%s\n' "$request" | sed -n 's/^# Close-Path: //p')
printf 'legacy response\nwith final newline\n' > "$close_path"
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"native response"}}'
`)
	id, err := manager.Send("codex", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitForStatus(t, manager, id, StatusClosed)
	content, err := os.ReadFile(filepath.Join(manager.TicketDir(id), "ticket-close.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "legacy response\nwith final newline\n" {
		t.Fatalf("legacy close artifact was changed: %q", content)
	}
}

func TestPublishedReplyHasPrivateMode(t *testing.T) {
	manager := testManager(t, `cat >/dev/null; printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"native response"}}'`)
	id, err := manager.Send("codex", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitForStatus(t, manager, id, StatusClosed)
	info, err := os.Stat(filepath.Join(manager.TicketDir(id), "ticket-close.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("close artifact mode = %o, want 600", got)
	}
}

func TestNonzeroExitNeverClosesTicketWithArtifact(t *testing.T) {
	manager := testManager(t, `
request=$(cat)
close_path=$(printf '%s\n' "$request" | sed -n 's/^# Close-Path: //p')
printf '%s\n' 'legacy artifact' > "$close_path"
printf '%s\n' 'worker failed badly' >&2
exit 7
`)
	id, err := manager.Send("codex", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitForStatus(t, manager, id, StatusCrashed)
	meta := readCompletionMeta(t, manager, id)
	if meta.FailureReason != "worker_exit" || meta.ExitCode == nil || *meta.ExitCode != 7 || meta.Signal != "" {
		t.Fatalf("failure metadata = %+v", meta)
	}
	if !strings.Contains(meta.StderrTail, "worker failed badly") {
		t.Fatalf("stderr tail = %q", meta.StderrTail)
	}
	content, err := os.ReadFile(filepath.Join(manager.TicketDir(id), "ticket-close.md"))
	if err != nil || string(content) != "legacy artifact\n" {
		t.Fatalf("preserved artifact = %q, %v", content, err)
	}
}

func TestSignalExitNeverClosesTicket(t *testing.T) {
	manager := testManager(t, `cat >/dev/null; printf '%s\n' 'terminating' >&2; kill -TERM $$`)
	id, err := manager.Send("codex", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitForStatus(t, manager, id, StatusCrashed)
	meta := readCompletionMeta(t, manager, id)
	if meta.FailureReason != "worker_signal" || meta.ExitCode != nil || meta.Signal == "" {
		t.Fatalf("failure metadata = %+v", meta)
	}
	if !strings.Contains(meta.StderrTail, "terminating") {
		t.Fatalf("stderr tail = %q", meta.StderrTail)
	}
}

func TestWhitespaceOnlyReplyFailsWithoutCloseArtifact(t *testing.T) {
	manager := testManager(t, `cat >/dev/null; printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":" \n\t "}}'`)
	id, err := manager.Send("codex", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitForStatus(t, manager, id, StatusCrashed)
	meta := readCompletionMeta(t, manager, id)
	if meta.FailureReason != "empty_reply" || meta.ExitCode != nil || meta.Signal != "" {
		t.Fatalf("failure metadata = %+v", meta)
	}
	if _, err := os.Stat(filepath.Join(manager.TicketDir(id), "ticket-close.md")); !os.IsNotExist(err) {
		t.Fatalf("close artifact exists or stat failed unexpectedly: %v", err)
	}
}

func TestSupervisorDrainsStdoutAfterParserFailure(t *testing.T) {
	manager := testManager(t, `cat >/dev/null; head -c 2097152 /dev/zero`)
	manager.Adapters["codex"] = shellAdapter{
		script: `cat >/dev/null; head -c 2097152 /dev/zero`,
		consumeStdout: func(io.Reader, func(string)) (Observation, error) {
			return Observation{}, errors.New("parse failed")
		},
	}
	id, err := manager.Send("codex", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitForStatus(t, manager, id, StatusCrashed)
	if meta := readCompletionMeta(t, manager, id); meta.FailureReason != "output_failed" {
		t.Fatalf("failure metadata = %+v", meta)
	}
}

func TestConfigurableAdapterDrainsUnknownOutputFormat(t *testing.T) {
	adapter := NewConfigurableAdapter("custom", "sh", "unknown", nil, nil)
	reader := &trackingReader{Reader: strings.NewReader("worker output")}
	_, err := adapter.ConsumeStdout(reader, func(string) {})
	if err == nil || !reader.drained {
		t.Fatalf("ConsumeStdout error = %v, drained = %v", err, reader.drained)
	}
}

type trackingReader struct {
	io.Reader
	drained bool
}

func (r *trackingReader) Read(content []byte) (int, error) {
	n, err := r.Reader.Read(content)
	if errors.Is(err, io.EOF) {
		r.drained = true
	}
	return n, err
}

type prepareAdapter struct {
	prepare func(context.Context) (Launch, error)
}

func (prepareAdapter) Name() string       { return "codex" }
func (prepareAdapter) Executable() string { return "sh" }
func (a prepareAdapter) Prepare(ctx context.Context, _, _ string) (Launch, error) {
	return a.prepare(ctx)
}
func (prepareAdapter) ConsumeStdout(stdout io.Reader, _ func(string)) (Observation, error) {
	_, err := io.Copy(io.Discard, stdout)
	return Observation{}, err
}

func commandLaunch(ctx context.Context, script string) Launch {
	return Launch{Command: exec.CommandContext(ctx, "sh", "-c", script)}
}
