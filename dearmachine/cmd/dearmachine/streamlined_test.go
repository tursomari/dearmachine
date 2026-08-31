package main

import (
	"errors"
	"flag"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dearmachine/dearmachine/internal/client"
)

func TestLegacyDirectStartupIsNotACommand(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	err := run(
		[]string{
			"--inbox-id", "legacy-inbox",
			"--db", filepath.Join(t.TempDir(), "legacy.db"),
			"--allow", "legacy@example.test",
			"--once",
		},
		func(string) string { return "unused" },
		deps,
	)
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("legacy direct invocation error = %v", err)
	}
}

func TestUpBackgroundsByDefaultAndWaitsForReadiness(t *testing.T) {
	app := &fakeApplication{}
	deps := testDependencies(t, app)
	pair := makeUpTestPair(t, deps, "background")
	var gotArgs []string
	var gotLog string
	deps.startBackground = func(args []string, logPath string) (int, error) {
		gotArgs = slices.Clone(args)
		gotLog = logPath
		return 4321, nil
	}
	ready := false
	deps.waitDaemonReady = func(lockPath string, pid int, timeout time.Duration) error {
		ready = true
		if pid != 4321 || !strings.HasSuffix(lockPath, ".dearmachine/run/dearmachine.pid") || timeout <= 0 {
			t.Fatalf("readiness = %q/%d/%s", lockPath, pid, timeout)
		}
		return nil
	}
	var output strings.Builder
	deps.stdout = &output
	if err := run([]string{"up", "--pair", pair.UserEmail, "--magnifica-humanitas"}, func(string) string { return "" }, deps); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotArgs, []string{"up", "--foreground", "--pair", pair.UserEmail, "--magnifica-humanitas"}) {
		t.Fatalf("background args = %v", gotArgs)
	}
	if !strings.HasSuffix(gotLog, ".dearmachine/log/dearmachine.log") || !ready || app.runCount != 0 {
		t.Fatalf("background log/ready/direct runs = %q/%v/%d", gotLog, ready, app.runCount)
	}
	if !strings.Contains(output.String(), "PID 4321") {
		t.Fatalf("up output = %q", output.String())
	}
}

func TestUpForegroundRunsAttached(t *testing.T) {
	app := &fakeApplication{}
	deps := testDependencies(t, app)
	makeUpTestPair(t, deps, "foreground")
	deps.startBackground = func([]string, string) (int, error) {
		t.Fatal("foreground attempted to spawn")
		return 0, nil
	}
	if err := run([]string{"up", "--foreground"}, func(string) string { return "" }, deps); err != nil {
		t.Fatal(err)
	}
	if app.runCount != 1 {
		t.Fatalf("foreground runs = %d", app.runCount)
	}
}

func TestStatusAndDownUseCanonicalDaemonState(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	makeUpTestPair(t, deps, "lifecycle")
	deps.daemonStatus = func(path string) (int, bool, error) {
		if !strings.HasSuffix(path, ".dearmachine/run/dearmachine.pid") {
			t.Fatalf("status path = %q", path)
		}
		return 6789, true, nil
	}
	stopped := false
	deps.stopDaemon = func(path string, timeout time.Duration) error {
		stopped = true
		return nil
	}
	var output strings.Builder
	deps.stdout = &output
	if err := run([]string{"status"}, func(string) string { return "" }, deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "running (PID 6789)") || !strings.Contains(output.String(), "lifecycle@example.test") {
		t.Fatalf("status output = %q", output.String())
	}
	output.Reset()
	if err := run([]string{"down"}, func(string) string { return "" }, deps); err != nil {
		t.Fatal(err)
	}
	if !stopped || !strings.Contains(output.String(), "Stopped DearMachine") {
		t.Fatalf("down = %v/%q", stopped, output.String())
	}
	deps.stopDaemon = func(string, time.Duration) error { return client.ErrDaemonStopped }
	output.Reset()
	if err := run([]string{"down"}, func(string) string { return "" }, deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "already stopped") {
		t.Fatalf("idempotent down output = %q", output.String())
	}
}

func TestRemovedListPointsToStatus(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	err := run([]string{"up", "--list"}, func(string) string { return "" }, deps)
	if err == nil || !strings.Contains(err.Error(), "dearmachine status") {
		t.Fatalf("up --list error = %v", err)
	}
	if !errors.Is(run([]string{"status", "--help"}, func(string) string { return "" }, deps), flag.ErrHelp) {
		t.Fatal("status --help did not return flag.ErrHelp")
	}
}

func TestRunConfigurationRejectsLegacyDirectFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--inbox-id", "legacy-inbox"},
		{"--db", "legacy.db"},
		{"--allow", "legacy@example.test"},
		{"--pidfile", "legacy.pid"},
		{"--transport", "agentmail"},
	} {
		if _, err := parseConfig(args, io.Discard); err == nil {
			t.Fatalf("parseConfig(%v) accepted a legacy flag", args)
		}
	}
}
