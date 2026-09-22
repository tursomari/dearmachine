package synctrigger

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type commandCall struct {
	ctx  context.Context
	dir  string
	name string
	args []string
}

type mockRunner struct {
	entries []commandCall
	errAt   map[string]error
}

func (m *mockRunner) Run(
	ctx context.Context,
	dir string,
	name string,
	args ...string,
) ([]byte, error) {
	argsCopy := make([]string, len(args))
	copy(argsCopy, args)
	m.entries = append(m.entries, commandCall{ctx: ctx, dir: dir, name: name, args: argsCopy})

	commandID := strings.Join(append([]string{name}, argsCopy...), " ")
	if err, ok := m.errAt[commandID]; ok {
		return nil, err
	}

	if name == "machtiani" && len(argsCopy) >= 3 && argsCopy[0] == "session" && argsCopy[1] == "fork" {
		return []byte("forked-123\n"), nil
	}
	return []byte(""), nil
}

func TestOrchestrateNoForkNeeded(t *testing.T) {
	t.Parallel()
	var calls int
	o := &Orchestrator{
		RepoPath:           "/repo",
		AgentBinary:        "machtiani",
		PromptTemplatePath: "/prompt.md",
		Logger:             log.New(&bytes.Buffer{}, "", 0),
		Lister: func(context.Context, string) ([]SessionInfo, error) {
			return []SessionInfo{
				{
					SessionID: "new-session",
					UpdatedAt: time.Now().Add(2 * time.Minute),
				},
			}, nil
		},
		GitLastCommitTime: func(string) (time.Time, error) {
			return time.Now(), nil
		},
		RunCommand: func(context.Context, string, string, ...string) ([]byte, error) {
			calls++
			return []byte{}, nil
		},
	}
	if err := o.OrchestrateSync(context.Background()); err != nil {
		t.Fatalf("OrchestrateSync: %v", err)
	}
	if calls != 0 {
		t.Fatalf("command count = %d, want 0", calls)
	}
}

func TestOrchestrateSuccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(home, ".config/dearmachine/machtiani/config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("default_model = \"sync-selected\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &mockRunner{}
	var logs bytes.Buffer
	baseTime := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	statePath := filepath.Join(t.TempDir(), "state", "sync-trigger.json")
	o := &Orchestrator{
		RepoPath:           "/repo",
		AgentBinary:        "machtiani",
		PromptTemplatePath: "/prompt.md",
		StatePath:          statePath,
		Logger:             log.New(&logs, "", 0),
		Lister: func(context.Context, string) ([]SessionInfo, error) {
			return []SessionInfo{
				{SessionID: "older", UpdatedAt: baseTime},
				{SessionID: "new", UpdatedAt: baseTime.Add(time.Hour)},
			}, nil
		},
		GitLastCommitTime: func(string) (time.Time, error) {
			return time.Time{}, nil
		},
		RunCommand: runner.Run,
	}
	if err := o.OrchestrateSync(context.Background()); err != nil {
		t.Fatalf("OrchestrateSync: %v", err)
	}

	want := [][]string{
		{"machtiani", "session", "fork", "older"},
		{"machtiani", "run", "--resume", "forked-123", "--file", "/prompt.md"},
		{"machtiani", "session", "delete", "forked-123"},
		{"machtiani", "sync", "--include-docs", "--model", "sync-selected", "--answer-model", "sync-selected"},
	}
	if len(runner.entries) != len(want) {
		t.Fatalf("command count = %d, want %d", len(runner.entries), len(want))
	}
	for i, wantCall := range want {
		got := append([]string{runner.entries[i].name}, runner.entries[i].args...)
		if runner.entries[i].dir != "/repo" {
			t.Fatalf("command %d dir = %q, want %q", i, runner.entries[i].dir, "/repo")
		}
		if got[0] != wantCall[0] {
			t.Fatalf("command %d name = %q, want %q", i, got[0], wantCall[0])
		}
		if len(got)-1 != len(wantCall)-1 {
			t.Fatalf("command %d arg count = %d, want %d", i, len(got)-1, len(wantCall)-1)
		}
		for j, wantArg := range wantCall[1:] {
			if got[j+1] != wantArg {
				t.Fatalf("command %d arg %d = %q, want %q", i, j, got[j+1], wantArg)
			}
		}
	}
	checkpoint, err := loadReviewCheckpoint(statePath)
	if err != nil {
		t.Fatalf("loadReviewCheckpoint: %v", err)
	}
	if checkpoint.SessionID != "older" || !checkpoint.UpdatedAt.Equal(baseTime) {
		t.Fatalf("checkpoint = %+v, want oldest session", checkpoint)
	}
	for _, wantLog := range []string{
		"reviewing source session older; holding 1 newer session(s)",
		"checkpoint advanced source=older",
	} {
		if !strings.Contains(logs.String(), wantLog) {
			t.Fatalf("logs missing %q:\n%s", wantLog, logs.String())
		}
	}

	if err := o.OrchestrateSync(context.Background()); err != nil {
		t.Fatalf("second OrchestrateSync: %v", err)
	}
	if len(runner.entries) != len(want) {
		t.Fatalf("command count after checkpoint = %d, want %d", len(runner.entries), len(want))
	}
}

func TestOrchestrateDrainsBacklogOldestFirstAndHoldsNewest(t *testing.T) {
	t.Parallel()
	runner := &mockRunner{}
	baseTime := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	statePath := filepath.Join(t.TempDir(), "state", "sync-trigger.json")
	o := &Orchestrator{
		RepoPath:           "/repo",
		AgentBinary:        "machtiani",
		PromptTemplatePath: "/prompt.md",
		StatePath:          statePath,
		Logger:             log.New(&bytes.Buffer{}, "", 0),
		Lister: func(context.Context, string) ([]SessionInfo, error) {
			return []SessionInfo{
				{SessionID: "session-d", UpdatedAt: baseTime.Add(4 * time.Hour)},
				{SessionID: "session-b", UpdatedAt: baseTime.Add(2 * time.Hour)},
				{SessionID: "session-a", UpdatedAt: baseTime.Add(time.Hour)},
				{SessionID: "session-c", UpdatedAt: baseTime.Add(3 * time.Hour)},
			}, nil
		},
		GitLastCommitTime: func(string) (time.Time, error) { return baseTime, nil },
		RunCommand:        runner.Run,
	}

	if err := o.OrchestrateSync(context.Background()); err != nil {
		t.Fatalf("OrchestrateSync: %v", err)
	}

	var forked []string
	for _, entry := range runner.entries {
		if entry.name == "machtiani" && len(entry.args) == 3 &&
			entry.args[0] == "session" && entry.args[1] == "fork" {
			forked = append(forked, entry.args[2])
		}
	}
	wantForked := []string{"session-a", "session-b", "session-c"}
	if strings.Join(forked, ",") != strings.Join(wantForked, ",") {
		t.Fatalf("forked sessions = %v, want %v", forked, wantForked)
	}
	checkpoint, err := loadReviewCheckpoint(statePath)
	if err != nil {
		t.Fatalf("loadReviewCheckpoint: %v", err)
	}
	if checkpoint.SessionID != "session-c" {
		t.Fatalf("checkpoint = %+v, want session-c with session-d held", checkpoint)
	}
}

func TestOrchestrateGateStaysOpenAcrossPartialBacklogFailure(t *testing.T) {
	t.Parallel()
	baseTime := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	statePath := filepath.Join(t.TempDir(), "state", "sync-trigger.json")
	if err := saveReviewCheckpoint(statePath, reviewCheckpoint{
		SessionID:      "session-a",
		UpdatedAt:      baseTime,
		CountedThrough: baseTime,
	}); err != nil {
		t.Fatalf("saveReviewCheckpoint: %v", err)
	}

	forked := make([]string, 0, 3)
	syncCalls := 0
	failSecondSync := true
	o := &Orchestrator{
		RepoPath:            "/repo",
		AgentBinary:         "machtiani",
		PromptTemplatePath:  "/prompt.md",
		StatePath:           statePath,
		MaintenanceMinTurns: 20,
		Logger:              log.New(&bytes.Buffer{}, "", 0),
		Lister: func(context.Context, string) ([]SessionInfo, error) {
			return []SessionInfo{
				{SessionID: "session-b", UpdatedAt: baseTime.Add(time.Hour)},
				{SessionID: "session-c", UpdatedAt: baseTime.Add(2 * time.Hour)},
				{SessionID: "session-d", UpdatedAt: baseTime.Add(3 * time.Hour)},
			}, nil
		},
		TurnCounter: func(since time.Time) (int, error) {
			if since.Equal(baseTime) {
				return 20, nil
			}
			return 0, nil
		},
		RunCommand: func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
			command := strings.Join(args, " ")
			switch {
			case strings.HasPrefix(command, "session fork "):
				source := args[2]
				forked = append(forked, source)
				return []byte("forked-" + source + "\n"), nil
			case strings.HasPrefix(command, "run --resume forked-"):
				return nil, nil
			case strings.HasPrefix(command, "session delete forked-"):
				return nil, nil
			case command == "sync --include-docs":
				syncCalls++
				if failSecondSync && syncCalls == 2 {
					return nil, errors.New("second sync failed")
				}
				return nil, nil
			default:
				t.Fatalf("unexpected command: %s", command)
				return nil, nil
			}
		},
	}

	err := o.OrchestrateSync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "second sync failed") {
		t.Fatalf("OrchestrateSync error = %v, want second sync failure", err)
	}
	checkpoint, err := loadReviewCheckpoint(statePath)
	if err != nil {
		t.Fatalf("load checkpoint after partial failure: %v", err)
	}
	if checkpoint.SessionID != "session-b" {
		t.Fatalf("checkpoint after partial failure = %+v, want session-b", checkpoint)
	}
	if checkpoint.TurnsAccumulated < 20 {
		t.Fatalf("checkpoint turns after partial failure = %d, want gate held open", checkpoint.TurnsAccumulated)
	}

	failSecondSync = false
	if err := o.OrchestrateSync(context.Background()); err != nil {
		t.Fatalf("retry OrchestrateSync: %v", err)
	}
	if got, want := strings.Join(forked, ","), "session-b,session-c,session-c"; got != want {
		t.Fatalf("forked sessions = %s, want %s", got, want)
	}
	checkpoint, err = loadReviewCheckpoint(statePath)
	if err != nil {
		t.Fatalf("load checkpoint after retry: %v", err)
	}
	if checkpoint.SessionID != "session-c" || checkpoint.TurnsAccumulated != 0 {
		t.Fatalf("checkpoint after retry = %+v, want session-c with closed gate", checkpoint)
	}
}

func TestOrchestrateCheckpointPreventsLaterGitSyncFromHidingHeldSession(t *testing.T) {
	t.Parallel()
	runner := &mockRunner{}
	baseTime := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	statePath := filepath.Join(t.TempDir(), "state", "sync-trigger.json")
	if err := saveReviewCheckpoint(statePath, reviewCheckpoint{
		SessionID: "session-a",
		UpdatedAt: baseTime.Add(time.Hour),
	}); err != nil {
		t.Fatalf("saveReviewCheckpoint: %v", err)
	}
	o := &Orchestrator{
		RepoPath:           "/repo",
		AgentBinary:        "machtiani",
		PromptTemplatePath: "/prompt.md",
		StatePath:          statePath,
		Logger:             log.New(&bytes.Buffer{}, "", 0),
		Lister: func(context.Context, string) ([]SessionInfo, error) {
			return []SessionInfo{
				{SessionID: "session-b", UpdatedAt: baseTime.Add(2 * time.Hour)},
				{SessionID: "session-c", UpdatedAt: baseTime.Add(4 * time.Hour)},
			}, nil
		},
		GitLastCommitTime: func(string) (time.Time, error) {
			t.Fatal("GitLastCommitTime called after checkpoint exists")
			return baseTime.Add(3 * time.Hour), nil
		},
		RunCommand: runner.Run,
	}

	if err := o.OrchestrateSync(context.Background()); err != nil {
		t.Fatalf("OrchestrateSync: %v", err)
	}
	if len(runner.entries) == 0 || strings.Join(runner.entries[0].args, " ") != "session fork session-b" {
		t.Fatalf("first command = %+v, want held session-b review", runner.entries)
	}
	checkpoint, err := loadReviewCheckpoint(statePath)
	if err != nil {
		t.Fatalf("loadReviewCheckpoint: %v", err)
	}
	if checkpoint.SessionID != "session-b" {
		t.Fatalf("checkpoint = %+v, want session-b", checkpoint)
	}
}

func TestOrchestrateForkFails(t *testing.T) {
	t.Parallel()
	runner := &mockRunner{
		errAt: map[string]error{"machtiani session fork older": errors.New("fork failed")},
	}
	o := &Orchestrator{
		RepoPath:           "/repo",
		AgentBinary:        "machtiani",
		PromptTemplatePath: "/prompt.md",
		Logger:             log.New(&bytes.Buffer{}, "", 0),
		Lister: func(context.Context, string) ([]SessionInfo, error) {
			return []SessionInfo{
				{SessionID: "older", UpdatedAt: time.Now()},
				{SessionID: "old", UpdatedAt: time.Now().Add(time.Hour)},
			}, nil
		},
		GitLastCommitTime: func(string) (time.Time, error) { return time.Time{}, nil },
		RunCommand: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return runner.Run(ctx, dir, name, args...)
		},
	}
	err := o.OrchestrateSync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "fork failed") {
		t.Fatalf("OrchestrateSync error = %v, want fork failure", err)
	}
	if len(runner.entries) != 1 {
		t.Fatalf("command count = %d, want 1", len(runner.entries))
	}
}

func TestOrchestrateRunFails(t *testing.T) {
	t.Parallel()
	runner := &mockRunner{
		errAt: map[string]error{"machtiani run --resume forked-123 --file /prompt.md": errors.New("run failed")},
	}
	o := &Orchestrator{
		RepoPath:           "/repo",
		AgentBinary:        "machtiani",
		PromptTemplatePath: "/prompt.md",
		StatePath:          filepath.Join(t.TempDir(), "sync-trigger.json"),
		Logger:             log.New(&bytes.Buffer{}, "", 0),
		Lister: func(context.Context, string) ([]SessionInfo, error) {
			return []SessionInfo{
				{SessionID: "older", UpdatedAt: time.Now()},
				{SessionID: "old", UpdatedAt: time.Now().Add(time.Hour)},
			}, nil
		},
		GitLastCommitTime: func(string) (time.Time, error) { return time.Time{}, nil },
		RunCommand: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return runner.Run(ctx, dir, name, args...)
		},
	}
	err := o.OrchestrateSync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "run failed") {
		t.Fatalf("OrchestrateSync error = %v, want run failure", err)
	}
	if len(runner.entries) != 3 {
		t.Fatalf("command count = %d, want 3", len(runner.entries))
	}
	cleanup := runner.entries[2]
	if cleanup.name != "machtiani" || strings.Join(cleanup.args, " ") != "session delete forked-123" {
		t.Fatalf("cleanup command = %s %s, want session delete", cleanup.name, strings.Join(cleanup.args, " "))
	}
}

func TestOrchestrateRunCancellationStillCleansUpFork(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	var cleanupContextErr error
	o := &Orchestrator{
		RepoPath:           "/repo",
		AgentBinary:        "machtiani",
		PromptTemplatePath: "/prompt.md",
		StatePath:          filepath.Join(t.TempDir(), "sync-trigger.json"),
		Logger:             log.New(&bytes.Buffer{}, "", 0),
		Lister: func(context.Context, string) ([]SessionInfo, error) {
			return []SessionInfo{
				{SessionID: "older", UpdatedAt: time.Now()},
				{SessionID: "newer", UpdatedAt: time.Now().Add(time.Hour)},
			}, nil
		},
		GitLastCommitTime: func(string) (time.Time, error) { return time.Time{}, nil },
		RunCommand: func(commandContext context.Context, _ string, _ string, args ...string) ([]byte, error) {
			command := strings.Join(args, " ")
			switch command {
			case "session fork older":
				return []byte("forked-123\n"), nil
			case "run --resume forked-123 --file /prompt.md":
				cancel()
				return nil, context.Canceled
			case "session delete forked-123":
				cleanupContextErr = commandContext.Err()
				return nil, nil
			default:
				t.Fatalf("unexpected command: %s", command)
				return nil, nil
			}
		},
	}

	err := o.OrchestrateSync(ctx)
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("OrchestrateSync error = %v, want cancellation", err)
	}
	if cleanupContextErr != nil {
		t.Fatalf("cleanup context error = %v, want active cleanup context", cleanupContextErr)
	}
}

func TestAgentManagedCommandRunnerSetsConfiguredEnvironment(t *testing.T) {
	t.Setenv("AGENT_MANAGER_PATH", "/stale/manager")
	t.Setenv("DEARMACHINE_BACKEND", "stale")
	t.Setenv("DEARMACHINE_BACKENDS", `["stale"]`)
	t.Setenv("MACHTIANI_SESSION_ID", "stale-session")

	runner, err := agentManagedCommandRunner(
		"/configured/agent-manager",
		[]string{"forge", "codex"},
		nil,
	)
	if err != nil {
		t.Fatalf("agentManagedCommandRunner: %v", err)
	}
	output, err := runner(
		context.Background(),
		t.TempDir(),
		"sh",
		"-c",
		`printf '%s\n%s\n%s\n%s\n' "$AGENT_MANAGER_PATH" "$DEARMACHINE_BACKENDS" "${DEARMACHINE_BACKEND-unset}" "${MACHTIANI_SESSION_ID-unset}"`,
	)
	if err != nil {
		t.Fatalf("run command: %v: %s", err, output)
	}
	want := "/configured/agent-manager\n[\"forge\",\"codex\"]\nunset\nunset\n"
	if string(output) != want {
		t.Fatalf("managed environment = %q, want %q", output, want)
	}
}

func TestOrchestrateSyncFailureDoesNotAdvanceCheckpoint(t *testing.T) {
	t.Parallel()
	runner := &mockRunner{
		errAt: map[string]error{"machtiani sync --include-docs": errors.New("sync failed")},
	}
	statePath := filepath.Join(t.TempDir(), "state", "sync-trigger.json")
	o := &Orchestrator{
		RepoPath:           "/repo",
		AgentBinary:        "machtiani",
		PromptTemplatePath: "/prompt.md",
		StatePath:          statePath,
		Logger:             log.New(&bytes.Buffer{}, "", 0),
		Lister: func(context.Context, string) ([]SessionInfo, error) {
			return []SessionInfo{
				{SessionID: "older", UpdatedAt: time.Now()},
				{SessionID: "new", UpdatedAt: time.Now().Add(time.Hour)},
			}, nil
		},
		GitLastCommitTime: func(string) (time.Time, error) { return time.Time{}, nil },
		RunCommand:        runner.Run,
	}

	err := o.OrchestrateSync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sync failed") {
		t.Fatalf("OrchestrateSync error = %v, want sync failure", err)
	}
	checkpoint, err := loadReviewCheckpoint(statePath)
	if err != nil {
		t.Fatalf("loadReviewCheckpoint: %v", err)
	}
	if !checkpoint.UpdatedAt.IsZero() {
		t.Fatalf("checkpoint advanced after sync failure: %+v", checkpoint)
	}
}

func TestOrchestrateDeleteFails(t *testing.T) {
	t.Parallel()
	runner := &mockRunner{
		errAt: map[string]error{"machtiani session delete forked-123": errors.New("delete failed")},
	}
	o := &Orchestrator{
		RepoPath:           "/repo",
		AgentBinary:        "machtiani",
		PromptTemplatePath: "/prompt.md",
		Logger:             log.New(&bytes.Buffer{}, "", 0),
		Lister: func(context.Context, string) ([]SessionInfo, error) {
			return []SessionInfo{
				{SessionID: "older", UpdatedAt: time.Now()},
				{SessionID: "old", UpdatedAt: time.Now().Add(time.Hour)},
			}, nil
		},
		GitLastCommitTime: func(string) (time.Time, error) { return time.Time{}, nil },
		RunCommand: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return runner.Run(ctx, dir, name, args...)
		},
	}
	err := o.OrchestrateSync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "delete failed") {
		t.Fatalf("OrchestrateSync error = %v, want delete failure", err)
	}
	if len(runner.entries) != 3 {
		t.Fatalf("command count = %d, want 3", len(runner.entries))
	}
}

func TestOrchestrateGateCountsTurnsFromStartWithoutCheckpoint(t *testing.T) {
	t.Parallel()
	statePath := filepath.Join(t.TempDir(), "state", "sync-trigger.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var listerCalls int
	var turnCounterCalls int
	var countedSince time.Time
	runner := &mockRunner{}
	baseTime := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	o := &Orchestrator{
		RepoPath:            "/repo",
		AgentBinary:         "machtiani",
		PromptTemplatePath:  "/prompt.md",
		StatePath:           statePath,
		MaintenanceMinTurns: 3,
		Logger:              log.New(&bytes.Buffer{}, "", 0),
		Lister: func(context.Context, string) ([]SessionInfo, error) {
			listerCalls++
			return []SessionInfo{
				{SessionID: "newer", UpdatedAt: baseTime.Add(time.Hour)},
				{SessionID: "oldest", UpdatedAt: baseTime},
			}, nil
		},
		GitLastCommitTime: func(string) (time.Time, error) { return time.Time{}, nil },
		TurnCounter: func(since time.Time) (int, error) {
			turnCounterCalls++
			countedSince = since
			return 5, nil
		},
		RunCommand: runner.Run,
	}
	if err := o.OrchestrateSync(context.Background()); err != nil {
		t.Fatalf("OrchestrateSync: %v", err)
	}
	if listerCalls != 1 {
		t.Fatalf("session lister invoked %d time(s), want 1", listerCalls)
	}
	if turnCounterCalls != 1 {
		t.Fatalf("turn counter invoked %d time(s), want 1", turnCounterCalls)
	}
	if !countedSince.IsZero() {
		t.Fatalf("turn counter since = %s, want zero time", countedSince)
	}
	want := [][]string{
		{"machtiani", "session", "fork", "oldest"},
		{"machtiani", "run", "--resume", "forked-123", "--file", "/prompt.md"},
		{"machtiani", "session", "delete", "forked-123"},
		{"machtiani", "sync", "--include-docs"},
	}
	if len(runner.entries) != len(want) {
		t.Fatalf("command count = %d, want %d", len(runner.entries), len(want))
	}
	for i, wantCall := range want {
		got := append([]string{runner.entries[i].name}, runner.entries[i].args...)
		if strings.Join(got, " ") != strings.Join(wantCall, " ") {
			t.Fatalf("command %d = %q, want %q", i, strings.Join(got, " "), strings.Join(wantCall, " "))
		}
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	if !strings.Contains(string(data), `"turns_accumulated": 0`) {
		t.Fatalf("checkpoint after successful run must reset turns_accumulated to 0, got:\n%s", data)
	}
	if !strings.Contains(string(data), `"counted_through"`) {
		t.Fatalf("checkpoint after successful run must contain counted_through, got:\n%s", data)
	}
}

func TestOrchestrateGateNeverRecountsTurnAfterCrossing(t *testing.T) {
	t.Parallel()
	statePath := filepath.Join(t.TempDir(), "state", "sync-trigger.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	checkpointJSON := `{"session_id":"session-a","updated_at":"2026-08-05T13:00:00Z","turns_accumulated":0}` + "\n"
	if err := os.WriteFile(statePath, []byte(checkpointJSON), 0o600); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}

	var logs bytes.Buffer
	var listerCalls int
	var countedSince []time.Time
	runner := &mockRunner{}
	o := &Orchestrator{
		RepoPath:            "/repo",
		AgentBinary:         "machtiani",
		PromptTemplatePath:  "/prompt.md",
		StatePath:           statePath,
		MaintenanceMinTurns: 3,
		Logger:              log.New(&logs, "", 0),
		Lister: func(context.Context, string) ([]SessionInfo, error) {
			listerCalls++
			return []SessionInfo{
				{SessionID: "session-b", UpdatedAt: time.Date(2026, 8, 5, 14, 0, 0, 0, time.UTC)},
				{SessionID: "session-c", UpdatedAt: time.Date(2026, 8, 5, 15, 0, 0, 0, time.UTC)},
			}, nil
		},
		GitLastCommitTime: func(string) (time.Time, error) { return time.Time{}, nil },
		TurnCounter: func(since time.Time) (int, error) {
			countedSince = append(countedSince, since)
			if since.Before(time.Date(2026, 8, 5, 15, 0, 0, 0, time.UTC)) {
				return 3, nil
			}
			return 0, nil
		},
		RunCommand: runner.Run,
	}

	if err := o.OrchestrateSync(context.Background()); err != nil {
		t.Fatalf("first OrchestrateSync: %v", err)
	}
	if listerCalls != 1 {
		t.Fatalf("session lister invoked %d time(s) after first crossing, want 1", listerCalls)
	}
	if len(runner.entries) != 4 {
		t.Fatalf("command count after first crossing = %d, want 4", len(runner.entries))
	}
	if got := strings.Join(runner.entries[0].args, " "); got != "session fork session-b" {
		t.Fatalf("first command args = %q, want %q", got, "session fork session-b")
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	if !strings.Contains(string(data), `"counted_through"`) {
		t.Errorf("checkpoint after first crossing must contain counted_through, got:\n%s", data)
	}
	checkpoint, err := loadReviewCheckpoint(statePath)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if checkpoint.CountedThrough.IsZero() {
		t.Fatalf("checkpoint after first crossing has zero counted_through: %+v", checkpoint)
	}

	if err := o.OrchestrateSync(context.Background()); err != nil {
		t.Fatalf("second OrchestrateSync: %v", err)
	}
	if listerCalls != 1 {
		t.Fatalf("session lister invoked %d time(s) total, want 1", listerCalls)
	}
	if len(runner.entries) != 4 {
		t.Fatalf("command count after second call = %d, want 4", len(runner.entries))
	}
	if len(countedSince) != 2 {
		t.Fatalf("turn counter invoked %d time(s), want 2", len(countedSince))
	}
	wantLegacySince := time.Date(2026, 8, 5, 13, 0, 0, 0, time.UTC)
	if !countedSince[0].Equal(wantLegacySince) {
		t.Fatalf("first turn counter since = %s, want %s", countedSince[0], wantLegacySince)
	}
	if !countedSince[1].After(time.Date(2026, 8, 5, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("second turn counter since = %s, want after session-c update", countedSince[1])
	}
	for _, wantLog := range []string{"below threshold 3", "0 accumulated turn(s)"} {
		if !strings.Contains(logs.String(), wantLog) {
			t.Fatalf("logs missing %q:\n%s", wantLog, logs.String())
		}
	}
	t.Logf("turn counter since values: %s, %s", countedSince[0].Format(time.RFC3339Nano), countedSince[1].Format(time.RFC3339Nano))
}

func TestOrchestrateGateSkipsBelowThresholdWithoutCheckpoint(t *testing.T) {
	t.Parallel()
	statePath := filepath.Join(t.TempDir(), "state", "sync-trigger.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var logs bytes.Buffer
	var listerCalls int
	var turnCounterCalls int
	var countedSince time.Time
	runner := &mockRunner{}
	o := &Orchestrator{
		RepoPath:            "/repo",
		AgentBinary:         "machtiani",
		PromptTemplatePath:  "/prompt.md",
		StatePath:           statePath,
		MaintenanceMinTurns: 3,
		Logger:              log.New(&logs, "", 0),
		Lister: func(context.Context, string) ([]SessionInfo, error) {
			listerCalls++
			return nil, nil
		},
		GitLastCommitTime: func(string) (time.Time, error) { return time.Time{}, nil },
		TurnCounter: func(since time.Time) (int, error) {
			turnCounterCalls++
			countedSince = since
			return 2, nil
		},
		RunCommand: runner.Run,
	}
	if err := o.OrchestrateSync(context.Background()); err != nil {
		t.Fatalf("OrchestrateSync: %v", err)
	}
	if turnCounterCalls != 1 {
		t.Fatalf("turn counter invoked %d time(s), want 1", turnCounterCalls)
	}
	if !countedSince.IsZero() {
		t.Fatalf("turn counter since = %s, want zero time", countedSince)
	}
	if listerCalls != 0 {
		t.Fatalf("session lister invoked %d time(s), want 0 below turn threshold", listerCalls)
	}
	if len(runner.entries) != 0 {
		t.Fatalf("command count = %d, want 0 below turn threshold", len(runner.entries))
	}
	if !strings.Contains(logs.String(), "below threshold 3") {
		t.Fatalf("logs missing %q:\n%s", "below threshold 3", logs.String())
	}
}

func TestOrchestrateSkippedBelowTurnThreshold(t *testing.T) {
	t.Parallel()
	statePath := filepath.Join(t.TempDir(), "state", "sync-trigger.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	checkpointJSON := `{"session_id":"session-a","updated_at":"2026-08-05T13:00:00Z","turns_accumulated":5}` + "\n"
	if err := os.WriteFile(statePath, []byte(checkpointJSON), 0o600); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	var listerCalls int
	runner := &mockRunner{}
	o := &Orchestrator{
		RepoPath:            "/repo",
		AgentBinary:         "machtiani",
		PromptTemplatePath:  "/prompt.md",
		StatePath:           statePath,
		MaintenanceMinTurns: 20,
		Logger:              log.New(&bytes.Buffer{}, "", 0),
		Lister: func(context.Context, string) ([]SessionInfo, error) {
			listerCalls++
			return []SessionInfo{
				{SessionID: "session-b", UpdatedAt: time.Date(2026, 8, 5, 14, 0, 0, 0, time.UTC)},
			}, nil
		},
		GitLastCommitTime: func(string) (time.Time, error) {
			t.Fatal("GitLastCommitTime must not be called below turn threshold")
			return time.Time{}, nil
		},
		RunCommand: runner.Run,
	}
	if err := o.OrchestrateSync(context.Background()); err != nil {
		t.Fatalf("OrchestrateSync: %v", err)
	}
	if listerCalls != 0 {
		t.Fatalf("session lister invoked %d time(s), want 0 below turn threshold", listerCalls)
	}
	if len(runner.entries) != 0 {
		t.Fatalf("command count = %d, want 0 below turn threshold", len(runner.entries))
	}
}

func TestOrchestrateRunsAtTurnThreshold(t *testing.T) {
	t.Parallel()
	statePath := filepath.Join(t.TempDir(), "state", "sync-trigger.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	checkpointJSON := `{"session_id":"session-a","updated_at":"2026-08-05T13:00:00Z","turns_accumulated":20}` + "\n"
	if err := os.WriteFile(statePath, []byte(checkpointJSON), 0o600); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	runner := &mockRunner{}
	o := &Orchestrator{
		RepoPath:            "/repo",
		AgentBinary:         "machtiani",
		PromptTemplatePath:  "/prompt.md",
		StatePath:           statePath,
		MaintenanceMinTurns: 20,
		Logger:              log.New(&bytes.Buffer{}, "", 0),
		Lister: func(context.Context, string) ([]SessionInfo, error) {
			base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
			return []SessionInfo{
				{SessionID: "session-b", UpdatedAt: base.Add(2 * time.Hour)},
				{SessionID: "session-c", UpdatedAt: base.Add(4 * time.Hour)},
			}, nil
		},
		GitLastCommitTime: func(string) (time.Time, error) { return time.Time{}, nil },
		RunCommand:        runner.Run,
	}
	if err := o.OrchestrateSync(context.Background()); err != nil {
		t.Fatalf("OrchestrateSync: %v", err)
	}
	want := [][]string{
		{"machtiani", "session", "fork", "session-b"},
		{"machtiani", "run", "--resume", "forked-123", "--file", "/prompt.md"},
		{"machtiani", "session", "delete", "forked-123"},
		{"machtiani", "sync", "--include-docs"},
	}
	if len(runner.entries) != len(want) {
		t.Fatalf("command count = %d, want %d", len(runner.entries), len(want))
	}
	for i, wantCall := range want {
		got := append([]string{runner.entries[i].name}, runner.entries[i].args...)
		if strings.Join(got, " ") != strings.Join(wantCall, " ") {
			t.Fatalf("command %d = %q, want %q", i, strings.Join(got, " "), strings.Join(wantCall, " "))
		}
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	if !strings.Contains(string(data), `"turns_accumulated": 0`) {
		t.Fatalf("checkpoint after successful run must reset turns_accumulated to 0, got:\n%s", data)
	}
}

func TestOrchestrateCheckpointPersistsAndResetsTurns(t *testing.T) {
	t.Parallel()
	statePath := filepath.Join(t.TempDir(), "state", "sync-trigger.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	checkpointJSON := `{"session_id":"session-a","updated_at":"2026-08-05T13:00:00Z","turns_accumulated":17}` + "\n"
	if err := os.WriteFile(statePath, []byte(checkpointJSON), 0o600); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	loaded, err := loadReviewCheckpoint(statePath)
	if err != nil {
		t.Fatalf("loadReviewCheckpoint: %v", err)
	}
	if err := saveReviewCheckpoint(statePath, reviewCheckpoint{
		SessionID: loaded.SessionID,
		UpdatedAt: loaded.UpdatedAt,
	}); err != nil {
		t.Fatalf("saveReviewCheckpoint: %v", err)
	}
	saved, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	if !strings.Contains(string(saved), `"turns_accumulated": 0`) {
		t.Fatalf("checkpoint format must persist turns_accumulated (reset to 0 on save), got:\n%s", saved)
	}
}
