package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackendListPrintsApprovedPriorityOrderOnly(t *testing.T) {
	t.Setenv("DEARMACHINE_HOME", t.TempDir())
	t.Setenv("DEARMACHINE_BACKENDS", `["forgecode","codex"]`)
	var output strings.Builder
	if err := run([]string{"backend", "list"}, &output, io.Discard); err != nil {
		t.Fatalf("run backend list: %v", err)
	}
	if got := output.String(); got != "forgecode\ncodex\n" {
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
	t.Setenv("DEARMACHINE_BACKENDS", `["forgecode"]`)
	t.Setenv("PATH", t.TempDir())
	var output strings.Builder
	err := run([]string{"backend", "health", "forgecode"}, &output, io.Discard)
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
			args: []string{"ticket", "send", "--backend", "forgecode", "--file", request, "--cwd", cwd},
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
