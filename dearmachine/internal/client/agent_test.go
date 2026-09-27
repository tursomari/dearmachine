package client

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
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
	turn       TurnContext
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
		fixture.turn,
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

func TestAgentRunnerMagnificaHumanitasArgv(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			fixture := newAgentTestFixture(t)
			fixture.runner.magnificaHumanitas = enabled
			var fullArgs []string
			fixture.runner.invoke = func(command *exec.Cmd) error {
				if len(command.Args) > 1 && command.Args[1] == "run" {
					fullArgs = append([]string(nil), command.Args[1:]...)
				}
				return command.Run()
			}

			if _, err := fixture.runner.Run(
				context.Background(),
				fixture.session,
				"prompt",
				fixture.finalPath,
				fixture.turn,
			); err != nil {
				t.Fatal(err)
			}

			captured, err := os.ReadFile(filepath.Join(os.Getenv("FAKE_AGENT_CAPTURE"), "args-1"))
			if err != nil {
				t.Fatal(err)
			}
			capturedArgs := strings.Split(strings.TrimSpace(string(captured)), "\n")
			wantCaptured := []string{
				"--prompt",
				"--mode", "agent-managed",
				"--no-banner",
				"--no-cursor",
				"--final-file",
			}
			if enabled {
				wantCaptured = append([]string{"--prompt", "--magnifica-humanitas"}, wantCaptured[1:]...)
			}
			if !reflect.DeepEqual(capturedArgs, wantCaptured) {
				t.Fatalf("captured machtiani run args = %q, want %q", capturedArgs, wantCaptured)
			}

			wantFull := []string{
				"run",
				"--prompt", "prompt",
				"--mode", "agent-managed",
				"--no-banner",
				"--no-cursor",
				"--final-file", fixture.finalPath,
			}
			if enabled {
				wantFull = append([]string{"run", "--prompt", "prompt", "--magnifica-humanitas"}, wantFull[3:]...)
			}
			if !reflect.DeepEqual(fullArgs, wantFull) {
				t.Fatalf("machtiani run argv = %q, want %q", fullArgs, wantFull)
			}

			count := 0
			for _, arg := range fullArgs {
				if arg == "--magnifica-humanitas" {
					count++
				}
				if strings.Contains(arg, "magnifica_humanitas") || arg == "-magnifica-humanitas" {
					t.Fatalf("machtiani run args contain Magnifica Humanitas alias: %q", fullArgs)
				}
			}
			wantCount := 0
			if enabled {
				wantCount = 1
			}
			if count != wantCount {
				t.Fatalf("--magnifica-humanitas count = %d, want %d", count, wantCount)
			}
		})
	}
}

func TestAgentRunnerParsesMagnificaHumanitas(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   *MagnificaHumanitas
	}{
		{
			name:   "present",
			status: `{"status":"success","magnifica_humanitas":{"paragraph":245,"line":1,"quote":"Quo vadis, humanitas?"}}`,
			want: &MagnificaHumanitas{
				Paragraph: 245,
				Line:      1,
				Quote:     "Quo vadis, humanitas?",
			},
		},
		{name: "absent", status: `{"status":"success"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAgentTestFixture(t)
			writeTestFile(t, fixture.statusFile, test.status)

			result, err := fixture.runner.Run(
				context.Background(),
				fixture.session,
				"prompt",
				fixture.finalPath,
				fixture.turn,
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.Kind != ResultAnswer || result.Text != "test answer" ||
				!reflect.DeepEqual(result.MagnificaHumanitas, test.want) {
				t.Fatalf("Run result = %+v, want Magnifica Humanitas %+v", result, test.want)
			}
		})
	}
}

func TestRunResultZeroValue(t *testing.T) {
	var result RunResult
	if result.Kind != "" || result.Text != "" || result.MagnificaHumanitas != nil {
		t.Fatalf("RunResult zero value = %+v", result)
	}
}

func TestAgentRunnerReturnsGracefulStopOnInterrupt(t *testing.T) {
	fixture := newAgentTestFixture(t)
	t.Setenv("FAKE_AGENT_RUN_DELAY", "5")
	writeTestFile(t, fixture.statusFile, `{"status":"interrupted"}`)

	done := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		_, err := fixture.runner.runObserved(
			context.Background(),
			fixture.session,
			"prompt",
			fixture.finalPath,
			fixture.turn,
			func() { close(started) },
		)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("fake agent run did not start")
	}
	if !fixture.runner.Stop("thread-1") {
		t.Fatal("Stop did not find the running thread")
	}
	if err := <-done; !errors.Is(err, ErrGracefullyStopped) {
		t.Fatalf("Run error = %v, want ErrGracefullyStopped", err)
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
		fixture.turn,
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
				_, err := fixture.runner.Run(ctx, fixture.session, "prompt", fixture.finalPath, fixture.turn)
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
				_, err := fixture.runner.ForkSession(ctx, fixture.session.SessionID, "")
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
				_, err := fixture.runner.Run(ctx, fixture.session, "prompt", fixture.finalPath, fixture.turn)
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
	forkedSessionID := newConversationReference()
	t.Setenv("FAKE_AGENT_FORK_ID", forkedSessionID)
	forked, err := fixture.runner.ForkSession(
		context.Background(),
		fixture.session.SessionID,
		"",
	)
	if err != nil || forked != forkedSessionID {
		t.Fatalf("ForkSession = %q, %v", forked, err)
	}
	if err := fixture.runner.DeleteSession(
		context.Background(),
		fixture.session.SessionID,
	); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	deleted, err := os.ReadFile(filepath.Join(os.Getenv("FAKE_AGENT_CAPTURE"), "deleted-sessions"))
	if err != nil || string(deleted) != fixture.session.SessionID+"\n" {
		t.Fatalf("deleted sessions = %q, %v", deleted, err)
	}
}

func TestAgentRunnerForkSessionAcceptsOpaqueForkedID(t *testing.T) {
	fixture := newAgentTestFixture(t)
	const forkedSessionID = "agent-20260821T141425-8795"
	t.Setenv("FAKE_AGENT_FORK_ID", forkedSessionID)

	forked, err := fixture.runner.ForkSession(
		context.Background(),
		fixture.session.SessionID,
		"",
	)
	if err != nil || forked != forkedSessionID {
		t.Fatalf("ForkSession = %q, %v", forked, err)
	}
}

func TestAgentRunnerForkSessionUsesExplicitCanonicalDestination(t *testing.T) {
	fixture := newAgentTestFixture(t)
	const (
		checkpointSessionID  = "agent-20260821T141425-8795"
		destinationSessionID = "dm1-stvwx-yz0123"
	)

	forked, err := fixture.runner.ForkSession(
		context.Background(),
		checkpointSessionID,
		destinationSessionID,
	)
	if err != nil || forked != destinationSessionID {
		t.Fatalf("ForkSession = %q, %v", forked, err)
	}
	source, err := os.ReadFile(filepath.Join(os.Getenv("FAKE_AGENT_CAPTURE"), "forked-sessions"))
	if err != nil || string(source) != checkpointSessionID+"\n" {
		t.Fatalf("forked source = %q, %v", source, err)
	}
	destination, err := os.ReadFile(filepath.Join(os.Getenv("FAKE_AGENT_CAPTURE"), "forked-destinations"))
	if err != nil || string(destination) != destinationSessionID+"\n" {
		t.Fatalf("forked destination = %q, %v", destination, err)
	}
}

func TestAgentRunnerForkSessionRejectsControlWhitespaceOutput(t *testing.T) {
	fixture := newAgentTestFixture(t)
	for name, forkedID := range map[string]string{
		"empty":           "",
		"spaces":          "two session-ids",
		"carriage return": "agent-first\ragent-second",
		"line feed":       "agent-first\nagent-second",
		"nul":             "agent-first\x00agent-second",
		"source ID":       fixture.session.SessionID,
	} {
		t.Run(name, func(t *testing.T) {
			fixture.runner.invoke = func(command *exec.Cmd) error {
				if _, err := command.Stdout.Write([]byte(forkedID)); err != nil {
					return err
				}
				return nil
			}
			if _, err := fixture.runner.ForkSession(
				context.Background(),
				fixture.session.SessionID,
				"",
			); err == nil {
				t.Fatalf("ForkSession accepted %q", forkedID)
			}
		})
	}
}

func TestAgentRunnerDeleteSessionAcceptsOpaqueID(t *testing.T) {
	fixture := newAgentTestFixture(t)
	const opaqueSessionID = "agent-20260821T141425-8795"

	if err := fixture.runner.DeleteSession(context.Background(), opaqueSessionID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	deleted, err := os.ReadFile(filepath.Join(os.Getenv("FAKE_AGENT_CAPTURE"), "deleted-sessions"))
	if err != nil || string(deleted) != opaqueSessionID+"\n" {
		t.Fatalf("deleted sessions = %q, %v", deleted, err)
	}

	for name, sessionID := range map[string]string{
		"empty":     "",
		"blank":     " \t ",
		"spaces":    "two session-ids",
		"line feed": "agent-first\nagent-second",
		"nul":       "agent-first\x00agent-second",
	} {
		t.Run(name, func(t *testing.T) {
			if err := fixture.runner.DeleteSession(context.Background(), sessionID); err == nil {
				t.Fatalf("DeleteSession accepted %q", sessionID)
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
				_, err := fixture.runner.Run(ctx, fixture.session, "prompt", fixture.finalPath, fixture.turn)
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
				fixture.turn,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestAgentRunnerRequiresFinalAnswerPath(t *testing.T) {
	fixture := newAgentTestFixture(t)
	_, err := fixture.runner.Run(context.Background(), fixture.session, "prompt", " ", fixture.turn)
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
				fixture.turn,
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

func TestValidateTurnContextRejectsInvalidInputs(t *testing.T) {
	runner, err := NewAgentRunner("machtiani", t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*TurnContext)
		want   string
	}{
		{
			name:   "empty inbox",
			mutate: func(tc *TurnContext) { tc.InboxPath = "" },
			want:   "turn context inbox path",
		},
		{
			name:   "empty outbox",
			mutate: func(tc *TurnContext) { tc.OutboxPath = "" },
			want:   "turn context outbox path",
		},
		{
			name:   "empty manifest",
			mutate: func(tc *TurnContext) { tc.ManifestPath = "" },
			want:   "turn context manifest path",
		},
		{
			name:   "empty tier",
			mutate: func(tc *TurnContext) { tc.Tier = "" },
			want:   "turn context tier",
		},
		{
			name:   "tier fancy",
			mutate: func(tc *TurnContext) { tc.Tier = "fancy" },
			want:   "turn context tier",
		},
		{
			name:   "relative inbox path",
			mutate: func(tc *TurnContext) { tc.InboxPath = "relative/inbox" },
			want:   "turn context inbox path",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tc := validTurnContext()
			test.mutate(&tc)
			err := runner.ValidateTurnContext(tc)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateTurnContext error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateTurnContextAcceptsValidAbsolutePaths(t *testing.T) {
	runner, err := NewAgentRunner("machtiani", t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	tc := TurnContext{
		InboxPath:    "/tmp/in",
		OutboxPath:   "/tmp/out",
		ManifestPath: "/tmp/manifest.json",
		Tier:         "formatted",
	}
	if err := runner.ValidateTurnContext(tc); err != nil {
		t.Fatalf("ValidateTurnContext valid context error = %v", err)
	}
}

func TestRunExposesAttachmentEnvironmentExactly(t *testing.T) {
	fixture := newAgentTestFixture(t)
	t.Setenv("DEARMACHINE_ATTACHMENTS_INBOX", "stale-inbox")
	t.Setenv("DEARMACHINE_ATTACHMENTS_OUTBOX", "stale-outbox")
	t.Setenv("DEARMACHINE_ATTACHMENTS_MANIFEST", "stale-manifest")
	t.Setenv("DEARMACHINE_BACKEND", "stale-parent-value")

	var captured [][]string
	fixture.runner.invoke = func(command *exec.Cmd) error {
		if len(command.Args) > 1 && command.Args[1] == "run" {
			captured = append(captured, append([]string(nil), command.Env...))
		}
		return command.Run()
	}

	if _, err := fixture.runner.Run(
		context.Background(),
		fixture.session,
		"prompt",
		fixture.finalPath,
		fixture.turn,
	); err != nil {
		t.Fatal(err)
	}
	if len(captured) != 1 {
		t.Fatalf("captured run envs = %d, want 1", len(captured))
	}
	env := captured[0]
	requireUniqueEnv(t, env, "DEARMACHINE_ATTACHMENTS_INBOX", fixture.turn.InboxPath)
	requireUniqueEnv(t, env, "DEARMACHINE_ATTACHMENTS_OUTBOX", fixture.turn.OutboxPath)
	requireUniqueEnv(t, env, "DEARMACHINE_ATTACHMENTS_MANIFEST", fixture.turn.ManifestPath)
	requireUniqueEnv(t, env, "MACHTIANI_SESSION_ID", fixture.session.SessionID)
	requireUniqueEnv(t, env, "AGENT_MANAGER_PATH", "/test/agent-manager")
	if values := envValues(env, "DEARMACHINE_BACKEND"); len(values) != 0 {
		t.Fatalf("DEARMACHINE_BACKEND = %q, want unset", values)
	}

	captureDir := os.Getenv("FAKE_AGENT_CAPTURE")
	sessionID, err := os.ReadFile(filepath.Join(captureDir, "session-env-1"))
	if err != nil || string(sessionID) != fixture.session.SessionID {
		t.Fatalf("MACHTIANI_SESSION_ID = %q, %v", sessionID, err)
	}
	backend, err := os.ReadFile(filepath.Join(captureDir, "backend-env-1"))
	if err != nil || len(backend) != 0 {
		t.Fatalf("DEARMACHINE_BACKEND = %q, %v", backend, err)
	}
	manager, err := os.ReadFile(filepath.Join(captureDir, "manager-env-1"))
	if err != nil || string(manager) != "/test/agent-manager" {
		t.Fatalf("AGENT_MANAGER_PATH = %q, %v", manager, err)
	}
}

func TestRunRejectsUnvalidatedTurnContext(t *testing.T) {
	fixture := newAgentTestFixture(t)
	_, err := fixture.runner.Run(
		context.Background(),
		fixture.session,
		"prompt",
		fixture.finalPath,
		TurnContext{},
	)
	if err == nil || !strings.Contains(err.Error(), "validate turn context") {
		t.Fatalf("Run empty turn context error = %v", err)
	}
}

func TestRecoverForwardsSameTurnContext(t *testing.T) {
	fixture := newAgentTestFixture(t)
	writeTestFile(t, fixture.statusFile, `{"status":"in_progress","goal":"original prompt"}`)

	var captured [][]string
	var capturedArgs [][]string
	fixture.runner.invoke = func(command *exec.Cmd) error {
		if len(command.Args) > 1 && command.Args[1] == "run" {
			captured = append(captured, append([]string(nil), command.Env...))
			capturedArgs = append(capturedArgs, append([]string(nil), command.Args...))
		}
		return command.Run()
	}

	_, err := fixture.runner.Recover(
		context.Background(),
		fixture.session,
		"original prompt",
		fixture.finalPath,
		fixture.turn,
	)
	if len(captured) != 1 {
		t.Fatalf("captured recovery run envs = %d, want 1 (err=%v)", len(captured), err)
	}
	if len(capturedArgs) != 1 {
		t.Fatalf("captured recovery run args = %d, want 1", len(capturedArgs))
	}
	args := capturedArgs[0]
	if !slices.Contains(args, "--resume") || slices.Contains(args, "--prompt") {
		t.Fatalf("recovery args = %v, want --resume without --prompt", args)
	}
	if got := agentCapture(t, "text-1"); got != "" {
		t.Fatalf("recovery prompt = %q, want empty", got)
	}
	env := captured[0]
	requireUniqueEnv(t, env, "DEARMACHINE_ATTACHMENTS_INBOX", fixture.turn.InboxPath)
	requireUniqueEnv(t, env, "DEARMACHINE_ATTACHMENTS_OUTBOX", fixture.turn.OutboxPath)
	requireUniqueEnv(t, env, "DEARMACHINE_ATTACHMENTS_MANIFEST", fixture.turn.ManifestPath)
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
			SessionID: testCanonicalConversationReference,
			Sequence:  1,
			Status:    "active",
			IsNew:     true,
		},
		turn: validTurnContext(),
	}
}

func validTurnContext() TurnContext {
	return TurnContext{
		InboxPath:    "/tmp/in",
		OutboxPath:   "/tmp/out",
		ManifestPath: "/tmp/manifest.json",
		Tier:         "formatted",
	}
}

func envValues(environment []string, key string) []string {
	prefix := key + "="
	var values []string
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			values = append(values, strings.TrimPrefix(entry, prefix))
		}
	}
	return values
}

func requireUniqueEnv(t *testing.T, environment []string, key, want string) {
	t.Helper()
	values := envValues(environment, key)
	if len(values) != 1 || values[0] != want {
		t.Fatalf("%s = %q, want exactly [%q]", key, values, want)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestAgentCommandsSelectDearMachineConfiguration(t *testing.T) {
	fixture := newAgentTestFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MACHTIANI_CONFIG", filepath.Join(home, "personal.toml"))
	seen := 0
	fixture.runner.invoke = func(command *exec.Cmd) error {
		expected := "MACHTIANI_CONFIG=" + filepath.Join(home, ".config/dearmachine/machtiani/config.toml")
		if !slices.Contains(command.Env, expected) {
			t.Errorf("child did not select DearMachine config: %s", command.Args[1])
		}
		seen++
		return command.Run()
	}
	if err := fixture.runner.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.runner.Run(context.Background(), fixture.session, "prompt", fixture.finalPath, fixture.turn); err != nil {
		t.Fatal(err)
	}
	if seen < 3 {
		t.Fatal("expected sync, run, and session inspection")
	}
}

func TestSyncPassesEffectiveModelExplicitly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(home, ".config/dearmachine/machtiani/config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("default_model = \"planner\"\nanswer_model = \"sync-selected\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runner, err := NewAgentRunner("machtiani", t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, oneRun := range []string{"", "cli-override"} {
		runner.model = oneRun
		want := "sync-selected"
		if oneRun != "" {
			want = oneRun
		}
		runner.invoke = func(command *exec.Cmd) error {
			expected := []string{"machtiani", "sync", "--model", want, "--answer-model", want, "--file-discovery-model", want}
			if !slices.Equal(command.Args, expected) {
				t.Fatalf("sync args = %q", command.Args)
			}
			return nil
		}
		if err := runner.Sync(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}
