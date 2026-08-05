package synctrigger

import (
	"bytes"
	"context"
	"errors"
	"log"
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

	if name == "mct-agent" && len(argsCopy) >= 3 && argsCopy[0] == "session" && argsCopy[1] == "fork" {
		return []byte("forked-123\n"), nil
	}
	return []byte(""), nil
}

func TestOrchestrateNoForkNeeded(t *testing.T) {
	t.Parallel()
	var calls int
	o := &Orchestrator{
		RepoPath:           "/repo",
		MCTBinary:          "mct-agent",
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
	t.Parallel()
	runner := &mockRunner{}
	o := &Orchestrator{
		RepoPath:           "/repo",
		MCTBinary:          "mct-agent",
		PromptTemplatePath: "/prompt.md",
		Logger:             log.New(&bytes.Buffer{}, "", 0),
		Lister: func(context.Context, string) ([]SessionInfo, error) {
			return []SessionInfo{
				{SessionID: "older", UpdatedAt: time.Now()},
				{SessionID: "new", UpdatedAt: time.Now().Add(time.Hour)},
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
		{"mct-agent", "session", "fork", "new"},
		{"mct-agent", "run", "--session-id", "forked-123", "--file", "/prompt.md"},
		{"mct-agent", "session", "delete", "forked-123"},
		{"mct-agent", "sync", "--include-docs"},
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
}

func TestOrchestrateForkFails(t *testing.T) {
	t.Parallel()
	runner := &mockRunner{
		errAt: map[string]error{"mct-agent session fork old": errors.New("fork failed")},
	}
	o := &Orchestrator{
		RepoPath:           "/repo",
		MCTBinary:          "mct-agent",
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
		errAt: map[string]error{"mct-agent run --session-id forked-123 --file /prompt.md": errors.New("run failed")},
	}
	o := &Orchestrator{
		RepoPath:           "/repo",
		MCTBinary:          "mct-agent",
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
	if err == nil || !strings.Contains(err.Error(), "run failed") {
		t.Fatalf("OrchestrateSync error = %v, want run failure", err)
	}
	if len(runner.entries) != 2 {
		t.Fatalf("command count = %d, want 2", len(runner.entries))
	}
}

func TestOrchestrateDeleteFails(t *testing.T) {
	t.Parallel()
	runner := &mockRunner{
		errAt: map[string]error{"mct-agent session delete forked-123": errors.New("delete failed")},
	}
	o := &Orchestrator{
		RepoPath:           "/repo",
		MCTBinary:          "mct-agent",
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
