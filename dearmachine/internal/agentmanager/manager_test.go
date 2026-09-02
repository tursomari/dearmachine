package agentmanager

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	backendcatalog "github.com/dearmachine/dearmachine/internal/backends"
)

type shellAdapter struct {
	name          string
	executable    string
	script        string
	nativeSession string
	consumeStdout func(io.Reader, func(string)) (Observation, error)
}

func (a shellAdapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "codex"
}
func (a shellAdapter) Executable() string {
	if a.executable != "" {
		return a.executable
	}
	return "sh"
}
func (a shellAdapter) Prepare(ctx context.Context, cwd, _ string) (Launch, error) {
	command := exec.CommandContext(ctx, "sh", "-c", a.script)
	command.Dir = cwd
	return Launch{Command: command, NativeSession: a.nativeSession}, nil
}
func (a shellAdapter) ConsumeStdout(stdout io.Reader, foundSession func(string)) (Observation, error) {
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
	want := []string{
		"codex",
		"exec",
		"--skip-git-repo-check",
		"--json",
		"--sandbox",
		"workspace-write",
		"--add-dir",
		"/tickets/ticket-1",
		"-",
	}
	if !slices.Equal(command.Args, want) {
		t.Fatalf("command args = %v, want %v", command.Args, want)
	}
}

func TestCodexYoloAdapterBypassesApprovalsAndSandbox(t *testing.T) {
	launch, err := (CodexYoloAdapter{}).Prepare(context.Background(), "/project", "/tickets/ticket-1")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if launch.Command.Dir != "/project" {
		t.Fatalf("command directory = %q", launch.Command.Dir)
	}
	want := []string{
		"codex",
		"exec",
		"--skip-git-repo-check",
		"--json",
		"--dangerously-bypass-approvals-and-sandbox",
		"-",
	}
	if !slices.Equal(launch.Command.Args, want) {
		t.Fatalf("command args = %v, want %v", launch.Command.Args, want)
	}
}

func TestForgeAdapterInvokesForgeDirectly(t *testing.T) {
	adapter := ForgeAdapter{newSessionID: func() (string, error) {
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

func TestOMPAdapterInvokesConfiguredOMPNoninteractively(t *testing.T) {
	launch, err := (OMPAdapter{}).Prepare(context.Background(), "/project", "/tickets/ticket-1")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if launch.Command.Dir != "/project" {
		t.Fatalf("command directory = %q", launch.Command.Dir)
	}
	want := []string{"omp", "--print", "--mode", "text", "--no-session", "--no-pty", "--auto-approve"}
	if !slices.Equal(launch.Command.Args, want) {
		t.Fatalf("command args = %v, want %v", launch.Command.Args, want)
	}
	if !launch.PromptArgument {
		t.Fatal("OMP launch does not request its prompt as a positional argument")
	}
}

func TestNewRegistersBuiltInPlainTextAdapters(t *testing.T) {
	manager := New(t.TempDir())
	for _, id := range []string{"forge", "omp"} {
		adapter, ok := manager.Adapters[id]
		if !ok || adapter.Name() != id || adapter.Executable() != id {
			t.Fatalf("%s adapter = %#v, found = %v", id, adapter, ok)
		}
	}
}

func TestAdapterRegistryMatchesSharedCatalog(t *testing.T) {
	manager := New(t.TempDir())
	registered := backendcatalog.All()
	if len(manager.Adapters) < len(registered) {
		t.Fatalf("adapter count = %d, catalog count = %d", len(manager.Adapters), len(registered))
	}
	for _, backend := range registered {
		adapter, ok := manager.Adapters[backend.ID]
		if !ok {
			t.Errorf("catalog backend %q has no adapter", backend.ID)
			continue
		}
		if adapter.Name() != backend.ID || adapter.Executable() != backend.Executable {
			t.Errorf("backend %q adapter = %q/%q, want %q/%q", backend.ID, adapter.Name(), adapter.Executable(), backend.ID, backend.Executable)
		}
	}
	// Custom adapters may exist beyond the catalog, so we do not
	// require every adapter to have a corresponding catalog entry.
	// for id := range manager.Adapters {
	// 	if _, ok := backendcatalog.Lookup(id); !ok {
	// 		t.Errorf("adapter %q has no catalog entry", id)
	// 	}
	// }
}

func TestConfigureFromDeviceConfigResolvesBuiltInAndCustomBackends(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	codex := filepath.Join(bin, "codex")
	if err := os.WriteFile(codex, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(t.TempDir(), "custom-backend")
	if err := os.WriteFile(custom, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "dearmachine.toml")
	if err := os.WriteFile(configPath, []byte("version = 1\nbackends = [\"codex-yolo\", \"custom\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	customConfig := "[custom]\nexecutable = \"" + custom + "\"\noutput_format = \"plain\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "custom-backends.toml"), []byte(customConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	manager := New(filepath.Join(root, "agent-manager"))
	if err := manager.ConfigureFromDeviceConfig(configPath); err != nil {
		t.Fatalf("ConfigureFromDeviceConfig: %v", err)
	}
	resolved, err := manager.ResolveBackends()
	if err != nil {
		t.Fatalf("ResolveBackends: %v", err)
	}
	want := []ResolvedBackend{
		{ID: "codex-yolo", Executable: "codex", Path: codex},
		{ID: "custom", Executable: custom, Path: custom},
	}
	if !slices.Equal(resolved, want) {
		t.Fatalf("ResolveBackends = %#v, want %#v", resolved, want)
	}
}

func TestCodexAdapterObservesSessionAndReply(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"type":"thread.started","thread_id":"native-123"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"probe complete"}}`,
	}, "\n"))
	var session string
	observation, err := (CodexAdapter{}).ConsumeStdout(input, func(found string) { session = found })
	if err != nil || session != "native-123" || observation.Reply != "probe complete" {
		t.Fatalf("ConsumeStdout = %+v, session=%q, err=%v", observation, session, err)
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

func TestSupervisorPublishesNativeReply(t *testing.T) {
	manager := testManager(t, `
cat >/dev/null
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"native worker reply"}}'
`)
	id, err := manager.Send("codex", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	meta := waitForStatus(t, manager, id, StatusClosed)
	content, err := os.ReadFile(filepath.Join(manager.TicketDir(id), "ticket-close.md"))
	if err != nil {
		t.Fatalf("read close ticket: %v", err)
	}
	if string(content) != "native worker reply" {
		t.Fatalf("close ticket = %q", content)
	}
	if meta.Status != StatusClosed {
		t.Fatalf("status = %q", meta.Status)
	}
}

func TestTicketContentPlacesCompletionProtocolAfterConflictingRequest(t *testing.T) {
	request := "Reply only. This is read-only; do not modify files anywhere.\n"
	content := string(ticketContent("ticket-1", "codex", "/tickets/ticket-1/ticket-close.md", []byte(request)))
	if !strings.Contains(content, "# Close-Path: /tickets/ticket-1/ticket-close.md") {
		t.Fatalf("ticket envelope lost Close-Path compatibility header: %s", content)
	}
	requestAt := strings.Index(content, request)
	protocolAt := strings.Index(content, "# Completion protocol")
	if requestAt < 0 || protocolAt < requestAt {
		t.Fatalf("completion protocol does not follow delegated request: %s", content)
	}
	for _, required := range []string{"mandatory control-plane", "explicitly exempt", "read-only", "reply-only", "do-not-modify-files"} {
		if !strings.Contains(content[protocolAt:], required) {
			t.Errorf("completion protocol missing %q: %s", required, content)
		}
	}
}

func TestTicketNonzeroExitFails(t *testing.T) {
	manager := testManager(t, `printf '%s\n' '{"type":"thread.started","thread_id":"native-crash"}'; exit 2`)
	request := writeRequest(t)
	id, err := manager.Send("codex", request, t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	meta := waitForStatus(t, manager, id, StatusFailed)
	if meta.NativeSession != "native-crash" {
		t.Fatalf("metadata = %+v", meta)
	}
}

func TestTicketCancelAndCancelAll(t *testing.T) {
	manager := testManager(t, `printf '%s\n' '{"type":"thread.started","thread_id":"native-long"}'; exec sleep 30`)
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
	waitForCancellationSettled(t, manager, first)
	waitForCancellationSettled(t, manager, second)
}

func TestForgeTicketLifecycleDrainsLargeOutput(t *testing.T) {
	manager := testForgeManager(t, `
request=$(cat)
close_path=$(printf '%s\n' "$request" | sed -n 's/^# Close-Path: //p')
head -c 2097152 /dev/zero | tr '\000' x
printf '%s\n' 'forge worker complete' > "$close_path"
`)
	id, err := manager.Send("forge", writeRequest(t), t.TempDir())
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

func TestForgeTicketNonzeroExitFails(t *testing.T) {
	manager := testForgeManager(t, `cat >/dev/null; printf '%s' 'ordinary forge output'; exit 2`)
	id, err := manager.Send("forge", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	meta := waitForStatus(t, manager, id, StatusFailed)
	if meta.NativeSession != "029a3702-f8fa-470f-8a28-190c0f53410e" {
		t.Fatalf("metadata = %+v", meta)
	}
}

func TestForgeTicketCancel(t *testing.T) {
	manager := testForgeManager(t, `cat >/dev/null; exec sleep 30`)
	id, err := manager.Send("forge", writeRequest(t), t.TempDir())
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
	waitForCancellationSettled(t, manager, id)
}

func TestBackendHealthFileProbe(t *testing.T) {
	for _, backend := range []string{"codex", "codex-yolo", "forge", "omp"} {
		t.Run(backend, func(t *testing.T) {
			bin := t.TempDir()
			executableName := map[string]string{
				"codex": "codex", "codex-yolo": "codex", "forge": "forge", "omp": "omp",
			}[backend]
			executable := filepath.Join(bin, executableName)
			script := `#!/bin/sh
set -eu
`
			if backend == "omp" {
				script += `prompt=
for prompt in "$@"; do :; done
`
			} else {
				script += `prompt=$(cat)
`
			}
			script += `
path=${prompt#*exactly }
path=${path%% containing exactly*}
printf '%s\n' 'Dear Machine, backend health probe.' > "$path"
`
			if backend == "codex" || backend == "codex-yolo" {
				script += `printf '%s\n' '{"type":"thread.started","thread_id":"health-session"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"probe complete"}}'
`
			} else {
				script += `printf '%s\n' '` + backend + ` probe complete'
`
			}
			if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			manager := New(filepath.Join(t.TempDir(), "agent-manager"))
			if err := manager.SetApprovedBackends([]string{backend}); err != nil {
				t.Fatal(err)
			}
			cwd := t.TempDir()
			result, err := manager.BackendHealth(context.Background(), backend, cwd)
			if err != nil || !result.OK || !strings.Contains(result.Reply, "probe complete") {
				t.Fatalf("BackendHealth = %+v, %v", result, err)
			}
			matches, err := filepath.Glob(filepath.Join(cwd, ".healthcheck-probe-*.txt"))
			if err != nil || len(matches) != 0 {
				t.Fatalf("health probe artifacts = %v, %v", matches, err)
			}
		})
	}
}

func TestBackendHealthFailures(t *testing.T) {
	t.Run("file not written", func(t *testing.T) {
		manager := testManager(t, `cat >/dev/null; printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"did nothing"}}'`)
		result, err := manager.BackendHealth(context.Background(), "codex", t.TempDir())
		if err == nil || result.OK || result.Reason != "file-not-written" {
			t.Fatalf("BackendHealth = %+v, %v", result, err)
		}
	})

	t.Run("missing executable", func(t *testing.T) {
		manager := New(t.TempDir())
		manager.Adapters = map[string]Adapter{"codex": shellAdapter{executable: "definitely-not-a-real-agent"}}
		if err := manager.SetApprovedBackends([]string{"codex"}); err != nil {
			t.Fatal(err)
		}
		result, err := manager.BackendHealth(context.Background(), "codex", t.TempDir())
		if err == nil || result.Reason != "unavailable" {
			t.Fatalf("BackendHealth = %+v, %v", result, err)
		}
	})

	t.Run("context timeout", func(t *testing.T) {
		manager := testManager(t, `cat >/dev/null; exec sleep 30`)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		result, err := manager.BackendHealth(ctx, "codex", t.TempDir())
		if err == nil || result.Reason != "cancelled" || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("BackendHealth = %+v, %v", result, err)
		}
	})
}

func TestSendRejectsUnapprovedBackend(t *testing.T) {
	manager := New(t.TempDir())
	if err := manager.SetApprovedBackends([]string{"codex"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Send("forge", writeRequest(t), t.TempDir()); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("Send error = %v", err)
	}
}

func testManager(t *testing.T, script string) *Manager {
	t.Helper()
	manager := New(filepath.Join(t.TempDir(), "agent-manager"))
	manager.Adapters = map[string]Adapter{"codex": shellAdapter{script: script}}
	manager.ApprovedBackends = []string{"codex"}
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
		"forge": ForgeAdapter{newSessionID: func() (string, error) {
			return "029a3702-f8fa-470f-8a28-190c0f53410e", nil
		}},
	}
	manager.ApprovedBackends = []string{"forge"}
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

func waitForCancellationSettled(t *testing.T, manager *Manager, id string) Meta {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		meta, err := manager.Status(id)
		if err == nil && meta.Status == StatusCancelled && meta.FailureReason == "cancelled" {
			return meta
		}
		time.Sleep(10 * time.Millisecond)
	}
	meta, err := manager.Status(id)
	t.Fatalf("ticket %s cancellation did not settle: %+v, %v", id, meta, err)
	return Meta{}
}
