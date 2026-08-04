package deviceclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type mctTestFixture struct {
	runner     *MCTRunner
	statusFile string
	answerFile string
	finalPath  string
	session    Session
}

func TestNewMCTRunnerValidation(t *testing.T) {
	tests := []struct {
		name       string
		binary     string
		projectDir string
		want       string
	}{
		{name: "missing binary", binary: " ", projectDir: ".", want: "mct-agent binary is required"},
		{name: "missing project", binary: "mct-agent", projectDir: " ", want: "mct project directory is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewMCTRunner(test.binary, test.projectDir, "")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewMCTRunner error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestMCTRunnerPassesApprovedBackendSnapshot(t *testing.T) {
	fixture := newMCTTestFixture(t)
	if err := fixture.runner.ConfigureAgentManaged(
		[]string{"forgecode", "codex"},
		"/test/agent-manager",
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
	captureDir := os.Getenv("FAKE_MCT_CAPTURE")
	backends, err := os.ReadFile(filepath.Join(captureDir, "backends-env-1"))
	if err != nil || string(backends) != `["forgecode","codex"]` {
		t.Fatalf("DEARMACHINE_BACKENDS = %q, %v", backends, err)
	}
	singular, err := os.ReadFile(filepath.Join(captureDir, "backend-env-1"))
	if err != nil || len(singular) != 0 {
		t.Fatalf("DEARMACHINE_BACKEND = %q, %v", singular, err)
	}
}

func TestConfigureAgentManaged_BackendSliceImmutability(t *testing.T) {
	fixture := newMCTTestFixture(t)
	backends := []string{"codex", "forgecode"}
	if err := fixture.runner.ConfigureAgentManaged(backends, "/test/agent-manager"); err != nil {
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
	captureDir := os.Getenv("FAKE_MCT_CAPTURE")
	backendsEnv, err := os.ReadFile(filepath.Join(captureDir, "backends-env-1"))
	if err != nil || string(backendsEnv) != `["codex","forgecode"]` {
		t.Fatalf("DEARMACHINE_BACKENDS = %q, %v", backendsEnv, err)
	}
}

func TestMCTRunnerNonzeroExitContracts(t *testing.T) {
	tests := []struct {
		name       string
		exitEnv    string
		errorEnv   string
		diagnostic string
		want       string
		operation  func(context.Context, *mctTestFixture) error
	}{
		{
			name:       "sync",
			exitEnv:    "FAKE_MCT_SYNC_EXIT",
			errorEnv:   "FAKE_MCT_SYNC_ERROR",
			diagnostic: "sync diagnostic",
			want:       "mct-agent sync failed: exit status 17: sync diagnostic",
			operation: func(ctx context.Context, fixture *mctTestFixture) error {
				return fixture.runner.Sync(ctx)
			},
		},
		{
			name:       "run",
			exitEnv:    "FAKE_MCT_RUN_EXIT",
			errorEnv:   "FAKE_MCT_RUN_ERROR",
			diagnostic: "run diagnostic",
			want:       "mct-agent run failed: exit status 17: run diagnostic",
			operation: func(ctx context.Context, fixture *mctTestFixture) error {
				_, err := fixture.runner.Run(ctx, fixture.session, "prompt", fixture.finalPath)
				return err
			},
		},
		{
			name:       "session show",
			exitEnv:    "FAKE_MCT_SHOW_EXIT",
			errorEnv:   "FAKE_MCT_SHOW_ERROR",
			diagnostic: "show diagnostic",
			want:       "mct-agent session show failed: exit status 17: show diagnostic",
			operation: func(ctx context.Context, fixture *mctTestFixture) error {
				_, err := fixture.runner.Run(ctx, fixture.session, "prompt", fixture.finalPath)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMCTTestFixture(t)
			t.Setenv(test.exitEnv, "17")
			t.Setenv(test.errorEnv, test.diagnostic)
			err := test.operation(context.Background(), fixture)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("operation error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestMCTRunnerTimeoutContracts(t *testing.T) {
	tests := []struct {
		name      string
		delayEnv  string
		operation func(context.Context, *mctTestFixture) error
		want      string
	}{
		{
			name:     "sync",
			delayEnv: "FAKE_MCT_SYNC_DELAY",
			operation: func(ctx context.Context, fixture *mctTestFixture) error {
				return fixture.runner.Sync(ctx)
			},
			want: "mct-agent sync failed",
		},
		{
			name:     "run",
			delayEnv: "FAKE_MCT_RUN_DELAY",
			operation: func(ctx context.Context, fixture *mctTestFixture) error {
				_, err := fixture.runner.Run(ctx, fixture.session, "prompt", fixture.finalPath)
				return err
			},
			want: "mct-agent run failed",
		},
		{
			name:     "session show",
			delayEnv: "FAKE_MCT_SHOW_DELAY",
			operation: func(ctx context.Context, fixture *mctTestFixture) error {
				_, err := fixture.runner.showSession(ctx, fixture.session.SessionID)
				return err
			},
			want: "mct-agent session show failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMCTTestFixture(t)
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

func TestMCTRunnerSessionResponseContracts(t *testing.T) {
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
			want:   "parse mct-agent session status",
		},
		{
			name:      "missing output file",
			status:    `{"status":"success"}`,
			answer:    "answer",
			skipFinal: true,
			want:      "mct-agent reported success without a final answer file",
		},
		{
			name:   "empty output file",
			status: `{"status":"success"}`,
			answer: "  \n",
			want:   "mct-agent reported success with an empty final answer",
		},
		{
			name:   "unexpected status",
			status: `{"status":"running"}`,
			answer: "answer",
			want:   `mct-agent returned unsupported status "running"`,
		},
		{
			name:   "suspended without input",
			status: `{"status":"suspended_user_input"}`,
			answer: "answer",
			want:   "mct-agent suspended without a question",
		},
		{
			name:   "suspended with blank question",
			status: `{"status":"suspended_user_input","suspended_user_input":{"question":"  "}}`,
			answer: "answer",
			want:   "mct-agent suspended without a question",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMCTTestFixture(t)
			writeTestFile(t, fixture.statusFile, test.status)
			writeTestFile(t, fixture.answerFile, test.answer)
			if test.skipFinal {
				t.Setenv("FAKE_MCT_SKIP_FINAL", "1")
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

func TestMCTRunnerRequiresFinalAnswerPath(t *testing.T) {
	fixture := newMCTTestFixture(t)
	_, err := fixture.runner.Run(context.Background(), fixture.session, "prompt", " ")
	if err == nil || !strings.Contains(err.Error(), "mct final answer path is required") {
		t.Fatalf("Run empty final path error = %v", err)
	}
}

func TestMCTRunnerRecoverRerunsAfterUnusableSessionState(t *testing.T) {
	tests := []struct {
		name   string
		status string
	}{
		{name: "goal mismatch", status: `{"status":"success","goal":"different prompt"}`},
		{name: "malformed status", status: `{`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMCTTestFixture(t)
			writeTestFile(t, fixture.statusFile, test.status)
			result, err := fixture.runner.Recover(
				context.Background(),
				fixture.session,
				"original prompt",
				fixture.finalPath,
			)
			if test.name == "malformed status" {
				if err == nil || !strings.Contains(err.Error(), "parse mct-agent session status") {
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

func newMCTTestFixture(t *testing.T) *mctTestFixture {
	t.Helper()
	fixturePath, err := filepath.Abs(filepath.Join("testdata", "fake-mct-agent.sh"))
	if err != nil {
		t.Fatalf("fixture path: %v", err)
	}
	captureDir := t.TempDir()
	statusFile := filepath.Join(t.TempDir(), "status.json")
	answerFile := filepath.Join(t.TempDir(), "answer.md")
	t.Setenv("FAKE_MCT_CAPTURE", captureDir)
	t.Setenv("FAKE_MCT_STATUS", statusFile)
	t.Setenv("FAKE_MCT_ANSWER", answerFile)
	writeTestFile(t, statusFile, `{"status":"success"}`)
	writeTestFile(t, answerFile, "test answer")
	runner, err := NewMCTRunner(fixturePath, t.TempDir(), "")
	if err != nil {
		t.Fatalf("NewMCTRunner: %v", err)
	}
	if err := runner.ConfigureAgentManaged([]string{"codex"}, "/test/agent-manager"); err != nil {
		t.Fatalf("ConfigureAgentManaged: %v", err)
	}
	return &mctTestFixture{
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
