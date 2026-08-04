package agentmanager

import (
	"context"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type shellAdapter struct {
	name          string
	script        string
	nativeSession string
	consumeStdout func(io.Reader, func(string)) error
}

func (a shellAdapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "codex"
}
func (shellAdapter) Executable() string { return "sh" }
func (a shellAdapter) Prepare(ctx context.Context, cwd, _ string) (Launch, error) {
	command := exec.CommandContext(ctx, "sh", "-c", a.script)
	command.Dir = cwd
	return Launch{Command: command, NativeSession: a.nativeSession}, nil
}
func (a shellAdapter) ConsumeStdout(stdout io.Reader, foundSession func(string)) error {
	if a.consumeStdout != nil {
		return a.consumeStdout(stdout, foundSession)
	}
	return (CodexAdapter{}).ConsumeStdout(stdout, foundSession)
}

func TestCodexAdapterAddsTicketDirectory(t *testing.T) {
	launch, err := (CodexAdapter{}).Prepare(context.Background(), "/project", "/tickets/ticket-1")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	command := launch.Command
	if command.Dir != "/project" {
		t.Fatalf("command directory = %q", command.Dir)
	}
	want := []string{"codex", "exec", "--json", "--add-dir", "/tickets/ticket-1", "-"}
	if !slices.Equal(command.Args, want) {
		t.Fatalf("command args = %v, want %v", command.Args, want)
	}
}

func TestForgecodeAdapterInvokesForgeDirectly(t *testing.T) {
	adapter := ForgecodeAdapter{newSessionID: func() (string, error) {
		return "029a3702-f8fa-470f-8a28-190c0f53410e", nil
	}}
	launch, err := adapter.Prepare(context.Background(), "/project", "/tickets/ticket-1")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if launch.Command.Dir != "/project" {
		t.Fatalf("command directory = %q", launch.Command.Dir)
	}
	want := []string{"forge", "--conversation-id", "029a3702-f8fa-470f-8a28-190c0f53410e"}
	if !slices.Equal(launch.Command.Args, want) {
		t.Fatalf("command args = %v, want %v", launch.Command.Args, want)
	}
	if launch.NativeSession != want[2] {
		t.Fatalf("native session = %q, want %q", launch.NativeSession, want[2])
	}
}

func TestNewRegistersForgecodeAdapter(t *testing.T) {
	manager := New(t.TempDir())
	adapter, ok := manager.Adapters["forgecode"]
	if !ok || adapter.Name() != "forgecode" || adapter.Executable() != "forge" {
		t.Fatalf("forgecode adapter = %#v, found = %v", adapter, ok)
	}
}

func TestNewUUIDv4(t *testing.T) {
	actual, err := newUUIDv4()
	if err != nil {
		t.Fatalf("newUUIDv4: %v", err)
	}
	parts := strings.Split(actual, "-")
	if len(parts) != 5 || len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
		t.Fatalf("UUID format = %q", actual)
	}
	if _, err := hex.DecodeString(strings.Join(parts, "")); err != nil {
		t.Fatalf("UUID is not hexadecimal: %q: %v", actual, err)
	}
	if actual[14] != '4' || !strings.ContainsRune("89ab", rune(actual[19])) {
		t.Fatalf("UUID version or variant = %q", actual)
	}
}

func TestTicketLifecycle(t *testing.T) {
	manager := testManager(t, `
request=$(cat)
close_path=$(printf '%s\n' "$request" | sed -n 's/^# Close-Path: //p')
printf '%s\n' '{"type":"thread.started","thread_id":"native-123"}'
printf '%s\n' 'worker complete' > "$close_path"
`)
	request := filepath.Join(t.TempDir(), "request.md")
	if err := os.WriteFile(request, []byte("make the change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := manager.Send("codex", request, t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	meta := waitForStatus(t, manager, id, StatusClosed)
	if meta.NativeSession != "native-123" || meta.PID == 0 {
		t.Fatalf("metadata = %+v", meta)
	}
	var view strings.Builder
	if err := manager.View(id, &view); err != nil {
		t.Fatalf("View: %v", err)
	}
	for _, want := range []string{"make the change", "worker complete", "Close-Path:"} {
		if !strings.Contains(view.String(), want) {
			t.Errorf("view missing %q: %s", want, view.String())
		}
	}
	listed, err := manager.List("codex")
	if err != nil || len(listed) != 1 || listed[0].TicketID != id {
		t.Fatalf("List = %+v, %v", listed, err)
	}
}

func TestTicketCrash(t *testing.T) {
	manager := testManager(t, `printf '%s\n' '{"type":"thread.started","thread_id":"native-crash"}'; exit 2`)
	request := writeRequest(t)
	id, err := manager.Send("codex", request, t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	meta := waitForStatus(t, manager, id, StatusCrashed)
	if meta.NativeSession != "native-crash" {
		t.Fatalf("metadata = %+v", meta)
	}
}

func TestTicketCancelAndCancelAll(t *testing.T) {
	manager := testManager(t, `printf '%s\n' '{"type":"thread.started","thread_id":"native-long"}'; sleep 30`)
	first, err := manager.Send("codex", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Send("codex", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	waitForPID(t, manager, first)
	waitForPID(t, manager, second)
	if err := manager.Cancel(first); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got, _ := manager.Status(first); got.Status != StatusCancelled {
		t.Fatalf("first status = %s", got.Status)
	}
	if err := manager.CancelAll(); err != nil {
		t.Fatalf("CancelAll: %v", err)
	}
	if got, _ := manager.Status(second); got.Status != StatusCancelled {
		t.Fatalf("second status = %s", got.Status)
	}
}

func TestForgecodeTicketLifecycleDrainsLargeOutput(t *testing.T) {
	manager := testForgeManager(t, `
request=$(cat)
close_path=$(printf '%s\n' "$request" | sed -n 's/^# Close-Path: //p')
head -c 2097152 /dev/zero | tr '\000' x
printf '%s\n' 'forge worker complete' > "$close_path"
`)
	id, err := manager.Send("forgecode", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	meta := waitForStatus(t, manager, id, StatusClosed)
	if meta.NativeSession != "029a3702-f8fa-470f-8a28-190c0f53410e" || meta.PID == 0 {
		t.Fatalf("metadata = %+v", meta)
	}
	var view strings.Builder
	if err := manager.View(id, &view); err != nil {
		t.Fatalf("View: %v", err)
	}
	if !strings.Contains(view.String(), "forge worker complete") {
		t.Fatalf("view = %s", view.String())
	}
}

func TestForgecodeTicketCrash(t *testing.T) {
	manager := testForgeManager(t, `cat >/dev/null; printf '%s' 'ordinary forge output'; exit 2`)
	id, err := manager.Send("forgecode", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	meta := waitForStatus(t, manager, id, StatusCrashed)
	if meta.NativeSession != "029a3702-f8fa-470f-8a28-190c0f53410e" {
		t.Fatalf("metadata = %+v", meta)
	}
}

func TestForgecodeTicketCancel(t *testing.T) {
	manager := testForgeManager(t, `cat >/dev/null; exec sleep 30`)
	id, err := manager.Send("forgecode", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitForPID(t, manager, id)
	if err := manager.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got, _ := manager.Status(id); got.Status != StatusCancelled {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestWorkerHealth(t *testing.T) {
	manager := testManager(t, "exit 0")
	path, err := manager.WorkerHealth("codex")
	if err != nil || filepath.Base(path) != "sh" {
		t.Fatalf("WorkerHealth = %q, %v", path, err)
	}
	if _, err := manager.WorkerHealth("other"); err == nil {
		t.Fatal("unknown worker health succeeded")
	}
}

func testManager(t *testing.T, script string) *Manager {
	t.Helper()
	manager := New(filepath.Join(t.TempDir(), "agent-manager"))
	manager.Adapters = map[string]Adapter{"codex": shellAdapter{script: script}}
	manager.LaunchSupervisor = func(id string) error {
		go func() { _ = manager.Supervise(context.Background(), id) }()
		return nil
	}
	return manager
}

func testForgeManager(t *testing.T, script string) *Manager {
	t.Helper()
	bin := t.TempDir()
	executable := filepath.Join(bin, "forge")
	content := []byte("#!/bin/sh\nset -eu\n" + script + "\n")
	if err := os.WriteFile(executable, content, 0o700); err != nil {
		t.Fatalf("write fake forge: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	manager := New(filepath.Join(t.TempDir(), "agent-manager"))
	manager.Adapters = map[string]Adapter{
		"forgecode": ForgecodeAdapter{newSessionID: func() (string, error) {
			return "029a3702-f8fa-470f-8a28-190c0f53410e", nil
		}},
	}
	manager.LaunchSupervisor = func(id string) error {
		go func() { _ = manager.Supervise(context.Background(), id) }()
		return nil
	}
	return manager
}

func writeRequest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "request.md")
	if err := os.WriteFile(path, []byte("test request\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitForPID(t *testing.T, manager *Manager, id string) Meta {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		meta, err := manager.Status(id)
		if err == nil && meta.PID != 0 {
			return meta
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("ticket %s did not start", id)
	return Meta{}
}

func waitForStatus(t *testing.T, manager *Manager, id, status string) Meta {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		meta, err := manager.Status(id)
		if err == nil && meta.Status == status {
			return meta
		}
		time.Sleep(10 * time.Millisecond)
	}
	meta, err := manager.Status(id)
	t.Fatalf("ticket %s status = %+v, %v; want %s", id, meta, err, status)
	return Meta{}
}
