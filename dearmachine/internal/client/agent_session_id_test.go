package client

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestAgentRunnerNewSessionCarriesFullConversationReference(t *testing.T) {
	fixture := newAgentTestFixture(t)

	if _, err := fixture.runner.Run(
		context.Background(),
		fixture.session,
		"prompt",
		fixture.finalPath,
		fixture.turn,
	); err != nil {
		t.Fatal(err)
	}

	if got := agentCapture(t, "session-env-1"); got != fixture.session.SessionID {
		t.Fatalf("MACHTIANI_SESSION_ID = %q, want %q", got, fixture.session.SessionID)
	}
	if args := agentCaptureLines(t, "args-1"); slices.Contains(args, "--session-id") {
		t.Fatalf("new session unexpectedly passed --session-id: %v", args)
	}
}

func TestAgentRunnerNewSessionOmitsResume(t *testing.T) {
	fixture := newAgentTestFixture(t)

	if _, err := fixture.runner.Run(
		context.Background(),
		fixture.session,
		"prompt",
		fixture.finalPath,
		fixture.turn,
	); err != nil {
		t.Fatal(err)
	}

	args := agentCaptureLines(t, "args-1")
	for _, flag := range []string{"--session-id", "--resume"} {
		if slices.Contains(args, flag) {
			t.Fatalf("new session unexpectedly passed %s: %v", flag, args)
		}
	}
}

func TestAgentRunnerContinuationPassesFullConversationReferenceAndUnsetsEnv(t *testing.T) {
	fixture := newAgentTestFixture(t)
	fixture.session.IsNew = false

	if _, err := fixture.runner.Run(
		context.Background(),
		fixture.session,
		"prompt",
		fixture.finalPath,
		fixture.turn,
	); err != nil {
		t.Fatal(err)
	}

	assertArg(
		t,
		agentCaptureLines(t, "args-1"),
		"--resume",
		fixture.session.SessionID,
	)
	if got := agentCapture(t, "session-env-1"); got != "" {
		t.Fatalf("continuation set MACHTIANI_SESSION_ID = %q", got)
	}
}

func TestAgentRunnerContinuationSessionIDStableAcrossTurns(t *testing.T) {
	fixture := newAgentTestFixture(t)
	fixture.session.IsNew = false

	for turn := 1; turn <= 2; turn++ {
		if _, err := fixture.runner.Run(
			context.Background(),
			fixture.session,
			"prompt",
			fixture.finalPath,
			fixture.turn,
		); err != nil {
			t.Fatalf("Run turn %d: %v", turn, err)
		}
		assertArg(
			t,
			agentCaptureLines(t, "args-"+strconv.Itoa(turn)),
			"--resume",
			fixture.session.SessionID,
		)
	}
}

func TestAgentRunnerRecoveryContinuesUsingResolvedMachtianiSessionID(t *testing.T) {
	fixture := newAgentTestFixture(t)
	fixture.session.IsNew = false
	writeTestFile(t, fixture.statusFile, `{"status":"in_progress","goal":"original prompt"}`)
	var shown []string
	fixture.runner.invoke = func(command *exec.Cmd) error {
		if len(command.Args) > 3 && command.Args[1] == "session" && command.Args[2] == "show" {
			shown = append(shown, command.Args[3])
		}
		if len(command.Args) > 1 && command.Args[1] == "run" {
			writeTestFile(t, fixture.statusFile, `{"status":"success"}`)
		}
		return command.Run()
	}

	result, err := fixture.runner.Recover(
		context.Background(),
		fixture.session,
		"original prompt",
		fixture.finalPath,
		fixture.turn,
	)
	if err != nil || result.Kind != ResultAnswer || result.Text != "test answer" {
		t.Fatalf("Recover = %+v, %v", result, err)
	}
	assertArg(
		t,
		agentCaptureLines(t, "args-1"),
		"--resume",
		fixture.session.SessionID,
	)
	args := agentCaptureLines(t, "args-1")
	if slices.Contains(args, "--prompt") {
		t.Fatalf("recovery run unexpectedly passed --prompt: %v", args)
	}
	if got := agentCapture(t, "text-1"); got != "" {
		t.Fatalf("recovery prompt = %q, want empty", got)
	}
	if got := agentCapture(t, "session-env-1"); got != "" {
		t.Fatalf("recovery run set MACHTIANI_SESSION_ID = %q", got)
	}
	if len(shown) != 2 {
		t.Fatalf("session show calls = %v, want only recovery and result status reads", shown)
	}
	for _, sessionID := range shown {
		if sessionID != fixture.session.SessionID {
			t.Fatalf("session show IDs = %v, want only %q", shown, fixture.session.SessionID)
		}
	}
}

func TestAgentRunnerRecoveryResumesPromptless(t *testing.T) {
	fixture := newAgentTestFixture(t)
	fixture.session.IsNew = false
	writeTestFile(t, fixture.statusFile, `{"status":"in_progress","goal":"original prompt"}`)
	fixture.runner.invoke = func(command *exec.Cmd) error {
		if len(command.Args) > 1 && command.Args[1] == "run" {
			writeTestFile(t, fixture.statusFile, `{"status":"success"}`)
		}
		return command.Run()
	}

	if _, err := fixture.runner.Recover(
		context.Background(),
		fixture.session,
		"original prompt",
		fixture.finalPath,
		fixture.turn,
	); err != nil {
		t.Fatal(err)
	}

	if got := agentCapture(t, "count"); got != "1" {
		t.Fatalf("machtiani run count = %q, want 1", got)
	}
	args := agentCaptureLines(t, "args-1")
	assertArg(t, args, "--resume", fixture.session.SessionID)
	for _, flag := range []string{"--prompt", "--file"} {
		if slices.Contains(args, flag) {
			t.Fatalf("promptless recovery unexpectedly passed %s: %v", flag, args)
		}
	}
	if strings.Contains(strings.Join(args, " "), "recovery:") {
		t.Fatalf("promptless recovery args contain synthetic recovery text: %v", args)
	}
	if got := agentCapture(t, "text-1"); got != "" {
		t.Fatalf("recovery prompt = %q, want empty", got)
	}
}

func TestAgentRunnerRecoveryGoalMismatchStillUsesPrompt(t *testing.T) {
	fixture := newAgentTestFixture(t)
	fixture.session.IsNew = false
	writeTestFile(t, fixture.statusFile, `{"status":"in_progress","goal":"different goal"}`)
	fixture.runner.invoke = func(command *exec.Cmd) error {
		if len(command.Args) > 1 && command.Args[1] == "run" {
			writeTestFile(t, fixture.statusFile, `{"status":"success"}`)
		}
		return command.Run()
	}

	if _, err := fixture.runner.Recover(
		context.Background(),
		fixture.session,
		"original prompt",
		fixture.finalPath,
		fixture.turn,
	); err != nil {
		t.Fatal(err)
	}

	args := agentCaptureLines(t, "args-1")
	if !slices.Contains(args, "--prompt") {
		t.Fatalf("recovery args = %v, want --prompt", args)
	}
	if got := agentCapture(t, "text-1"); got != "original prompt" {
		t.Fatalf("recovery prompt = %q, want %q", got, "original prompt")
	}
	assertArg(t, args, "--resume", fixture.session.SessionID)
}

func agentCapture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(os.Getenv("FAKE_AGENT_CAPTURE"), name))
	if err != nil {
		t.Fatalf("read capture %s: %v", name, err)
	}
	return string(content)
}

func agentCaptureLines(t *testing.T, name string) []string {
	t.Helper()
	return strings.Split(strings.TrimSpace(agentCapture(t, name)), "\n")
}
