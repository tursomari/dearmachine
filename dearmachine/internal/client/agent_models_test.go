package client

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func writeRunModelConfig(t *testing.T, home, planner, answer, discovery, shell string) {
	t.Helper()
	path := filepath.Join(home, ".config/dearmachine/machtiani/config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, "default_model = \""+planner+"\"\nanswer_model = \""+answer+"\"\nfile_discovery_model = \""+discovery+"\"\nshell_agent_model = \""+shell+"\"\n")
}

func TestFollowUpUsesCurrentModelsWithoutRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	rig := newTestRigWithModel(t, "")
	writeRunModelConfig(t, home, "old-planner", "old-answer", "old-discovery", "old-shell")
	rig.mail.add(testMessage("msg-001", "thread-001", "Start the task."))
	rig.setAnswer("Started.")
	mustProcess(t, rig)
	original := rig.session("thread-001")
	assertArg(t, rig.captureLines("args-1"), "--model", "old-planner")

	writeRunModelConfig(t, home, "current-planner", "current-answer", "current-discovery", "current-shell")
	followUp := testMessage("msg-002", "thread-001", "Continue the task.")
	followUp.InReplyTo = "reply-001"
	rig.mail.add(followUp)
	rig.setAnswer("Continued.")
	mustProcess(t, rig)
	args := rig.captureLines("args-2")
	assertArg(t, args, "--resume", original.SessionID)
	for flag, alias := range map[string]string{
		"--model": "current-planner", "--answer-model": "current-answer",
		"--file-discovery-model": "current-discovery", "--shell-agent-model": "current-shell",
	} {
		assertArg(t, args, flag, alias)
	}
	if got := rig.session("thread-001"); got.SessionID != original.SessionID || got.Sequence != 2 {
		t.Fatalf("follow-up lost conversation continuity: %+v", got)
	}
}

func TestResumeWithoutPromptPassesCurrentRoleModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeRunModelConfig(t, home, "planner", "answer", "discovery", "shell")
	fixture := newAgentTestFixture(t)
	fixture.session.IsNew = false
	fixture.runner.model = "planner-override"
	var args []string
	fixture.runner.invoke = func(command *exec.Cmd) error {
		if command.Args[1] == "run" {
			args = append([]string(nil), command.Args...)
		}
		return command.Run()
	}
	if _, err := fixture.runner.RunResume(context.Background(), fixture.session, fixture.finalPath, fixture.turn); err != nil {
		t.Fatal(err)
	}
	assertArg(t, args, "--resume", fixture.session.SessionID)
	for flag, alias := range map[string]string{
		"--model": "planner-override", "--answer-model": "answer",
		"--file-discovery-model": "discovery", "--shell-agent-model": "shell",
	} {
		assertArg(t, args, flag, alias)
	}
}
