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
	if defaults.projectDir != "." || defaults.agentBinary != "machtiani" || defaults.concurrency != 3 ||
		defaults.maintenanceMinTurns != 20 ||
		defaults.pollInterval != time.Minute ||
		defaults.entryPointRepo != "~/.dearmachine/entrypoint/main" ||
		defaults.entryPointPrompt != "~/.dearmachine/entrypoint/main/documentation/update-prompt-template.md" {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}
	if defaults.model != "" ||
		defaults.once || defaults.verbose || defaults.magnificaHumanitas {
		t.Fatalf("unexpected optional defaults: %+v", defaults)
	}

	args := []string{
		"--project", "/tmp/project",
		"--model", "fast-model",
		"--agent-bin", "/tmp/machtiani",
		"--entry-point-repo", "/tmp/entrypoint",
		"--entry-point-prompt", "/tmp/entrypoint/documentation/update.md",
		"--concurrency", "5",
		"--maintenance-min-turns", "8",
		"--poll-interval", "250ms",
		"--magnifica-humanitas",
		"--once",
		"--verbose",
	}
	cfg, err := parseConfig(args, io.Discard)
	if err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if cfg.projectDir != "/tmp/project" || cfg.model != "fast-model" ||
		cfg.agentBinary != "/tmp/machtiani" || cfg.entryPointRepo != "/tmp/entrypoint" ||
		cfg.entryPointPrompt != "/tmp/entrypoint/documentation/update.md" ||
		cfg.concurrency != 5 || cfg.maintenanceMinTurns != 8 ||
		cfg.pollInterval != 250*time.Millisecond || !cfg.magnificaHumanitas || !cfg.once || !cfg.verbose {
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
	for _, want := range []string{"Usage of dearmachine:", "-agent-bin", "-concurrency", "-maintenance-min-turns", "-once"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("help output missing %q:\n%s", want, output.String())
		}
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
		newRawTransport: func(_, _ string) (client.Transport, error) {
			return &emptyTransport{}, nil
		},
		provisionInbox: func(_ context.Context, transport string) (client.Inbox, error) {
			return client.Inbox{Transport: transport, ProviderID: "provisioned-inbox", Address: "machine@example.test"}, nil
		},
		inspectInbox: func(_ context.Context, transport, selection string) (client.Inbox, error) {
			return client.Inbox{Transport: transport, ProviderID: selection, Address: "adopted@example.test"}, nil
		},
		authorizePair: func(context.Context, string, string, string) error { return nil },
		newRunner:     client.NewAgentRunner,
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
		newLogger:     func() *log.Logger { return log.New(io.Discard, "", 0) },
		newPairDaemon: newApplicationGroup,
		notifyContext: func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
			return context.WithCancel(parent)
		},
		flagOutput:    io.Discard,
		isInteractive: func(io.Reader) bool { return true },
	}
	configureAgentTestDeps(t, &deps)
	return deps
}

type emptyTransport struct{}

func (*emptyTransport) Poll(context.Context) ([]client.Message, error)           { return nil, nil }
func (*emptyTransport) Thread(context.Context, string) ([]client.Message, error) { return nil, nil }
func (*emptyTransport) Message(context.Context, string) (client.Message, error) {
	return client.Message{}, nil
}
func (*emptyTransport) Reply(context.Context, string, client.ReplyPayload, string) (string, error) {
	return "", nil
}
func (*emptyTransport) ReplyReceipt(context.Context, client.Message) (string, bool, error) {
	return "", false, nil
}
func (*emptyTransport) MarkProcessed(context.Context, string) error { return nil }
func (*emptyTransport) FetchAttachment(context.Context, string, int64) ([]byte, error) {
	return nil, nil
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
