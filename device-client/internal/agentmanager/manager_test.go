package agentmanager

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type shellAdapter struct {
	script string
}

func (shellAdapter) Name() string       { return "codex" }
func (shellAdapter) Executable() string { return "sh" }
func (a shellAdapter) Command(ctx context.Context, cwd string) *exec.Cmd {
	command := exec.CommandContext(ctx, "sh", "-c", a.script)
	command.Dir = cwd
	return command
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
