package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dearmachine/dearmachine/internal/client"
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
	if defaults.transport != "agentmail" || defaults.dbPath != "" || defaults.projectDir != "." ||
		defaults.agentBinary != "machtiani" || defaults.concurrency != 3 ||
		defaults.maintenanceMinTurns != 20 ||
		defaults.pollInterval != time.Minute ||
		defaults.entryPointRepo != "~/.dearmachine/entrypoint/main" ||
		defaults.entryPointPrompt != "~/.dearmachine/entrypoint/main/documentation/update-prompt-template.md" {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}
	if defaults.inboxID != "" || defaults.model != "" || defaults.pidfile != "" ||
		defaults.once || defaults.verbose || defaults.magnificaHumanitas {
		t.Fatalf("unexpected optional defaults: %+v", defaults)
	}

	args := []string{
		"--inbox-id", "inbox-123",
		"--transport", "openmail",
		"--db", "/tmp/state.db",
		"--project", "/tmp/project",
		"--model", "fast-model",
		"--agent-bin", "/tmp/machtiani",
		"--entry-point-repo", "/tmp/entrypoint",
		"--entry-point-prompt", "/tmp/entrypoint/documentation/update.md",
		"--concurrency", "5",
		"--maintenance-min-turns", "8",
		"--poll-interval", "250ms",
		"--pidfile", "/tmp/dearmachine.pid",
		"--magnifica-humanitas",
		"--once",
		"--verbose",
	}
	cfg, err := parseConfig(args, io.Discard)
	if err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if cfg.inboxID != "inbox-123" || cfg.transport != "openmail" || cfg.dbPath != "/tmp/state.db" ||
		cfg.projectDir != "/tmp/project" || cfg.model != "fast-model" ||
		cfg.agentBinary != "/tmp/machtiani" || cfg.entryPointRepo != "/tmp/entrypoint" ||
		cfg.entryPointPrompt != "/tmp/entrypoint/documentation/update.md" ||
		cfg.concurrency != 5 || cfg.maintenanceMinTurns != 8 ||
		cfg.pollInterval != 250*time.Millisecond ||
		cfg.pidfile != "/tmp/dearmachine.pid" || !cfg.magnificaHumanitas || !cfg.once || !cfg.verbose {
		t.Fatalf("unexpected parsed config: %+v", cfg)
	}
}

func TestParseConfigRejectsMagnificaHumanitasAliases(t *testing.T) {
	for _, alias := range []string{"-magnifica-humanitas", "--magnifica_humanitas"} {
		t.Run(alias, func(t *testing.T) {
			if _, err := parseConfig([]string{alias}, io.Discard); err == nil {
				t.Fatalf("parseConfig(%q) succeeded", alias)
			}
		})
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
	deps.openStore = func(path string) (*client.Store, error) {
		opened = path
		return client.OpenStore(filepath.Join(t.TempDir(), "test.db"))
	}
	if err := run(
		[]string{"--inbox-id", "inbox-123", "--once"},
		func(string) string { return "test-key" },
		deps,
	); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := filepath.Join(home, ".dearmachine", "state", "dearmachine.db")
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
		{name: "zero concurrency", args: []string{"--concurrency", "0"}},
		{name: "negative concurrency", args: []string{"--concurrency", "-1"}},
		{name: "negative maintenance turns", args: []string{"--maintenance-min-turns", "-1"}},
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

func TestParseConfigReportsInvalidConcurrency(t *testing.T) {
	_, err := parseConfig([]string{"--concurrency", "0"}, io.Discard)
	if err == nil || err.Error() != "--concurrency must be at least 1" {
		t.Fatalf("parseConfig error = %v", err)
	}
}

func TestParseConfigReportsInvalidMaintenanceMinTurns(t *testing.T) {
	_, err := parseConfig([]string{"--maintenance-min-turns", "-1"}, io.Discard)
	if err == nil || err.Error() != "--maintenance-min-turns must not be negative" {
		t.Fatalf("parseConfig error = %v", err)
	}
}

func TestParseConfigHelp(t *testing.T) {
	var output strings.Builder
	_, err := parseConfig([]string{"--help"}, &output)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseConfig help error = %v", err)
	}
	for _, want := range []string{"Usage of dearmachine:", "-agent-bin", "-concurrency", "-inbox-id", "-maintenance-min-turns", "-once", "-transport"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("help output missing %q:\n%s", want, output.String())
		}
	}
}

func TestRunUsesSelectedTransportConstructor(t *testing.T) {
	app := &fakeApplication{}
	deps := testDependencies(t, app)
	constructed := false
	deps.newTransport = func(inboxID string) (client.Transport, error) {
		constructed = true
		return client.NewAgentMailTransport(inboxID)
	}
	if err := run(
		[]string{"--transport", "openmail", "--inbox-id", "inb-test", "--once"},
		func(string) string { return "" },
		deps,
	); err != nil {
		t.Fatalf("run openmail: %v", err)
	}
	if !constructed || app.runOnceCount != 1 {
		t.Fatalf("OpenMail selection construction/run = %v/%d", constructed, app.runOnceCount)
	}
}

func TestRunValidatesRequiredConfigurationBeforeConstruction(t *testing.T) {
	t.Setenv("AGENTMAIL_API_KEY", "")
	t.Setenv("AGENTMAIL_API_KEY_FILE", "")
	called := false
	deps := dependencies{
		openStore: func(string) (*client.Store, error) {
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
			want:   "AGENTMAIL_API_KEY or AGENTMAIL_API_KEY_FILE is required",
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
	t.Setenv("AGENTMAIL_API_KEY", "offline-test-key")
	t.Setenv("AGENTMAIL_API_KEY_FILE", "")
	for _, once := range []bool{false, true} {
		t.Run(map[bool]string{false: "continuous", true: "once"}[once], func(t *testing.T) {
			app := &fakeApplication{}
			var (
				store        *client.Store
				storePath    string
				inboxID      string
				binary       string
				projectDir   string
				model        string
				concurrency  int
				pollInterval time.Duration
				verbose      bool
				pidfile      string
				responseTier client.ResponseTier
				signals      []os.Signal
				stopped      bool
				orchestrator *synctrigger.Orchestrator
			)
			ctx := context.WithValue(context.Background(), "test-key", "signal-context")
			deps := dependencies{
				openStore: func(path string) (*client.Store, error) {
					storePath = path
					var err error
					store, err = client.OpenStore(filepath.Join(t.TempDir(), "state.db"))
					return store, err
				},
				newTransport: func(got string) (client.Transport, error) {
					inboxID = got
					return client.NewAgentMailTransport(got)
				},
				newRunner: func(gotBinary, gotProject, gotModel string) (*client.AgentRunner, error) {
					binary, projectDir, model = gotBinary, gotProject, gotModel
					return client.NewAgentRunner(gotBinary, gotProject, gotModel)
				},
				newApp: func(
					_ client.Transport,
					_ *client.Store,
					_ *client.AgentRunner,
					gotOrchestrator *synctrigger.Orchestrator,
					gotConcurrency int,
					gotInterval time.Duration,
					_ *log.Logger,
					gotVerbose bool,
					gotPIDFile string,
					gotResponseTier client.ResponseTier,
				) (application, error) {
					orchestrator = gotOrchestrator
					concurrency = gotConcurrency
					pollInterval, verbose, pidfile, responseTier = gotInterval, gotVerbose, gotPIDFile, gotResponseTier
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
				"--agent-bin", "/bin/machtiani",
				"--concurrency", "7",
				"--poll-interval", "3s",
				"--pidfile", "/run/dearmachine.pid",
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
				binary != "/bin/machtiani" || projectDir != "/project" || model != "model-123" ||
				concurrency != 7 || pollInterval != 3*time.Second || !verbose ||
				pidfile != "/run/dearmachine.pid" || responseTier != client.TierPlain {
				t.Fatalf("unexpected construction: db=%q inbox=%q runner=%q,%q,%q app=%d,%s,%v,%q,%q",
					storePath, inboxID, binary, projectDir, model, concurrency,
					pollInterval, verbose, pidfile, responseTier)
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
				deps.openStore = func(string) (*client.Store, error) { return nil, want }
			case "mailbox":
				deps.newTransport = func(string) (client.Transport, error) {
					return nil, want
				}
			case "runner":
				deps.newRunner = func(string, string, string) (*client.AgentRunner, error) {
					return nil, want
				}
			case "application":
				deps.newApp = func(
					client.Transport,
					*client.Store,
					*client.AgentRunner,
					*synctrigger.Orchestrator,
					int,
					time.Duration,
					*log.Logger,
					bool,
					string,
					client.ResponseTier,
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
		[]string{"init", "--entry-point-repo", repo, "--agent-bin", "/missing/machtiani"},
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
			agentBinary:      "/configured/machtiani",
		},
		dependencies{userHomeDir: func() (string, error) { return t.TempDir(), nil }},
		log.New(io.Discard, "", 0),
		backends,
		manager,
		nil,
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

func TestLoadAgentManagedConfigFindsPackagedManagerOnPath(t *testing.T) {
	deps := dependencies{}
	configureAgentTestDeps(t, &deps)
	backends, manager, _, tier, err := loadAgentManagedConfig(config{}, deps)
	if err != nil {
		t.Fatalf("loadAgentManagedConfig: %v", err)
	}
	if manager != "/test/bin/agent-manager" {
		t.Fatalf("manager = %q", manager)
	}
	if !slices.Equal(backends, []string{"codex"}) {
		t.Fatalf("backends = %v", backends)
	}
	if tier != client.TierPlain {
		t.Fatalf("response tier = %q, want %q", tier, client.TierPlain)
	}
}

func TestRunWiresConfiguredResponseTierToApp(t *testing.T) {
	t.Setenv("AGENTMAIL_API_KEY", "offline-test-key")
	t.Setenv("AGENTMAIL_API_KEY_FILE", "")
	tests := []struct {
		name        string
		writeConfig func(t *testing.T, path string)
		want        client.ResponseTier
	}{
		{
			name: "formatted tier from config",
			writeConfig: func(t *testing.T, path string) {
				t.Helper()
				if err := client.SaveDeviceConfig(path, client.DeviceConfig{
					Version:      client.DeviceConfigVersion,
					Backends:     []string{"codex"},
					ResponseTier: client.TierFormatted,
				}); err != nil {
					t.Fatalf("save device config: %v", err)
				}
			},
			want: client.TierFormatted,
		},
		{
			name: "missing tier key defaults to plain",
			writeConfig: func(t *testing.T, path string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				content := fmt.Sprintf(
					"version = %d\nbackends = [\"codex\"]\n",
					client.DeviceConfigVersion,
				)
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: client.TierPlain,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var gotTier client.ResponseTier
			app := &fakeApplication{}
			deps := dependencies{
				openStore: func(string) (*client.Store, error) {
					return client.OpenStore(filepath.Join(t.TempDir(), "state.db"))
				},
				newTransport: func(inboxID string) (client.Transport, error) {
					return client.NewAgentMailTransport(inboxID)
				},
				newRunner: client.NewAgentRunner,
				newApp: func(
					_ client.Transport,
					_ *client.Store,
					_ *client.AgentRunner,
					_ *synctrigger.Orchestrator,
					_ int,
					_ time.Duration,
					_ *log.Logger,
					_ bool,
					_ string,
					tier client.ResponseTier,
				) (application, error) {
					gotTier = tier
					return app, nil
				},
				newLogger: func() *log.Logger { return log.New(io.Discard, "", 0) },
				notifyContext: func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
					return context.WithCancel(parent)
				},
				flagOutput: io.Discard,
			}
			home := t.TempDir()
			configPath := filepath.Join(home, ".dearmachine", "config", "dearmachine.toml")
			test.writeConfig(t, configPath)
			deps.userHomeDir = func() (string, error) { return home, nil }
			deps.lookPath = func(executable string) (string, error) {
				switch executable {
				case "agent-manager":
					return "/test/bin/agent-manager", nil
				case "codex":
					return "/test/bin/codex", nil
				}
				return "", os.ErrNotExist
			}
			if err := run(
				[]string{"--inbox-id", "inbox-123", "--once"},
				func(string) string { return "test-key" },
				deps,
			); err != nil {
				t.Fatalf("run: %v", err)
			}
			if gotTier != test.want {
				t.Fatalf("newApp response tier = %q, want %q", gotTier, test.want)
			}
		})
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
	path := filepath.Join(home, ".dearmachine", "config", "dearmachine.toml")
	config, err := client.LoadDeviceConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !slices.Equal(config.Backends, []string{"codex"}) {
		t.Fatalf("backends = %v", config.Backends)
	}
}

func testDependencies(t *testing.T, app application) dependencies {
	t.Helper()
	t.Setenv("AGENTMAIL_API_KEY", "offline-test-key")
	t.Setenv("AGENTMAIL_API_KEY_FILE", "")
	deps := dependencies{
		openStore: func(string) (*client.Store, error) {
			return client.OpenStore(filepath.Join(t.TempDir(), "state.db"))
		},
		newTransport: func(inboxID string) (client.Transport, error) {
			return client.NewAgentMailTransport(inboxID)
		},
		newRunner: client.NewAgentRunner,
		newApp: func(
			client.Transport,
			*client.Store,
			*client.AgentRunner,
			*synctrigger.Orchestrator,
			int,
			time.Duration,
			*log.Logger,
			bool,
			string,
			client.ResponseTier,
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
	path := filepath.Join(home, ".dearmachine", "config", "dearmachine.toml")
	if err := client.SaveDeviceConfig(path, client.DeviceConfig{
		Version:  client.DeviceConfigVersion,
		Backends: []string{"codex"},
	}); err != nil {
		t.Fatalf("save device config: %v", err)
	}
	deps.userHomeDir = func() (string, error) { return home, nil }
	deps.lookPath = func(executable string) (string, error) {
		switch executable {
		case "agent-manager":
			return "/test/bin/agent-manager", nil
		case "codex":
			return "/test/bin/codex", nil
		}
		return "", os.ErrNotExist
	}
}
