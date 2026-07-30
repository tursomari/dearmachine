package deviceclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type ResultKind string

const (
	ResultAnswer   ResultKind = "answer"
	ResultQuestion ResultKind = "question"
)

type RunResult struct {
	Kind ResultKind
	Text string
}

type MCTRunner struct {
	binary     string
	projectDir string
	model      string
}

type sessionState struct {
	Status             string              `json:"status"`
	SuspendedUserInput *suspendedUserInput `json:"suspended_user_input"`
}

type suspendedUserInput struct {
	Question string `json:"question"`
	Context  string `json:"context"`
}

func NewMCTRunner(binary, projectDir, model string) (*MCTRunner, error) {
	if strings.TrimSpace(binary) == "" {
		return nil, fmt.Errorf("mct-agent binary is required")
	}
	if strings.TrimSpace(projectDir) == "" {
		return nil, fmt.Errorf("mct project directory is required")
	}
	return &MCTRunner{
		binary:     binary,
		projectDir: projectDir,
		model:      strings.TrimSpace(model),
	}, nil
}

func (r *MCTRunner) Sync(ctx context.Context) error {
	command := exec.CommandContext(ctx, r.binary, "sync")
	command.Dir = r.projectDir
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return fmt.Errorf(
			"mct-agent sync failed: %w: %s",
			err,
			strings.TrimSpace(output.String()),
		)
	}
	return nil
}

func (r *MCTRunner) Run(ctx context.Context, session Session, text string) (RunResult, error) {
	if err := r.Sync(ctx); err != nil {
		return RunResult{}, err
	}

	outputDir, err := os.MkdirTemp("", "device-client-mct-")
	if err != nil {
		return RunResult{}, fmt.Errorf("create mct output directory: %w", err)
	}
	defer os.RemoveAll(outputDir)

	finalPath := filepath.Join(outputDir, "agent-final-answer.md")
	args := []string{
		"run",
		"--text", text,
	}
	if !session.IsNew {
		args = append(args, "--session-id", session.SessionID)
	}
	if r.model != "" {
		args = append(args, "--model", r.model)
	}
	args = append(
		args,
		"--no-banner",
		"--no-cursor",
		"--final-file", finalPath,
	)

	command := exec.CommandContext(ctx, r.binary, args...)
	command.Dir = r.projectDir
	command.Env = os.Environ()
	if session.IsNew {
		command.Env = setEnv(command.Env, "MACHTIANI_SESSION_ID", session.SessionID)
	}
	var runOutput bytes.Buffer
	command.Stdout = &runOutput
	command.Stderr = &runOutput
	if err := command.Run(); err != nil {
		return RunResult{}, fmt.Errorf("mct-agent run failed: %w: %s", err, strings.TrimSpace(runOutput.String()))
	}

	state, err := r.showSession(ctx, session.SessionID)
	if err != nil {
		return RunResult{}, err
	}

	switch state.Status {
	case "success":
		answer, err := os.ReadFile(finalPath)
		if err != nil {
			return RunResult{}, fmt.Errorf("read mct final answer: %w", err)
		}
		text := strings.TrimSpace(string(answer))
		if text == "" {
			return RunResult{}, fmt.Errorf("mct-agent reported success with an empty final answer")
		}
		return RunResult{Kind: ResultAnswer, Text: text}, nil
	case "suspended_user_input":
		if state.SuspendedUserInput == nil ||
			strings.TrimSpace(state.SuspendedUserInput.Question) == "" {
			return RunResult{}, fmt.Errorf("mct-agent suspended without a question")
		}
		return RunResult{
			Kind: ResultQuestion,
			Text: clarificationText(
				state.SuspendedUserInput.Question,
				state.SuspendedUserInput.Context,
			),
		}, nil
	default:
		return RunResult{}, fmt.Errorf("mct-agent returned unsupported status %q", state.Status)
	}
}

func (r *MCTRunner) showSession(ctx context.Context, sessionID string) (sessionState, error) {
	command := exec.CommandContext(
		ctx,
		r.binary,
		"session", "show", sessionID, "--json",
	)
	command.Dir = r.projectDir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return sessionState{}, fmt.Errorf(
			"mct-agent session show failed: %w: %s",
			err,
			strings.TrimSpace(stderr.String()),
		)
	}

	var state sessionState
	if err := json.Unmarshal(stdout.Bytes(), &state); err != nil {
		return sessionState{}, fmt.Errorf("parse mct-agent session status: %w", err)
	}
	return state, nil
}

func clarificationText(question, context string) string {
	var body strings.Builder
	body.WriteString("Your computer has a question about your request:\n\n")
	body.WriteString(strings.TrimSpace(question))
	if strings.TrimSpace(context) != "" {
		body.WriteString("\n\n")
		body.WriteString(strings.TrimSpace(context))
	}
	body.WriteString("\n\nReply to this email to answer.")
	return body.String()
}

func setEnv(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}
