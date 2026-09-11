package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func serveTest(t *testing.T, cfg Config) string {
	t.Helper()
	if cfg.StateDir == "" {
		cfg.StateDir = privateTempDir(t)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("supervisor did not exit")
		}
	})
	awaitStatus(t, cfg.StateDir, func(s Status) bool { return s.Supervisor != "" })
	return cfg.StateDir
}

func awaitStatus(t *testing.T, root string, check func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, err := Request(root, "status", time.Second)
		if err == nil && check(s) {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("expected supervisor state not reached")
	return Status{}
}

func TestControlLifecycle(t *testing.T) {
	root := serveTest(t, Config{Command: []string{testExecutable(t, "sleep"), "60"}})
	initial := awaitStatus(t, root, func(s Status) bool { return s.Supervisor == "running" })
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Request(root, "up", time.Second)
			if err != nil || s.DaemonPID != initial.DaemonPID {
				t.Errorf("concurrent up: %+v %v", s, err)
			}
		}()
	}
	wg.Wait()
	next, err := Request(root, "restart", time.Second)
	if err != nil || next.Daemon != "running" || next.DaemonPID == initial.DaemonPID {
		t.Fatalf("restart: %+v %v", next, err)
	}
	if err := Run(context.Background(), Config{StateDir: root, Command: []string{testExecutable(t, "sleep"), "60"}}); err != ErrLocked {
		t.Fatalf("duplicate: %v", err)
	}
	for range 2 {
		s, err := Request(root, "down", time.Second)
		if err != nil || s.Supervisor != "stopped" || s.DaemonPID != 0 {
			t.Fatalf("down: %+v %v", s, err)
		}
	}
	s, err := Request(root, "up", time.Second)
	if err != nil || s.Daemon != "running" {
		t.Fatalf("up after stop: %+v %v", s, err)
	}
}

func TestBackoffBoundAndStop(t *testing.T) {
	root := serveTest(t, Config{Command: []string{testExecutable(t, "sh"), "-c", "exit 7"}, Ready: func(int) bool { return false }, InitialBackoff: 80 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, MaxFailures: 3})
	s := awaitStatus(t, root, func(s Status) bool { return s.Supervisor == "backing-off" })
	if s.RetryInMs == nil || *s.RetryInMs > 100 || !strings.Contains(s.LastExit, "7") {
		t.Fatalf("backoff: %+v", s)
	}
	if s.ConsecutiveFailures < 1 || s.ConsecutiveFailures >= 3 || s.FailureLimit != 3 {
		t.Fatalf("retry diagnostics: %+v", s)
	}
	s = awaitStatus(t, root, func(s Status) bool { return s.Supervisor == "failed" })
	if s.ConsecutiveFailures != 3 || s.FailureLimit != 3 {
		t.Fatalf("failure limit diagnostics: %+v", s)
	}
	if _, err := Request(root, "up", time.Second); err == nil {
		t.Fatal("crashing child reported successful up")
	}
	awaitStatus(t, root, func(s Status) bool { return s.Supervisor == "backing-off" })
	s, err := Request(root, "down", time.Second)
	if err != nil || s.Supervisor != "stopped" {
		t.Fatalf("stop backoff: %+v %v", s, err)
	}
	time.Sleep(150 * time.Millisecond)
	awaitStatus(t, root, func(s Status) bool { return s.Supervisor == "stopped" })
}

func TestReadinessAndStopDuringStartup(t *testing.T) {
	root := serveTest(t, Config{Command: []string{testExecutable(t, "sleep"), "60"}, Ready: func(int) bool { return false }, StartupTimeout: time.Second})
	s, err := Request(root, "status", time.Second)
	if err != nil || s.Supervisor != "starting" || s.Daemon != "unknown" {
		t.Fatalf("readiness: %+v %v", s, err)
	}
	pending := make(chan error, 1)
	go func() { _, err := Request(root, "up", time.Second); pending <- err }()
	time.Sleep(20 * time.Millisecond)
	if _, err := Request(root, "down", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := <-pending; err == nil {
		t.Fatal("cancelled startup reported success")
	}
}

func TestProtocolAndStaleSocket(t *testing.T) {
	root := privateTempDir(t)
	if err := os.Mkdir(filepath.Join(root, "run"), 0700); err != nil {
		t.Fatal(err)
	}
	stale, err := net.Listen("unix", SocketPath(root))
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	serveTest(t, Config{StateDir: root, Command: []string{testExecutable(t, "sleep"), "60"}})
	info, _ := os.Stat(SocketPath(root))
	if info.Mode().Perm() != 0600 {
		t.Fatal("socket not private")
	}
	for _, input := range []string{`{"version":2,"command":"down"}`, `{"version":1,"command":"bogus"}`, `not json`, strings.Repeat("x", 65537)} {
		conn, err := net.Dial("unix", SocketPath(root))
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(time.Second))
		conn.Write([]byte(input + "\n"))
		var reply Response
		err = json.NewDecoder(bufio.NewReader(conn)).Decode(&reply)
		conn.Close()
		if err != nil || reply.OK {
			t.Fatalf("invalid request: %+v %v", reply, err)
		}
	}
	awaitStatus(t, root, func(s Status) bool { return s.Supervisor == "running" })
}

func TestBackoffPolicy(t *testing.T) {
	cfg := Config{InitialBackoff: time.Second, MaxBackoff: 30 * time.Second}
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second} {
		if got := backoff(cfg, i+1); got != want {
			t.Fatalf("failure %d: %s", i+1, got)
		}
	}
}

func TestHealthyRunResetsFailures(t *testing.T) {
	root := serveTest(t, Config{Command: []string{testExecutable(t, "sh"), "-c", "echo launched; sleep 0.1; exit 9"}, HealthyRun: 30 * time.Millisecond, InitialBackoff: 10 * time.Millisecond, MaxFailures: 2})
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, _ := os.ReadFile(LogPath(root))
		if strings.Count(string(data), "launched") >= 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("healthy runs did not reset failure count")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := Request(root, "down", time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestStartupTimeoutFailsAndReaps(t *testing.T) {
	root := serveTest(t, Config{Command: []string{testExecutable(t, "sleep"), "60"}, Ready: func(int) bool { return false }, StartupTimeout: 30 * time.Millisecond, MaxFailures: 1})
	s := awaitStatus(t, root, func(s Status) bool { return s.Supervisor == "failed" })
	if s.DaemonPID != 0 || !strings.Contains(s.LastExit, "readiness timeout") {
		t.Fatalf("timeout: %+v", s)
	}
}

func TestLaunchOptionsAreNotSilentlyIgnored(t *testing.T) {
	sleep := testExecutable(t, "sleep")
	root := serveTest(t, Config{Command: []string{sleep, "60"}})
	awaitStatus(t, root, func(s Status) bool { return s.Supervisor == "running" })
	if _, err := RequestUp(root, []string{sleep, "61"}, time.Second); err == nil {
		t.Fatal("changed running command accepted")
	}
	if _, err := Request(root, "down", time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := RequestUp(root, []string{sleep, "61"}, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestClientTimeoutDoesNotUndoCommittedUp(t *testing.T) {
	var ready atomic.Bool
	root := serveTest(t, Config{Command: []string{testExecutable(t, "sleep"), "60"}, Ready: func(int) bool { return ready.Load() }})
	if _, err := Request(root, "up", 20*time.Millisecond); err == nil {
		t.Fatal("unready child reported success")
	}
	ready.Store(true)
	awaitStatus(t, root, func(s Status) bool { return s.Supervisor == "running" })
}

func TestOwnershipCheckPreventsChildSpawn(t *testing.T) {
	root := privateTempDir(t)
	marker := filepath.Join(root, "unexpected")
	serveTest(t, Config{StateDir: root, Command: []string{testExecutable(t, "touch"), marker}, BeforeStart: func() error { return errors.New("another daemon owner") }, MaxFailures: 1})
	s := awaitStatus(t, root, func(s Status) bool { return s.Supervisor == "failed" })
	if !strings.Contains(s.LastExit, "another daemon owner") {
		t.Fatalf("ownership: %+v", s)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("spawned despite another owner")
	}
}

func TestLaunchDirectoryFollowsNativeUp(t *testing.T) {
	root := privateTempDir(t)
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	for _, dir := range []string{first, second} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	argv := []string{testExecutable(t, "sh"), "-c", "pwd; exec sleep 60"}
	serveTest(t, Config{StateDir: root, Command: argv, Directory: first})
	awaitStatus(t, root, func(s Status) bool { return s.Supervisor == "running" })
	if _, err := RequestUpInDirectory(root, argv, second, time.Second); err == nil {
		t.Fatal("changed a running launch directory")
	}
	if _, err := Request(root, "down", time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := RequestUpInDirectory(root, argv, second, time.Second); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(LogPath(root))
	if err != nil || !strings.Contains(string(data), first+"\n") || !strings.Contains(string(data), second+"\n") {
		t.Fatalf("launch directories: %q %v", data, err)
	}
}

func TestDefaultSocketPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	got, err := DefaultSocketPath(os.UserHomeDir)
	if err != nil || got != filepath.Join(home, ".dearmachine", "run", "supervisor.sock") {
		t.Fatalf("default endpoint: %q %v", got, err)
	}
	for _, home := range []string{"", "relative"} {
		if _, err := DefaultSocketPath(func() (string, error) { return home, nil }); err == nil {
			t.Fatalf("accepted home %q", home)
		}
	}
	t.Setenv("HOME", "")
	if _, err := DefaultSocketPath(os.UserHomeDir); err == nil {
		t.Fatal("missing HOME must fail, never fall back to XDG or passwd")
	}
}

func TestTSShapedControlRequests(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := serveTest(t, Config{StateDir: filepath.Join(home, ".dearmachine"), Command: []string{testExecutable(t, "sleep"), "60"}})
	awaitStatus(t, root, func(s Status) bool { return s.Supervisor == "running" })
	socket, err := DefaultSocketPath(os.UserHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"status", "down", "up", "restart"} {
		conn, err := net.Dial("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(time.Second))
		// Literal TS wire envelope, without Go-only argv/directory extensions.
		conn.Write([]byte(`{"version":1,"command":"` + command + `"}` + "\n"))
		var reply Response
		err = json.NewDecoder(conn).Decode(&reply)
		conn.Close()
		want := "running"
		if command == "down" {
			want = "stopped"
		}
		if err != nil || !reply.OK || reply.Version != 1 || reply.Status.Daemon != want {
			t.Fatalf("%s: %+v %v", command, reply, err)
		}
	}
}

func TestShutdownReleasesSupervisorForUpdate(t *testing.T) {
	root := serveTest(t, Config{Command: []string{testExecutable(t, "sleep"), "60"}})
	awaitStatus(t, root, func(s Status) bool { return s.Supervisor == "running" })
	s, err := Request(root, "shutdown", time.Second)
	if err != nil || s.Daemon != "stopped" {
		t.Fatalf("shutdown: %+v %v", s, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := CheckAvailable(root); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("shutdown retained supervisor ownership")
}
