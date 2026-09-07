package agentmanager

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const claudeAuthFailure = `{"type":"system","subtype":"init","cwd":"/project","session_id":"auth-session","apiKeySource":"none","permissionMode":"bypassPermissions"}
{"type":"assistant","session_id":"auth-session","error":"authentication_failed","message":{"role":"assistant","content":[{"type":"text","text":"Not logged in · Please run /login"}]}}
{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login","session_id":"auth-session","num_turns":1,"total_cost_usd":0}`

func TestClaudeStdout(t *testing.T) {
	for _, test := range []struct {
		name, stream, reply, session, failure string
	}{
		{
			name: "success uses terminal reply",
			stream: `not json
{"type":"system","subtype":"init","session_id":"session-123","cwd":"/project","model":"sonnet"}
{"type":"assistant","session_id":"session-123","message":{"role":"assistant","content":[{"type":"text","text":"Working..."}]}}
{"type":"result","subtype":"success","is_error":false,"result":"Done.\nAll checks passed.","session_id":"session-123","num_turns":1,"usage":{"input_tokens":20,"output_tokens":10}}
{"type":"result","subtype":"success","result":"ignored"}`,
			reply: "Done.\nAll checks passed.", session: "session-123",
		},
		{
			name: "multiple assistant blocks retained on truncated stream",
			stream: `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello "},{"type":"tool_use","id":"tool-1","name":"Read","input":{"file_path":"/project/a"}},{"type":"text","text":"world"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"!"}]}}`,
			reply: "Hello world!", failure: "without a result",
		},
		{name: "auth failure", stream: claudeAuthFailure, reply: "Not logged in · Please run /login", session: "auth-session", failure: "is_error=true"},
		{name: "error subtype", stream: `{"type":"result","subtype":"error_max_turns","is_error":false,"result":"Turn limit"}`, reply: "Turn limit", failure: "error_max_turns"},
		{name: "session from assistant", stream: `{"type":"assistant","session_id":"assistant-session","message":{"content":[]}}
{"type":"result","subtype":"success","result":"OK","session_id":"later-session"}`, reply: "OK", session: "assistant-session"},
		{name: "session from unknown event", stream: `{"type":"future","session_id":"future-session"}
{"type":"result","subtype":"success","result":"OK"}`, reply: "OK", session: "future-session"},
		{name: "session from result", stream: `{"type":"result","subtype":"success","result":"OK","session_id":"result-session"}`, reply: "OK", session: "result-session"},
		{name: "empty stream", failure: "without a result"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var sessions []string
			got, err := consumeClaudeStdout(strings.NewReader(test.stream), func(id string) { sessions = append(sessions, id) })
			if got.Reply != test.reply {
				t.Fatalf("reply = %q, want %q", got.Reply, test.reply)
			}
			if test.failure == "" && err != nil || test.failure != "" && (err == nil || !strings.Contains(err.Error(), test.failure)) {
				t.Fatalf("error = %v, want %q", err, test.failure)
			}
			if test.session == "" && len(sessions) != 0 || test.session != "" && !reflect.DeepEqual(sessions, []string{test.session}) {
				t.Fatalf("session callbacks = %v, want first session %q only", sessions, test.session)
			}
		})
	}
}

func TestClaudePrepare(t *testing.T) {
	for _, test := range []struct{ name, configured, anthropic, want string }{
		{"default", "", "", "sonnet"},
		{"native environment", "", "native-model", "native-model"},
		{"override verbatim", "z-ai/glm-5.3-flash", "native-model", "z-ai/glm-5.3-flash"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DEARMACHINE_CLAUDE_MODEL", test.configured)
			t.Setenv("ANTHROPIC_MODEL", test.anthropic)
			launch, err := (ClaudeAdapter{}).Prepare(context.Background(), "/project", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--model", test.want, "--permission-mode", "bypassPermissions"}
			if !reflect.DeepEqual(launch.Command.Args, want) || launch.Command.Dir != "/project" || launch.PromptArgument || launch.Command.Env != nil {
				t.Fatalf("unexpected launch: %+v, command: %+v", launch, launch.Command)
			}
			configureLaunchInput(&launch, []byte("ticket request"))
			input, err := io.ReadAll(launch.Command.Stdin)
			if err != nil || string(input) != "ticket request" {
				t.Fatalf("stdin = %q, %v", input, err)
			}
		})
	}
}

func TestClaudePrepareResumesTicketSession(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"worker":"claude","native_session_id":"prior-session"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	launch, err := (ClaudeAdapter{}).Prepare(context.Background(), "/project", dir)
	if err != nil {
		t.Fatal(err)
	}
	args := launch.Command.Args
	if !reflect.DeepEqual(args[len(args)-2:], []string{"--resume", "prior-session"}) || launch.NativeSession != "prior-session" {
		t.Fatalf("resume launch = %+v, args = %v", launch, args)
	}
	// A health probe passes cwd as writableDir; an unrelated project meta.json
	// must not cause it to resume a conversation.
	launch, err = (ClaudeAdapter{}).Prepare(context.Background(), dir, dir)
	if err != nil || launch.NativeSession != "" {
		t.Fatalf("health probe launch = %+v, %v", launch, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (ClaudeAdapter{}).Prepare(context.Background(), "/project", dir); err == nil {
		t.Fatal("expected invalid ticket metadata to fail")
	}
}

func TestClaudeSuperviseParsesAuthFailureBeforeExit(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\ncat >/dev/null\ncat <<'EVENTS'\n" + claudeAuthFailure + "\nEVENTS\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	manager := New(filepath.Join(t.TempDir(), "agent-manager"))
	manager.ApprovedBackends = []string{"claude"}
	manager.LaunchSupervisor = func(string) error { return nil }
	id, err := manager.Send("claude", writeRequest(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = manager.Supervise(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("supervise error = %v", err)
	}
	meta, err := manager.Status(id)
	if err != nil || meta.Status != StatusFailed || meta.NativeSession != "auth-session" || meta.ExitCode == nil || *meta.ExitCode != 1 {
		t.Fatalf("metadata = %+v, %v", meta, err)
	}
	if _, err := os.Stat(filepath.Join(manager.TicketDir(id), "ticket-close.md")); !os.IsNotExist(err) {
		t.Fatalf("auth failure published a reply: %v", err)
	}
}
