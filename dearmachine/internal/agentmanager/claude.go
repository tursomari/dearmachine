package agentmanager

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ClaudeAdapter uses bypassPermissions so headless workers can complete tool
// calls without approval prompts, like OMP's --auto-approve. It inherits the
// user's host permissions and therefore requires explicit catalog opt-in.
// Credentials come from the child's ambient environment, just like other
// built-ins. Model precedence is DEARMACHINE_CLAUDE_MODEL, ANTHROPIC_MODEL, sonnet.
type ClaudeAdapter struct{}

func (ClaudeAdapter) Name() string       { return "claude" }
func (ClaudeAdapter) Executable() string { return "claude" }
func (ClaudeAdapter) Prepare(ctx context.Context, cwd, writableDir string) (Launch, error) {
	model := os.Getenv("DEARMACHINE_CLAUDE_MODEL")
	if model == "" {
		model = os.Getenv("ANTHROPIC_MODEL")
	}
	if model == "" {
		model = "sonnet"
	}
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--model", model,
		"--permission-mode", "bypassPermissions"}

	// Prepare receives the ticket directory, not a session argument. Reuse only
	// this ticket's persisted session; directory-wide --continue could attach
	// an unrelated ticket to the wrong conversation. Health probes have no meta.
	var meta Meta
	if writableDir != "" && filepath.Clean(writableDir) != filepath.Clean(cwd) {
		content, err := os.ReadFile(filepath.Join(writableDir, "meta.json"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Launch{}, fmt.Errorf("read claude session metadata: %w", err)
		}
		if err == nil {
			if err := json.Unmarshal(content, &meta); err != nil {
				return Launch{}, fmt.Errorf("decode claude session metadata: %w", err)
			}
		}
	}
	if meta.NativeSession != "" {
		args = append(args, "--resume", meta.NativeSession)
	}
	command := exec.CommandContext(ctx, "claude", args...)
	command.Dir = cwd
	return Launch{Command: command, NativeSession: meta.NativeSession}, nil
}

func (ClaudeAdapter) ConsumeStdout(stdout io.Reader, foundSession func(string)) (Observation, error) {
	return consumeClaudeStdout(stdout, foundSession)
}

func consumeClaudeStdout(stdout io.Reader, foundSession func(string)) (Observation, error) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var assistant strings.Builder
	sessionFound := false
	for scanner.Scan() {
		var event struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			SessionID string `json:"session_id"`
			Result    string `json:"result"`
			IsError   bool   `json:"is_error"`
			Message   struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if !sessionFound && event.SessionID != "" {
			sessionFound = true
			if foundSession != nil {
				foundSession(event.SessionID)
			}
		}
		switch event.Type {
		case "assistant":
			for _, block := range event.Message.Content {
				if block.Type == "text" {
					assistant.WriteString(block.Text)
				}
			}
		case "result":
			observation := Observation{Reply: event.Result}
			// Drain after the terminal event so callers can safely Wait even if
			// the CLI writes trailing output. No later event can change the reply.
			_, drainErr := io.Copy(io.Discard, stdout)
			if event.IsError || event.Subtype != "success" {
				return observation, errors.Join(fmt.Errorf("claude result failed (subtype=%q, is_error=%t): %s", event.Subtype, event.IsError, event.Result), drainErr)
			}
			return observation, drainErr
		}
	}
	observation := Observation{Reply: assistant.String()}
	if scanErr := scanner.Err(); scanErr != nil {
		_, drainErr := io.Copy(io.Discard, stdout)
		return observation, errors.Join(scanErr, drainErr)
	}
	// Assistant text can be progress or a tool preamble. Without a terminal
	// result it is useful diagnostic output, but does not prove completion.
	return observation, errors.New("claude stream ended without a result event")
}
