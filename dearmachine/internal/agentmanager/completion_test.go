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
	info, err := os.Stat(filepath.Join(manager.TicketDir(id), "ticket-close.md"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("legacy close artifact mode = %v; want 0600", info.Mode().Perm())
	}
}

func TestOversizedWorkerArtifactFailsWithoutOverwrite(t *testing.T) {
	manager := testManager(t, `
request=$(cat)
close_path=$(printf '%s\n' "$request" | sed -n 's/^# Close-Path: //p')
head -c 65537 /dev/zero | tr '\000' x > "$close_path"
`)
	id, err := manager.Send("codex", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitForStatus(t, manager, id, StatusFailed)
	meta := readCompletionMeta(t, manager, id)
	if meta.FailureReason != "close_artifact_unreadable" {
		t.Fatalf("failure metadata = %+v", meta)
	}
	info, err := os.Stat(filepath.Join(manager.TicketDir(id), "ticket-close.md"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != maxTicketCloseBytes+1 {
		t.Fatalf("worker artifact size = %v; content was overwritten", info.Size())
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

func TestCodexYoloPublishesNativeFinalAnswerDespiteConflictingRequest(t *testing.T) {
	manager := testCodexYoloManager(t, `
cat >/dev/null
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"codex-yolo final answer"}}'
printf '%s\n' '{"type":"task_complete"}'
`)
	request := filepath.Join(t.TempDir(), "request.md")
	if err := os.WriteFile(request, []byte("Reply only. Work read-only and do not modify files anywhere.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	id, err := manager.Send("codex-yolo", request, cwd)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitForStatus(t, manager, id, StatusClosed)
	meta := readCompletionMeta(t, manager, id)
	if meta.CompletionSource != CompletionSourceNativeReply {
		t.Fatalf("completion source = %q", meta.CompletionSource)
	}
	content, err := os.ReadFile(filepath.Join(manager.TicketDir(id), "ticket-close.md"))
	if err != nil || string(content) != "codex-yolo final answer" {
		t.Fatalf("close artifact = %q, %v", content, err)
	}
	entries, err := os.ReadDir(cwd)
	if err != nil || len(entries) != 0 {
		t.Fatalf("read-only task workspace changed: %v, %v", entries, err)
	}
}

func TestForgePublishesNativeFinalAnswer(t *testing.T) {
	manager := testForgeManager(t, `cat >/dev/null; printf '%s\n' 'TASK COMPLETED: forge final answer'`)
	id, err := manager.Send("forge", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitForStatus(t, manager, id, StatusClosed)
	meta := readCompletionMeta(t, manager, id)
	if meta.CompletionSource != CompletionSourceNativeReply {
		t.Fatalf("completion source = %q", meta.CompletionSource)
	}
	content, err := os.ReadFile(filepath.Join(manager.TicketDir(id), "ticket-close.md"))
	if err != nil || string(content) != "TASK COMPLETED: forge final answer" {
		t.Fatalf("close artifact = %q, %v", content, err)
	}
}

func TestOMPPublishesNativeFinalAnswer(t *testing.T) {
	manager := testOMPManager(t, `printf '%s' 'TASK COMPLETED: OMP final answer'`)
	id, err := manager.Send("omp", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitForStatus(t, manager, id, StatusClosed)
	meta := readCompletionMeta(t, manager, id)
	if meta.CompletionSource != CompletionSourceNativeReply {
		t.Fatalf("completion source = %q", meta.CompletionSource)
	}
	content, err := os.ReadFile(filepath.Join(manager.TicketDir(id), "ticket-close.md"))
	if err != nil || string(content) != "TASK COMPLETED: OMP final answer" {
		t.Fatalf("close artifact = %q, %v", content, err)
	}
}

func TestPublishedReplyIsAtomicPrivateAndSizeBounded(t *testing.T) {
	ticketDir := t.TempDir()
	if err := publishReply(ticketDir, strings.Repeat("x", maxTicketCloseBytes+1024)); err != nil {
		t.Fatalf("publishReply: %v", err)
	}
	closePath := filepath.Join(ticketDir, "ticket-close.md")
	info, err := os.Stat(closePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != maxTicketCloseBytes || info.Mode().Perm() != 0o600 {
		t.Fatalf("close artifact size/mode = %d/%o", info.Size(), info.Mode().Perm())
	}
	temporary, err := filepath.Glob(filepath.Join(ticketDir, ".ticket-close-*.tmp"))
	if err != nil || len(temporary) != 0 {
		t.Fatalf("temporary artifacts = %v, %v", temporary, err)
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
	waitForStatus(t, manager, id, StatusFailed)
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

func TestWhitespaceOnlyReplyIsIncompleteWithoutCloseArtifact(t *testing.T) {
	manager := testManager(t, `cat >/dev/null; printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":" \n\t "}}'`)
	id, err := manager.Send("codex", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitForStatus(t, manager, id, StatusIncomplete)
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
	waitForStatus(t, manager, id, StatusFailed)
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
			if meta.Status != StatusFailed || meta.FailureReason != test.reason {
				t.Fatalf("failure metadata = %+v", meta)
			}
		})
	}
}

func TestSupervisorBoundsPersistedStderrTail(t *testing.T) {
	manager := testManager(t, `cat >/dev/null; head -c 131072 /dev/zero | tr '\000' x > stderr.bin; printf tail-marker >> stderr.bin; cat stderr.bin >&2; exit 9`)
	cwd := t.TempDir()
	id, err := manager.Send("codex", writeRequest(t), cwd)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitForStatus(t, manager, id, StatusFailed)
	meta := readCompletionMeta(t, manager, id)
	if len(meta.StderrTail) != 64*1024 || !strings.HasSuffix(meta.StderrTail, "tail-marker") {
		tail := meta.StderrTail
		if len(tail) > 32 {
			tail = tail[len(tail)-32:]
		}
		source, sourceErr := os.ReadFile(filepath.Join(cwd, "stderr.bin"))
		sourceTail := source
		if len(sourceTail) > 32 {
			sourceTail = sourceTail[len(sourceTail)-32:]
		}
		t.Fatalf("stderr tail length = %d, suffix = %q; source suffix = %q, %v", len(meta.StderrTail), tail, sourceTail, sourceErr)
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
	if meta.Status != StatusFailed || meta.FailureReason != "metadata_write_failed" || meta.Signal == "" {
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
	second := waitForCancellationSettled(t, manager, id)
	if second.Status != StatusCancelled || second.FinishedAt != first.FinishedAt {
		t.Fatalf("status after repeated cancellation = %+v; first = %+v", second, first)
	}
}

func TestContextCancellationIsNotClassifiedAsCrash(t *testing.T) {
	manager, id := superviseFixture(t)
	manager.Adapters["codex"] = prepareAdapter{prepare: func(ctx context.Context) (Launch, error) {
		return commandLaunch(ctx, `exec sleep 30`), nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := manager.Supervise(ctx, id); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Supervise error = %v", err)
	}
	meta := readCompletionMeta(t, manager, id)
	if meta.Status != StatusCancelled || meta.FailureReason != "context_cancelled" || meta.Signal == "" {
		t.Fatalf("cancellation metadata = %+v", meta)
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

func testCodexYoloManager(t *testing.T, script string) *Manager {
	t.Helper()
	bin := t.TempDir()
	executable := filepath.Join(bin, "codex")
	content := []byte("#!/bin/sh\nset -eu\n" + script + "\n")
	if err := os.WriteFile(executable, content, 0o700); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	manager := New(filepath.Join(t.TempDir(), "agent-manager"))
	manager.Adapters = map[string]Adapter{"codex-yolo": CodexYoloAdapter{}}
	manager.ApprovedBackends = []string{"codex-yolo"}
	manager.LaunchSupervisor = func(id string) error {
		go func() { _ = manager.Supervise(context.Background(), id) }()
		return nil
	}
	return manager
}

func testOMPManager(t *testing.T, script string) *Manager {
	t.Helper()
	bin := t.TempDir()
	executable := filepath.Join(bin, "omp")
	content := []byte("#!/bin/sh\nset -eu\n" + script + "\n")
	if err := os.WriteFile(executable, content, 0o700); err != nil {
		t.Fatalf("write fake OMP: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	manager := New(filepath.Join(t.TempDir(), "agent-manager"))
	manager.Adapters = map[string]Adapter{"omp": OMPAdapter{}}
	manager.ApprovedBackends = []string{"omp"}
	manager.LaunchSupervisor = func(id string) error {
		go func() { _ = manager.Supervise(context.Background(), id) }()
		return nil
	}
	return manager
}
