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
	"time"
)

type completionMeta struct {
	Status           string `json:"status"`
	CompletionSource string `json:"completion_source"`
	FailureReason    string `json:"failure_reason"`
	StderrTail       string `json:"stderr_tail"`
	ExitCode         *int   `json:"exit_code"`
	Signal           string `json:"signal"`
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
	if meta := readCompletionMeta(t, manager, id); meta.CompletionSource != "worker_artifact" {
		t.Fatalf("completion source = %q, want worker_artifact", meta.CompletionSource)
	}
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
	if meta := readCompletionMeta(t, manager, id); meta.CompletionSource != "native_reply" {
		t.Fatalf("completion source = %q, want native_reply", meta.CompletionSource)
	}
	info, err := os.Stat(filepath.Join(manager.TicketDir(id), "ticket-close.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("close artifact mode = %o, want 600", got)
	}
	if info.Size() == 0 {
		t.Fatal("close artifact is empty")
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
	if meta.CompletionSource != "" {
		t.Fatalf("completion source = %q, want empty", meta.CompletionSource)
	}
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
	if meta.FailureReason != "empty_reply" || meta.ExitCode == nil || *meta.ExitCode != 0 || meta.Signal != "" {
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
	if meta := readCompletionMeta(t, manager, id); meta.FailureReason != "output_failed" || meta.ExitCode == nil || *meta.ExitCode != 0 {
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

func TestSupervisorPersistsEarlyFailureReasons(t *testing.T) {
	tests := []struct {
		name    string
		reason  string
		adapter Adapter
		missing bool
	}{
		{
			name:    "missing adapter",
			reason:  "missing_adapter",
			missing: true,
		},
		{
			name:   "prepare",
			reason: "prepare_failed",
			adapter: prepareAdapter{prepare: func(context.Context) (Launch, error) {
				return Launch{}, errors.New("prepare exploded")
			}},
		},
		{
			name:   "stdout pipe",
			reason: "stdout_pipe_failed",
			adapter: prepareAdapter{prepare: func(ctx context.Context) (Launch, error) {
				launch := commandLaunch(ctx, "exit 0")
				launch.Command.Stdout = io.Discard
				return launch, nil
			}},
		},
		{
			name:   "stderr pipe",
			reason: "stderr_pipe_failed",
			adapter: prepareAdapter{prepare: func(ctx context.Context) (Launch, error) {
				launch := commandLaunch(ctx, "exit 0")
				launch.Command.Stderr = io.Discard
				return launch, nil
			}},
		},
		{
			name:   "start",
			reason: "start_failed",
			adapter: prepareAdapter{prepare: func(ctx context.Context) (Launch, error) {
				return Launch{Command: exec.CommandContext(ctx, "/definitely/not/a/worker")}, nil
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, id := superviseFixture(t)
			if test.missing {
				delete(manager.Adapters, "codex")
			} else if test.adapter != nil {
				manager.Adapters["codex"] = test.adapter
			}
			if err := manager.Supervise(context.Background(), id); err == nil {
				t.Fatal("Supervise unexpectedly succeeded")
			}
			meta := readCompletionMeta(t, manager, id)
			if meta.Status != StatusCrashed || meta.FailureReason != test.reason {
				t.Fatalf("failure metadata = %+v", meta)
			}
		})
	}
}

func TestSupervisorBoundsPersistedStderrTail(t *testing.T) {
	manager := testManager(t, `cat >/dev/null; head -c 131072 /dev/zero | tr '\000' x >&2; printf tail-marker >&2; exit 9`)
	id, err := manager.Send("codex", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitForStatus(t, manager, id, StatusCrashed)
	meta := readCompletionMeta(t, manager, id)
	if len(meta.StderrTail) != 64*1024 || !strings.HasSuffix(meta.StderrTail, "tail-marker") {
		t.Fatalf("stderr tail length = %d, suffix present = %v", len(meta.StderrTail), strings.HasSuffix(meta.StderrTail, "tail-marker"))
	}
}

func TestSupervisorPersistsInitialMetadataWriteFailure(t *testing.T) {
	manager, id := superviseFixture(t)
	manager.Adapters["codex"] = prepareAdapter{prepare: func(ctx context.Context) (Launch, error) {
		return commandLaunch(ctx, `cat >/dev/null; exec sleep 30`), nil
	}}
	failNextStartedWrite := true
	manager.beforeWriteMeta = func(meta Meta) error {
		if failNextStartedWrite && meta.PID != 0 {
			failNextStartedWrite = false
			return errors.New("metadata storage unavailable")
		}
		return nil
	}
	if err := manager.Supervise(context.Background(), id); err == nil {
		t.Fatal("Supervise unexpectedly succeeded")
	}
	meta := readCompletionMeta(t, manager, id)
	if meta.Status != StatusCrashed || meta.FailureReason != "metadata_write_failed" || meta.Signal == "" {
		t.Fatalf("failure metadata = %+v", meta)
	}
}

func TestAcceptedCancellationWinsAndIsIdempotent(t *testing.T) {
	manager := testManager(t, `cat >/dev/null; exec sleep 30`)
	id, err := manager.Send("codex", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitForPID(t, manager, id)
	if err := manager.Cancel(id); err != nil {
		t.Fatalf("first Cancel: %v", err)
	}
	first, err := manager.Status(id)
	if err != nil || first.Status != StatusCancelled {
		t.Fatalf("status after cancellation = %+v, %v", first, err)
	}
	if err := manager.Cancel(id); err != nil {
		t.Fatalf("second Cancel: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	second, err := manager.Status(id)
	if err != nil || second.Status != StatusCancelled || second.FinishedAt != first.FinishedAt {
		t.Fatalf("status after repeated cancellation = %+v, %v; first = %+v", second, err, first)
	}
}

func TestCancelAfterTerminalCompletionIsNoOp(t *testing.T) {
	manager := testManager(t, `cat >/dev/null; printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"done"}}'`)
	id, err := manager.Send("codex", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	closed := waitForStatus(t, manager, id, StatusClosed)
	if err := manager.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	after, err := manager.Status(id)
	if err != nil || after.Status != StatusClosed || after.FinishedAt != closed.FinishedAt {
		t.Fatalf("status after Cancel = %+v, %v; closed = %+v", after, err, closed)
	}
}

func superviseFixture(t *testing.T) (*Manager, string) {
	t.Helper()
	manager := New(filepath.Join(t.TempDir(), "agent-manager"))
	id := "ticket-1"
	if err := os.MkdirAll(manager.TicketDir(id), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manager.TicketDir(id), "ticket-open.md"), []byte("request\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.writeMeta(Meta{
		TicketID:  id,
		Worker:    "codex",
		Status:    StatusOpen,
		CreatedAt: manager.Now(),
		CWD:       t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	return manager, id
}
