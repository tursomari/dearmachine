package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type agentTestFixture struct {
	runner     *AgentRunner
	statusFile string
	answerFile string
	finalPath  string
	session    Session
}

func TestNewAgentRunnerValidation(t *testing.T) {
	tests := []struct {
		name       string
		binary     string
		projectDir string
		want       string
	}{
		{name: "missing binary", binary: " ", projectDir: ".", want: "machtiani binary is required"},
		{name: "missing project", binary: "machtiani", projectDir: " ", want: "agent project directory is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewAgentRunner(test.binary, test.projectDir, "")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewAgentRunner error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestAgentRunnerPassesApprovedBackendSnapshot(t *testing.T) {
	fixture := newAgentTestFixture(t)
	if err := fixture.runner.ConfigureAgentManaged(
		[]string{"forge", "codex"},
		"/test/agent-manager",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEARMACHINE_BACKEND", "stale-parent-value")
	if _, err := fixture.runner.Run(
		context.Background(),
		fixture.session,
		"prompt",
		fixture.finalPath,
	); err != nil {
		t.Fatal(err)
	}
	captureDir := os.Getenv("FAKE_AGENT_CAPTURE")
	backends, err := os.ReadFile(filepath.Join(captureDir, "backends-env-1"))
	if err != nil || string(backends) != `["forge","codex"]` {
		t.Fatalf("DEARMACHINE_BACKENDS = %q, %v", backends, err)
	}
	singular, err := os.ReadFile(filepath.Join(captureDir, "backend-env-1"))
	if err != nil || len(singular) != 0 {
		t.Fatalf("DEARMACHINE_BACKEND = %q, %v", singular, err)
	}
}

func TestConfigureAgentManaged_BackendSliceImmutability(t *testing.T) {
	fixture := newAgentTestFixture(t)
	backends := []string{"codex", "forge"}
	if err := fixture.runner.ConfigureAgentManaged(backends, "/test/agent-manager", nil); err != nil {
		t.Fatalf("ConfigureAgentManaged: %v", err)
	}
	backends[0] = "evil-backend"
	if _, err := fixture.runner.Run(
		context.Background(),
		fixture.session,
		"prompt",
		fixture.finalPath,
	); err != nil {
		t.Fatal(err)
	}
	captureDir := os.Getenv("FAKE_AGENT_CAPTURE")
	backendsEnv, err := os.ReadFile(filepath.Join(captureDir, "backends-env-1"))
	if err != nil || string(backendsEnv) != `["codex","forge"]` {
		t.Fatalf("DEARMACHINE_BACKENDS = %q, %v", backendsEnv, err)
	}
}

func TestAgentRunnerNonzeroExitContracts(t *testing.T) {
	tests := []struct {
		name       string
		exitEnv    string
		errorEnv   string
		diagnostic string
		want       string
		operation  func(context.Context, *agentTestFixture) error
	}{
		{
			name:       "sync",
			exitEnv:    "FAKE_AGENT_SYNC_EXIT",
			errorEnv:   "FAKE_AGENT_SYNC_ERROR",
			diagnostic: "sync diagnostic",
			want:       "machtiani sync failed: exit status 17: sync diagnostic",
			operation: func(ctx context.Context, fixture *agentTestFixture) error {
				return fixture.runner.Sync(ctx)
			},
		},
		{
			name:       "run",
			exitEnv:    "FAKE_AGENT_RUN_EXIT",
			errorEnv:   "FAKE_AGENT_RUN_ERROR",
			diagnostic: "run diagnostic",
			want:       "machtiani run failed: exit status 17: run diagnostic",
			operation: func(ctx context.Context, fixture *agentTestFixture) error {
				_, err := fixture.runner.Run(ctx, fixture.session, "prompt", fixture.finalPath)
				return err
			},
		},
		{
			name:       "session fork",
			exitEnv:    "FAKE_AGENT_FORK_EXIT",
			errorEnv:   "FAKE_AGENT_FORK_ERROR",
			diagnostic: "fork diagnostic",
			want:       "machtiani session fork failed: exit status 17: fork diagnostic",
			operation: func(ctx context.Context, fixture *agentTestFixture) error {
				_, err := fixture.runner.ForkSession(ctx, fixture.session.SessionID)
				return err
			},
		},
		{
			name:       "session show",
			exitEnv:    "FAKE_AGENT_SHOW_EXIT",
			errorEnv:   "FAKE_AGENT_SHOW_ERROR",
			diagnostic: "show diagnostic",
			want:       "machtiani session show failed: exit status 17: show diagnostic",
			operation: func(ctx context.Context, fixture *agentTestFixture) error {
				_, err := fixture.runner.Run(ctx, fixture.session, "prompt", fixture.finalPath)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAgentTestFixture(t)
			t.Setenv(test.exitEnv, "17")
			t.Setenv(test.errorEnv, test.diagnostic)
			err := test.operation(context.Background(), fixture)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("operation error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestAgentRunnerForkAndDeleteSession(t *testing.T) {
	fixture := newAgentTestFixture(t)
	t.Setenv("FAKE_AGENT_FORK_ID", "replacement-session")
	forked, err := fixture.runner.ForkSession(context.Background(), fixture.session.SessionID)
	if err != nil || forked != "replacement-session" {
		t.Fatalf("ForkSession = %q, %v", forked, err)
	}
	if err := fixture.runner.DeleteSession(context.Background(), fixture.session.SessionID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	deleted, err := os.ReadFile(filepath.Join(os.Getenv("FAKE_AGENT_CAPTURE"), "deleted-sessions"))
	if err != nil || string(deleted) != fixture.session.SessionID+"\n" {
		t.Fatalf("deleted sessions = %q, %v", deleted, err)
	}
}

func TestAgentRunnerForkRejectsInvalidSessionID(t *testing.T) {
	fixture := newAgentTestFixture(t)
	for _, forkedID := range []string{"", "two session-ids", fixture.session.SessionID} {
		t.Run(strings.ReplaceAll(forkedID, " ", "_"), func(t *testing.T) {
			t.Setenv("FAKE_AGENT_FORK_ID", forkedID)
			if _, err := fixture.runner.ForkSession(
				context.Background(),
				fixture.session.SessionID,
			); err == nil {
				t.Fatalf("ForkSession accepted %q", forkedID)
			}
		})
	}
}

func TestAgentRunnerTimeoutContracts(t *testing.T) {
	tests := []struct {
		name      string
		delayEnv  string
		operation func(context.Context, *agentTestFixture) error
		want      string
	}{
		{
			name:     "sync",
			delayEnv: "FAKE_AGENT_SYNC_DELAY",
			operation: func(ctx context.Context, fixture *agentTestFixture) error {
				return fixture.runner.Sync(ctx)
			},
			want: "machtiani sync failed",
		},
		{
			name:     "run",
			delayEnv: "FAKE_AGENT_RUN_DELAY",
			operation: func(ctx context.Context, fixture *agentTestFixture) error {
				_, err := fixture.runner.Run(ctx, fixture.session, "prompt", fixture.finalPath)
				return err
			},
			want: "machtiani run failed",
		},
		{
			name:     "session show",
			delayEnv: "FAKE_AGENT_SHOW_DELAY",
			operation: func(ctx context.Context, fixture *agentTestFixture) error {
				_, err := fixture.runner.showSession(ctx, fixture.session.SessionID)
				return err
			},
			want: "machtiani session show failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAgentTestFixture(t)
			t.Setenv(test.delayEnv, "0.5")
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			err := test.operation(ctx, fixture)
			if err == nil || !errors.Is(err, context.DeadlineExceeded) ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("operation timeout error = %v, want wrapped deadline and %q", err, test.want)
			}
		})
	}
}

func TestAgentRunnerSessionResponseContracts(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		answer    string
		skipFinal bool
		want      string
	}{
		{
			name:   "malformed response",
			status: `{`,
			answer: "answer",
			want:   "parse machtiani session status",
		},
		{
			name:      "missing output file",
			status:    `{"status":"success"}`,
			answer:    "answer",
			skipFinal: true,
			want:      "machtiani reported success without a final answer file",
		},
		{
			name:   "empty output file",
			status: `{"status":"success"}`,
			answer: "  \n",
			want:   "machtiani reported success with an empty final answer",
		},
		{
			name:   "unexpected status",
			status: `{"status":"running"}`,
			answer: "answer",
			want:   `machtiani returned unsupported status "running"`,
		},
		{
			name:   "suspended without input",
			status: `{"status":"suspended_user_input"}`,
			answer: "answer",
			want:   "machtiani suspended without a question",
		},
		{
			name:   "suspended with blank question",
			status: `{"status":"suspended_user_input","suspended_user_input":{"question":"  "}}`,
			answer: "answer",
			want:   "machtiani suspended without a question",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAgentTestFixture(t)
			writeTestFile(t, fixture.statusFile, test.status)
			writeTestFile(t, fixture.answerFile, test.answer)
			if test.skipFinal {
				t.Setenv("FAKE_AGENT_SKIP_FINAL", "1")
			}
			_, err := fixture.runner.Run(
				context.Background(),
				fixture.session,
				"prompt",
				fixture.finalPath,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestAgentRunnerRequiresFinalAnswerPath(t *testing.T) {
	fixture := newAgentTestFixture(t)
	_, err := fixture.runner.Run(context.Background(), fixture.session, "prompt", " ")
	if err == nil || !strings.Contains(err.Error(), "agent final answer path is required") {
		t.Fatalf("Run empty final path error = %v", err)
	}
}

func TestAgentRunnerRecoverRerunsAfterUnusableSessionState(t *testing.T) {
	tests := []struct {
		name   string
		status string
	}{
		{name: "goal mismatch", status: `{"status":"success","goal":"different prompt"}`},
		{name: "malformed status", status: `{`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAgentTestFixture(t)
			writeTestFile(t, fixture.statusFile, test.status)
			result, err := fixture.runner.Recover(
				context.Background(),
				fixture.session,
				"original prompt",
				fixture.finalPath,
			)
			if test.name == "malformed status" {
				if err == nil || !strings.Contains(err.Error(), "parse machtiani session status") {
					t.Fatalf("Recover malformed status error = %v", err)
				}
				return
			}
			if err != nil || result.Kind != ResultAnswer || result.Text != "test answer" {
				t.Fatalf("Recover result = %+v, %v", result, err)
			}
		})
	}
}

func newAgentTestFixture(t *testing.T) *agentTestFixture {
	t.Helper()
	fixturePath, err := filepath.Abs(filepath.Join("testdata", "fake-agent.sh"))
	if err != nil {
		t.Fatalf("fixture path: %v", err)
	}
	captureDir := t.TempDir()
	statusFile := filepath.Join(t.TempDir(), "status.json")
	answerFile := filepath.Join(t.TempDir(), "answer.md")
	t.Setenv("FAKE_AGENT_CAPTURE", captureDir)
	t.Setenv("FAKE_AGENT_STATUS", statusFile)
	t.Setenv("FAKE_AGENT_ANSWER", answerFile)
	writeTestFile(t, statusFile, `{"status":"success"}`)
	writeTestFile(t, answerFile, "test answer")
	runner, err := NewAgentRunner(fixturePath, t.TempDir(), "")
	if err != nil {
		t.Fatalf("NewAgentRunner: %v", err)
	}
	if err := runner.ConfigureAgentManaged([]string{"codex"}, "/test/agent-manager", nil); err != nil {
		t.Fatalf("ConfigureAgentManaged: %v", err)
	}
	return &agentTestFixture{
		runner:     runner,
		statusFile: statusFile,
		answerFile: answerFile,
		finalPath:  filepath.Join(t.TempDir(), "final.md"),
		session: Session{
			ThreadID:  "thread-1",
			SessionID: "session-1",
			Sequence:  1,
			Status:    "active",
			IsNew:     true,
		},
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
