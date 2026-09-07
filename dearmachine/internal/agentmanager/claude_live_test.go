//go:build claude_live

package agentmanager

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestClaudeLive makes exactly one CLI invocation. The key file is read only
// when explicitly enabled; credentials are added to the child environment,
// never to the test process environment or test output.
func TestClaudeLive(t *testing.T) {
	keyPath := os.Getenv("DEARMACHINE_CLAUDE_LIVE_KEY_FILE")
	if keyPath == "" {
		t.Skip("set DEARMACHINE_CLAUDE_LIVE_KEY_FILE to opt into one OpenRouter call")
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal("cannot read live credential file")
	}
	token := strings.TrimSpace(string(key))
	if token == "" {
		t.Fatal("empty live credential file")
	}
	t.Setenv("DEARMACHINE_CLAUDE_MODEL", "z-ai/glm-5.3-flash")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	adapter := ClaudeAdapter{}
	launch, err := adapter.Prepare(ctx, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal("prepare live Claude command failed")
	}
	configureLaunchInput(&launch, []byte("Reply with exactly CLAUDE_ADAPTER_OK. Do not use any tools."))
	command := launch.Command
	command.Args = append(command.Args, "--max-turns", "1")
	// Isolate CLI settings/auth from the caller, including any primary API key
	// or nested-Claude marker. Only this child's environment receives the token.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "CLAUDE") || name == "HOME" || name == "XDG_CONFIG_HOME" || name == "DEARMACHINE_CLAUDE_LIVE_KEY_FILE" {
			continue
		}
		command.Env = append(command.Env, entry)
	}
	command.Env = append(command.Env,
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"ANTHROPIC_AUTH_TOKEN="+token,
		"ANTHROPIC_BASE_URL=https://openrouter.ai/api",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal("create live stdout pipe failed")
	}
	// Never print raw CLI output: provider diagnostics could contain credentials.
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatal("start live Claude command failed")
	}
	sessionFound := false
	observation, parseErr := adapter.ConsumeStdout(stdout, func(string) { sessionFound = true })
	_, drainErr := io.Copy(io.Discard, stdout)
	waitErr := command.Wait()
	if parseErr != nil || drainErr != nil || waitErr != nil {
		t.Fatalf("live command failed: parser_failed=%t drain_failed=%t exit_code=%d timed_out=%t session_captured=%t", parseErr != nil, drainErr != nil, command.ProcessState.ExitCode(), ctx.Err() != nil, sessionFound)
	}
	if !sessionFound || strings.TrimSpace(observation.Reply) != "CLAUDE_ADAPTER_OK" {
		t.Fatalf("live response failed validation: session_captured=%t expected_reply=%t", sessionFound, strings.TrimSpace(observation.Reply) == "CLAUDE_ADAPTER_OK")
	}
	t.Log("OpenRouter z-ai/glm-5.3-flash: exit 0, terminal success, session captured, expected reply received")
}
