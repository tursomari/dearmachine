package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelpMenusAtEveryCommandLevel(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "root flag",
			args: []string{"--help"},
			want: []string{"Usage:\n  agent-manager <command>", "backend", "ticket", "worker"},
		},
		{
			name: "root command",
			args: []string{"help"},
			want: []string{"Usage:\n  agent-manager <command>"},
		},
		{
			name: "backend group",
			args: []string{"backend", "--help"},
			want: []string{"agent-manager backend <command>", "health <name>"},
		},
		{
			name: "backend subcommand flag",
			args: []string{"backend", "health", "--help"},
			want: []string{"agent-manager backend health <name>", "transient file-write probe"},
		},
		{
			name: "backend subcommand command",
			args: []string{"backend", "help", "health"},
			want: []string{"agent-manager backend health <name>"},
		},
		{
			name: "hierarchical help command",
			args: []string{"help", "ticket", "send"},
			want: []string{"agent-manager ticket send --backend <name>", "--file <path>", "--cwd <project-dir>"},
		},
		{
			name: "ticket subcommand flag",
			args: []string{"ticket", "status", "--help"},
			want: []string{"agent-manager ticket status <ticket-id>"},
		},
		{
			name: "worker group",
			args: []string{"worker", "help"},
			want: []string{"agent-manager worker <name> status"},
		},
		{
			name: "worker status topic",
			args: []string{"help", "worker", "status"},
			want: []string{"agent-manager worker <name> status"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output strings.Builder
			if err := run(test.args, &output, io.Discard); err != nil {
				t.Fatalf("run help: %v", err)
			}
			for _, want := range test.want {
				if !strings.Contains(output.String(), want) {
					t.Errorf("help output missing %q:\n%s", want, output.String())
				}
			}
		})
	}
}

func TestSyntaxErrorsPointToExactHelpCommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing root command", want: `Run "agent-manager --help" for usage.`},
		{name: "unknown root command", args: []string{"bogus"}, want: `Run "agent-manager --help" for usage.`},
		{name: "missing backend command", args: []string{"backend"}, want: `Run "agent-manager backend --help" for usage.`},
		{name: "unknown backend command", args: []string{"backend", "bogus"}, want: `Run "agent-manager backend --help" for usage.`},
		{name: "backend list arguments", args: []string{"backend", "list", "extra"}, want: `Run "agent-manager backend list --help" for usage.`},
		{name: "missing health backend", args: []string{"backend", "health"}, want: `Run "agent-manager backend health --help" for usage.`},
		{name: "missing ticket command", args: []string{"ticket"}, want: `Run "agent-manager ticket --help" for usage.`},
		{name: "unknown ticket command", args: []string{"ticket", "bogus"}, want: `Run "agent-manager ticket --help" for usage.`},
		{name: "unknown nested help topic", args: []string{"help", "ticket", "bogus"}, want: `Run "agent-manager ticket --help" for usage.`},
		{name: "missing send flags", args: []string{"ticket", "send"}, want: `Run "agent-manager ticket send --help" for usage.`},
		{name: "unknown send flag", args: []string{"ticket", "send", "--bogus"}, want: `Run "agent-manager ticket send --help" for usage.`},
		{name: "missing ticket id", args: []string{"ticket", "view"}, want: `Run "agent-manager ticket view --help" for usage.`},
		{name: "invalid worker syntax", args: []string{"worker", "codex"}, want: `Run "agent-manager worker --help" for usage.`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := run(test.args, io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestBackendHealthHelpDoesNotRequireConfigurationOrRunProbe(t *testing.T) {
	t.Setenv("DEARMACHINE_BACKENDS", "")
	t.Setenv("PATH", t.TempDir())
	var output strings.Builder
	if err := run([]string{"backend", "health", "--help"}, &output, io.Discard); err != nil {
		t.Fatalf("run backend health help: %v", err)
	}
	if !strings.Contains(output.String(), "agent-manager backend health <name>") {
		t.Fatalf("health help output = %q", output.String())
	}
}

func TestBackendListPrintsApprovedPriorityOrderOnly(t *testing.T) {
	t.Setenv("DEARMACHINE_HOME", t.TempDir())
	t.Setenv("DEARMACHINE_BACKENDS", `["forge","codex"]`)
	var output strings.Builder
	if err := run([]string{"backend", "list"}, &output, io.Discard); err != nil {
		t.Fatalf("run backend list: %v", err)
	}
	if got := output.String(); got != "forge\ncodex\n" {
		t.Fatalf("backend list output = %q", got)
	}
}

func TestBackendHealthReportsFileProbeAndCleansUp(t *testing.T) {
	t.Setenv("DEARMACHINE_HOME", t.TempDir())
	t.Setenv("DEARMACHINE_BACKENDS", `["codex"]`)
	bin := t.TempDir()
	executable := filepath.Join(bin, "codex")
	script := `#!/bin/sh
set -eu
prompt=$(cat)
path=${prompt#*exactly }
path=${path%% containing exactly*}
printf '%s\n' 'DearMachine backend health probe.' > "$path"
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"probe complete"}}'
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	previousCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousCWD) })

	var output strings.Builder
	if err := run([]string{"backend", "health", "codex"}, &output, io.Discard); err != nil {
		t.Fatalf("run backend health: %v\n%s", err, output.String())
	}
	for _, want := range []string{"backend=codex", `reply="probe complete"`, "result=ok"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("health output missing %q:\n%s", want, output.String())
		}
	}
	matches, err := filepath.Glob(filepath.Join(project, ".healthcheck-probe-*.txt"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("health probe artifacts = %v, %v", matches, err)
	}
}

func TestBackendHealthReportsFailureAndReturnsError(t *testing.T) {
	t.Setenv("DEARMACHINE_HOME", t.TempDir())
	t.Setenv("DEARMACHINE_BACKENDS", `["forge"]`)
	t.Setenv("PATH", t.TempDir())
	var output strings.Builder
	err := run([]string{"backend", "health", "forge"}, &output, io.Discard)
	if err == nil || !strings.Contains(output.String(), "result=fail reason=unavailable") {
		t.Fatalf("run error = %v, output = %q", err, output.String())
	}
}

func TestTicketSendRequiresBackendFlagAndApproval(t *testing.T) {
	t.Setenv("DEARMACHINE_HOME", t.TempDir())
	t.Setenv("DEARMACHINE_BACKENDS", `["codex"]`)
	request := filepath.Join(t.TempDir(), "request.md")
	if err := os.WriteFile(request, []byte("test request\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing backend flag",
			args: []string{"ticket", "send", "--file", request, "--cwd", cwd},
			want: "--backend <name>",
		},
		{
			name: "retired positional backend",
			args: []string{"ticket", "send", "codex", "--file", request, "--cwd", cwd},
			want: "--backend <name>",
		},
		{
			name: "unapproved backend",
			args: []string{"ticket", "send", "--backend", "forge", "--file", request, "--cwd", cwd},
			want: "not approved",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := run(test.args, io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestTicketSendBackendFlagReachesAvailabilityCheck(t *testing.T) {
	t.Setenv("DEARMACHINE_HOME", t.TempDir())
	t.Setenv("DEARMACHINE_BACKENDS", `["codex"]`)
	t.Setenv("PATH", t.TempDir())
	request := filepath.Join(t.TempDir(), "request.md")
	if err := os.WriteFile(request, []byte("test request\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run(
		[]string{"ticket", "send", "--backend", "codex", "--file", request, "--cwd", t.TempDir()},
		io.Discard,
		io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("run error = %v", err)
	}
}
