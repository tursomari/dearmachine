package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dearmachine/dearmachine/internal/deviceclient"
	"github.com/dearmachine/dearmachine/internal/synctrigger"
)

type fakeApplication struct {
	runCount     int
	runOnceCount int
	ctx          context.Context
	err          error
}

func (a *fakeApplication) Run(ctx context.Context) error {
	a.runCount++
	a.ctx = ctx
	return a.err
}

func (a *fakeApplication) RunOnce(ctx context.Context) error {
	a.runOnceCount++
	a.ctx = ctx
	return a.err
}

func TestParseConfigDefaultsAndFlags(t *testing.T) {
	defaults, err := parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatalf("parse defaults: %v", err)
	}
	if defaults.dbPath != "" || defaults.projectDir != "." ||
		defaults.mctBinary != "mct-agent" || defaults.pollInterval != time.Minute ||
		defaults.entryPointRepo != "~/.dearmachine/entrypoint/main" ||
		defaults.entryPointPrompt != "~/.dearmachine/entrypoint/main/documentation/update-prompt-template.md" {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}
	if defaults.inboxID != "" || defaults.model != "" || defaults.pidfile != "" ||
		defaults.once || defaults.verbose {
		t.Fatalf("unexpected optional defaults: %+v", defaults)
	}

	args := []string{
		"--inbox-id", "inbox-123",
		"--db", "/tmp/state.db",
		"--project", "/tmp/project",
		"--model", "fast-model",
		"--mct-agent", "/tmp/mct-agent",
		"--entry-point-repo", "/tmp/entrypoint",
		"--entry-point-prompt", "/tmp/entrypoint/documentation/update.md",
		"--poll-interval", "250ms",
		"--pidfile", "/tmp/device-client.pid",
		"--once",
		"--verbose",
	}
	cfg, err := parseConfig(args, io.Discard)
	if err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if cfg.inboxID != "inbox-123" || cfg.dbPath != "/tmp/state.db" ||
		cfg.projectDir != "/tmp/project" || cfg.model != "fast-model" ||
		cfg.mctBinary != "/tmp/mct-agent" || cfg.entryPointRepo != "/tmp/entrypoint" ||
		cfg.entryPointPrompt != "/tmp/entrypoint/documentation/update.md" ||
		cfg.pollInterval != 250*time.Millisecond ||
		cfg.pidfile != "/tmp/device-client.pid" || !cfg.once || !cfg.verbose {
		t.Fatalf("unexpected parsed config: %+v", cfg)
	}
}

func TestRunUsesPrivateDefaultDatabasePath(t *testing.T) {
	app := &fakeApplication{}
	deps := testDependencies(t, app)
	home, err := deps.userHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	var opened string
	deps.openStore = func(path string) (*deviceclient.Store, error) {
		opened = path
		return deviceclient.OpenStore(filepath.Join(t.TempDir(), "test.db"))
	}
	if err := run(
		[]string{"--inbox-id", "inbox-123", "--once"},
		func(string) string { return "test-key" },
		deps,
	); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := filepath.Join(home, ".dearmachine", "state", "device-client.db")
	if opened != want {
		t.Fatalf("opened database = %q, want %q", opened, want)
	}
}

func TestParseConfigRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "unknown flag", args: []string{"--unknown"}},
		{name: "invalid duration", args: []string{"--poll-interval", "later"}},
		{name: "positional argument", args: []string{"unexpected"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseConfig(test.args, io.Discard); err == nil {
				t.Fatalf("parseConfig(%v) succeeded", test.args)
			}
		})
	}
}

func TestParseConfigHelp(t *testing.T) {
	var output strings.Builder
	_, err := parseConfig([]string{"--help"}, &output)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseConfig help error = %v", err)
	}
	for _, want := range []string{"Usage of device-client:", "-inbox-id", "-once"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("help output missing %q:\n%s", want, output.String())
		}
	}
}

func TestRunValidatesRequiredConfigurationBeforeConstruction(t *testing.T) {
	called := false
	deps := dependencies{
		openStore: func(string) (*deviceclient.Store, error) {
			called = true
			return nil, errors.New("unexpected construction")
		},
	}
	tests := []struct {
		name   string
		args   []string
		getenv func(string) string
		want   string
	}{
		{
			name:   "missing inbox",
			getenv: func(string) string { return "test-key" },
			want:   "--inbox-id is required",
		},
		{
			name:   "missing API key",
			args:   []string{"--inbox-id", "inbox-123"},
			getenv: func(string) string { return "" },
			want:   "AGENTMAIL_API_KEY is required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called = false
			err := run(test.args, test.getenv, deps)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run error = %v, want %q", err, test.want)
			}
			if called {
				t.Fatal("dependency construction started before validation")
			}
		})
	}
}

func TestRunConstructsDependenciesWiresSignalsAndDispatches(t *testing.T) {
	for _, once := range []bool{false, true} {
		t.Run(map[bool]string{false: "continuous", true: "once"}[once], func(t *testing.T) {
			app := &fakeApplication{}
			var (
				store        *deviceclient.Store
				storePath    string
				inboxID      string
				binary       string
				projectDir   string
				model        string
				pollInterval time.Duration
				verbose      bool
				pidfile      string
				signals      []os.Signal
				stopped      bool
				orchestrator *synctrigger.Orchestrator
			)
			ctx := context.WithValue(context.Background(), "test-key", "signal-context")
			deps := dependencies{
				openStore: func(path string) (*deviceclient.Store, error) {
					storePath = path
					var err error
					store, err = deviceclient.OpenStore(filepath.Join(t.TempDir(), "state.db"))
					return store, err
				},
				newTransport: func(got string) (deviceclient.Transport, error) {
					inboxID = got
					return deviceclient.NewAgentMailTransport(got)
				},
				newRunner: func(gotBinary, gotProject, gotModel string) (*deviceclient.MCTRunner, error) {
					binary, projectDir, model = gotBinary, gotProject, gotModel
					return deviceclient.NewMCTRunner(gotBinary, gotProject, gotModel)
				},
				newApp: func(
					_ deviceclient.Transport,
					_ *deviceclient.Store,
					_ *deviceclient.MCTRunner,
					gotOrchestrator *synctrigger.Orchestrator,
					gotInterval time.Duration,
					_ *log.Logger,
					gotVerbose bool,
					gotPIDFile string,
				) (application, error) {
					orchestrator = gotOrchestrator
					pollInterval, verbose, pidfile = gotInterval, gotVerbose, gotPIDFile
					return app, nil
				},
				newLogger: func() *log.Logger { return log.New(io.Discard, "", 0) },
				notifyContext: func(_ context.Context, got ...os.Signal) (context.Context, context.CancelFunc) {
					signals = slices.Clone(got)
					return ctx, func() { stopped = true }
				},
				flagOutput: io.Discard,
			}
			configureAgentTestDeps(t, &deps)
			args := []string{
				"--inbox-id", "inbox-123",
				"--db", "configured.db",
				"--project", "/project",
				"--model", "model-123",
				"--mct-agent", "/bin/mct-agent",
				"--poll-interval", "3s",
				"--pidfile", "/run/device-client.pid",
				"--verbose",
			}
			if once {
				args = append(args, "--once")
			}
			if err := run(args, func(key string) string {
				if key == "AGENTMAIL_API_KEY" {
					return "test-key"
				}
				return ""
			}, deps); err != nil {
				t.Fatalf("run: %v", err)
			}
			if storePath != "configured.db" || inboxID != "inbox-123" ||
				binary != "/bin/mct-agent" || projectDir != "/project" || model != "model-123" ||
				pollInterval != 3*time.Second || !verbose || pidfile != "/run/device-client.pid" {
				t.Fatalf("unexpected construction: db=%q inbox=%q runner=%q,%q,%q app=%s,%v,%q",
					storePath, inboxID, binary, projectDir, model, pollInterval, verbose, pidfile)
			}
			if orchestrator != nil {
				t.Fatal("expected orchestrator to be nil when entry-point repo is missing")
			}
			if len(signals) != 2 || signals[0] != os.Interrupt || signals[1] != syscall.SIGTERM {
				t.Fatalf("wired signals = %v", signals)
			}
			if !stopped || app.ctx != ctx {
				t.Fatalf("context wiring: stopped=%v context=%v", stopped, app.ctx)
			}
			if app.runCount != map[bool]int{false: 1, true: 0}[once] ||
				app.runOnceCount != map[bool]int{false: 0, true: 1}[once] {
				t.Fatalf("dispatch counts: Run=%d RunOnce=%d", app.runCount, app.runOnceCount)
			}
			if _, err := store.Seen("after-close"); err == nil {
				t.Fatal("store remained open after run")
			}
		})
	}
}

func TestRunReportsDependencyConstructionFailures(t *testing.T) {
	tests := []struct {
		name string
		fail string
	}{
		{name: "store", fail: "store"},
		{name: "mailbox", fail: "mailbox"},
		{name: "runner", fail: "runner"},
		{name: "application", fail: "application"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := errors.New(test.fail + " construction failed")
			deps := testDependencies(t, &fakeApplication{})
			switch test.fail {
			case "store":
				deps.openStore = func(string) (*deviceclient.Store, error) { return nil, want }
			case "mailbox":
				deps.newTransport = func(string) (deviceclient.Transport, error) {
					return nil, want
				}
			case "runner":
				deps.newRunner = func(string, string, string) (*deviceclient.MCTRunner, error) {
					return nil, want
				}
			case "application":
				deps.newApp = func(
					deviceclient.Transport,
					*deviceclient.Store,
					*deviceclient.MCTRunner,
					*synctrigger.Orchestrator,
					time.Duration,
					*log.Logger,
					bool,
					string,
				) (application, error) {
					return nil, want
				}
			}
			err := run(
				[]string{"--inbox-id", "inbox-123"},
				func(string) string { return "test-key" },
				deps,
			)
			if !errors.Is(err, want) {
				t.Fatalf("run error = %v, want %v", err, want)
			}
		})
	}
}

func TestRunReturnsSelectedCommandError(t *testing.T) {
	want := errors.New("application stopped")
	app := &fakeApplication{err: want}
	deps := testDependencies(t, app)
	err := run(
		[]string{"--inbox-id", "inbox-123", "--once"},
		func(string) string { return "test-key" },
		deps,
	)
	if !errors.Is(err, want) || app.runOnceCount != 1 || app.runCount != 0 {
		t.Fatalf("run error/counts = %v/%d/%d", err, app.runCount, app.runOnceCount)
	}
}

func TestRunInitDoesNotRequireMailConfigurationAndLeavesExistingRepoUntouched(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(repo, "README.md")
	if err := os.WriteFile(readme, []byte("existing entry point\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	deps := dependencies{
		stdout:      &output,
		flagOutput:  io.Discard,
		userHomeDir: func() (string, error) { return t.TempDir(), nil },
	}
	if err := run(
		[]string{"init", "--entry-point-repo", repo, "--mct-agent", "/missing/mct-agent"},
		func(string) string { return "" },
		deps,
	); err != nil {
		t.Fatalf("run init: %v", err)
	}
	if !strings.Contains(output.String(), "already initialized; left unchanged") {
		t.Fatalf("init output = %q", output.String())
	}
	data, err := os.ReadFile(readme)
	if err != nil || string(data) != "existing entry point\n" {
		t.Fatalf("existing README changed: %q, %v", data, err)
	}
}

func TestRunInitRejectsUnexpectedArguments(t *testing.T) {
	err := runInit([]string{"unexpected"}, dependencies{flagOutput: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "unexpected init arguments") {
		t.Fatalf("runInit error = %v", err)
	}
}

func TestBuildOrchestratorReceivesAgentManagedConfiguration(t *testing.T) {
	repo := t.TempDir()
	prompt := filepath.Join(repo, "documentation", "update-prompt.md")
	manager := "/configured/agent-manager"
	backends := []string{"forge", "codex"}
	orchestrator, err := buildOrchestrator(
		config{
			entryPointRepo:   repo,
			entryPointPrompt: prompt,
			mctBinary:        "/configured/mct-agent",
		},
		dependencies{userHomeDir: func() (string, error) { return t.TempDir(), nil }},
		log.New(io.Discard, "", 0),
		backends,
		manager,
	)
	if err != nil {
		t.Fatalf("buildOrchestrator: %v", err)
	}
	if orchestrator == nil {
		t.Fatal("buildOrchestrator returned nil")
	}
	if orchestrator.AgentManagerPath != manager || !slices.Equal(orchestrator.Backends, backends) {
		t.Fatalf(
			"agent-managed configuration = %q, %v; want %q, %v",
			orchestrator.AgentManagerPath,
			orchestrator.Backends,
			manager,
			backends,
		)
	}
	backends[0] = "codex"
	if !slices.Equal(orchestrator.Backends, []string{"forge", "codex"}) {
		t.Fatalf("orchestrator backends alias caller slice: %v", orchestrator.Backends)
	}
}

func TestSetupAgentsUsesDearMachineConfigPath(t *testing.T) {
	home := t.TempDir()
	var output strings.Builder
	deps := dependencies{
		stdin:  strings.NewReader("\n"),
		stdout: &output,
		lookPath: func(executable string) (string, error) {
			if executable == "codex" {
				return "/test/bin/codex", nil
			}
			return "", os.ErrNotExist
		},
		userHomeDir: func() (string, error) { return home, nil },
		flagOutput:  io.Discard,
	}
	if err := run([]string{"setup-agents"}, func(string) string { return "" }, deps); err != nil {
		t.Fatalf("setup-agents: %v", err)
	}
	path := filepath.Join(home, ".dearmachine", "config", "device-client.toml")
	config, err := deviceclient.LoadDeviceConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !slices.Equal(config.Backends, []string{"codex"}) {
		t.Fatalf("backends = %v", config.Backends)
	}
}

func testDependencies(t *testing.T, app application) dependencies {
	t.Helper()
	deps := dependencies{
		openStore: func(string) (*deviceclient.Store, error) {
			return deviceclient.OpenStore(filepath.Join(t.TempDir(), "state.db"))
		},
		newTransport: func(inboxID string) (deviceclient.Transport, error) {
			return deviceclient.NewAgentMailTransport(inboxID)
		},
		newRunner: deviceclient.NewMCTRunner,
		newApp: func(
			deviceclient.Transport,
			*deviceclient.Store,
			*deviceclient.MCTRunner,
			*synctrigger.Orchestrator,
			time.Duration,
			*log.Logger,
			bool,
			string,
		) (application, error) {
			return app, nil
		},
		newLogger: func() *log.Logger { return log.New(io.Discard, "", 0) },
		notifyContext: func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
			return context.WithCancel(parent)
		},
		flagOutput: io.Discard,
	}
	configureAgentTestDeps(t, &deps)
	return deps
}

func configureAgentTestDeps(t *testing.T, deps *dependencies) {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, ".dearmachine", "config", "device-client.toml")
	if err := deviceclient.SaveDeviceConfig(path, deviceclient.DeviceConfig{
		Version:  deviceclient.DeviceConfigVersion,
		Backends: []string{"codex"},
	}); err != nil {
		t.Fatalf("save device config: %v", err)
	}
	deps.userHomeDir = func() (string, error) { return home, nil }
	deps.lookPath = func(executable string) (string, error) {
		if executable == "codex" {
			return "/test/bin/codex", nil
		}
		return "", os.ErrNotExist
	}
}
