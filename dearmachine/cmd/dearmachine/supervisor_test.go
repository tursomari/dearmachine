package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/supervisor"
)

func supervisedDeps(t *testing.T) (dependencies, string) {
	t.Helper()
	deps := testDependencies(t, &fakeApplication{})
	home := socketTestHome(t)
	deps.userHomeDir = func() (string, error) { return home, nil }
	makeUpTestPair(t, deps, "supervised")
	root := filepath.Join(home, ".dearmachine")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- supervisor.Run(ctx, supervisor.Config{StateDir: root, Command: []string{testExecutable(t, "sleep"), "60"}})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("supervisor did not stop")
		}
	})
	deadline := time.Now().Add(time.Second)
	for {
		s, err := supervisor.Request(root, "status", time.Second)
		if err == nil && s.Daemon == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("supervisor not ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return deps, root
}

func TestCLIReportsAndControlsSupervisor(t *testing.T) {
	deps, root := supervisedDeps(t)
	deps.daemonStatus = managedDaemonStatus
	deps.stopDaemon = stopManagedDaemon
	var output strings.Builder
	deps.stdout = &output
	if err := run([]string{"status"}, os.Getenv, deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Supervisor: running") || !strings.Contains(output.String(), "Crash recovery: active") {
		t.Fatalf("status: %s", output.String())
	}
	if !strings.Contains(output.String(), "other startup mechanisms not inspected") {
		t.Fatalf("startup scope missing: %s", output.String())
	}
	if err := run([]string{"down"}, os.Getenv, deps); err != nil {
		t.Fatal(err)
	}
	status, _ := supervisor.Request(root, "status", time.Second)
	if status.Supervisor != "stopped" {
		t.Fatalf("down: %+v", status)
	}
	if err := run([]string{"restart"}, os.Getenv, deps); err != nil {
		t.Fatal(err)
	}
	status, _ = supervisor.Request(root, "status", time.Second)
	if status.Supervisor != "running" {
		t.Fatalf("restart: %+v", status)
	}
}

func TestStaleSupervisorNeverFallsBackToPIDStop(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	home, _ := deps.userHomeDir()
	root := filepath.Join(home, ".dearmachine")
	os.MkdirAll(filepath.Join(root, "run"), 0700)
	os.WriteFile(filepath.Join(root, "run", "supervisor.lock"), nil, 0600)
	lock, _ := client.DefaultDaemonLockPath(deps.userHomeDir)
	// No PID is signalled: a stale supervisor record is an unavailable endpoint.
	if _, _, err := managedDaemonStatus(lock); err == nil {
		t.Fatal("stale supervisor reported stopped")
	}
	if err := stopManagedDaemon(lock, time.Second); err == nil {
		t.Fatal("stale supervisor stop reported success")
	}
}

func TestRestartValidatesArgs(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	if err := run([]string{"restart", "extra"}, os.Getenv, deps); err == nil {
		t.Fatal("restart accepted trailing arguments")
	}
}

// This helper runs only in a disposable subprocess, with a private HOME.
func TestSupervisorCLIHelper(t *testing.T) {
	if os.Getenv("DEARMACHINE_CLI_TEST_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	args = args[1:]
	if len(args) > 0 && args[0] == "fixture-daemon" {
		root := args[1]
		release, err := client.CreateDaemonLock(filepath.Join(root, "run", "dearmachine.pid"))
		if err != nil {
			os.Exit(3)
		}
		ready := filepath.Join(root, "run", "dearmachine.ready")
		if err := os.WriteFile(ready, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(4)
		}
		fmt.Println("fixture ready")
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
		<-signals
		os.Remove(ready)
		release()
		os.Exit(0)
	}
	if err := run(args, os.Getenv, defaultDependencies()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestCreateRequiresSupervisorStopped(t *testing.T) {
	deps, root := supervisedDeps(t)
	if err := requireDaemonStopped(deps.userHomeDir); err == nil {
		t.Fatal("pair mutation allowed while supervisor owns a running child")
	}
	if _, err := supervisor.Request(root, "down", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := requireDaemonStopped(deps.userHomeDir); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorDoesNotHideAnotherDaemonOwner(t *testing.T) {
	deps, root := supervisedDeps(t)
	if _, err := supervisor.Request(root, "down", time.Second); err != nil {
		t.Fatal(err)
	}
	lock, _ := client.DefaultDaemonLockPath(deps.userHomeDir)
	// The test process is only probed, never signalled through this record.
	if err := os.WriteFile(lock, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := managedDaemonStatus(lock); err == nil {
		t.Fatal("another daemon owner was hidden")
	}
	if err := stopManagedDaemon(lock, time.Second); err == nil {
		t.Fatal("down claimed another owner's daemon was stopped")
	}
}

func TestHeadlessBootstrap(t *testing.T) {
	for _, installed := range []bool{false, true} {
		t.Run(strconv.FormatBool(installed), func(t *testing.T) {
			deps := testDependencies(t, &fakeApplication{})
			if installed {
				makeUpTestPair(t, deps, "bootstrap")
			}
			calls := 0
			deps.startBackground = func(args []string, log string) (int, error) {
				calls++
				if strings.Join(args, " ") != "up --foreground" {
					t.Fatalf("args: %v", args)
				}
				return 123, nil
			}
			err := run([]string{"up", "--bootstrap"}, func(string) string { return "" }, deps)
			if installed && (err != nil || calls != 1) {
				t.Fatalf("bootstrap: %v, calls %d", err, calls)
			}
			if !installed && (err == nil || calls != 0) {
				t.Fatalf("absent: %v, calls %d", err, calls)
			}
		})
	}
}

func TestBootstrapRejectsMismatchedEndpoint(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	makeUpTestPair(t, deps, "bootstrap")
	deps.startBackground = func([]string, string) (int, error) { t.Fatal("started wrong owner"); return 0, nil }
	err := run([]string{"up", "--bootstrap"}, func(key string) string {
		if key == "DEARMACHINE_SUPERVISOR_SOCKET" {
			return filepath.Join(t.TempDir(), "other.sock")
		}
		return ""
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "socket") {
		t.Fatalf("error: %v", err)
	}
}

// A bootstrap attempt must not leave a second resident owner beside a daemon
// started by a foreground session or an external service.
func TestBootstrapPreservesForegroundOwner(t *testing.T) {
	home := socketTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("DEARMACHINE_CLI_TEST_HELPER", "1")
	root := filepath.Join(home, ".dearmachine")
	release, err := client.CreateDaemonLock(filepath.Join(root, "run", "dearmachine.pid"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(home, "cli")
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(binary, "'", "'\\''") + "' -test.run=^TestSupervisorCLIHelper$ -- \"$@\"\n"
	if err := os.WriteFile(launcher, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = supervisor.Request(root, "shutdown", time.Second) }()
	if _, err := startSupervised(launcher, []string{"fixture-daemon", root}, root); err == nil {
		t.Fatal("bootstrap accepted a foreground owner")
	}
	if err := supervisor.CheckAvailable(root); err != nil {
		t.Fatalf("failed bootstrap left a resident supervisor: %v", err)
	}
	if _, err := os.Lstat(supervisor.SocketPath(root)); !os.IsNotExist(err) {
		t.Fatalf("failed bootstrap left a control endpoint: %v", err)
	}
	pid, running, err := client.DaemonStatus(filepath.Join(root, "run", "dearmachine.pid"))
	if err != nil || !running || pid != os.Getpid() {
		t.Fatalf("foreground owner changed: %d %v %v", pid, running, err)
	}
}
