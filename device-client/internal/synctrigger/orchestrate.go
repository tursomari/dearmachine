package synctrigger

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// CommandRunner executes a shell command in a directory.
type CommandRunner func(ctx context.Context, dir string, name string, args ...string) ([]byte, error)

// DefaultCommandRunner runs commands through exec.CommandContext with CombinedOutput.
func DefaultCommandRunner(
	ctx context.Context,
	dir string,
	name string,
	args ...string,
) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	return command.CombinedOutput()
}

// Orchestrator coordinates sync-trigger session orchestration.
type Orchestrator struct {
	RepoPath           string
	MCTBinary          string
	PromptTemplatePath string
	Lister             SessionLister
	GitLastCommitTime  GitLastCommitTime
	RunCommand         CommandRunner
	Logger             *log.Logger
}

// OrchestrateSync executes the session fork/run/delete/sync pipeline for
// newly discovered sessions.
func (o *Orchestrator) OrchestrateSync(ctx context.Context) error {
	if o == nil {
		return nil
	}
	if o.Logger != nil {
		o.Logger.Printf("sync trigger: evaluating sessions in %s", o.RepoPath)
	}
	lister := o.Lister
	if lister == nil {
		lister = DefaultSessionLister
	}
	gitLastCommitTime := o.GitLastCommitTime
	if gitLastCommitTime == nil {
		gitLastCommitTime = DefaultGitLastCommitTime
	}
	runCommand := o.RunCommand
	if runCommand == nil {
		runCommand = DefaultCommandRunner
	}

	detected, err := DetectSessions(ctx, o.RepoPath, lister, gitLastCommitTime)
	if err != nil {
		return fmt.Errorf("detect sync sessions: %w", err)
	}
	if detected.ForkSessionID == "" {
		if o.Logger != nil {
			o.Logger.Printf("sync trigger: no sync trigger needed")
		}
		return nil
	}
	if o.Logger != nil {
		o.Logger.Printf("sync trigger: forking session %s", detected.ForkSessionID)
	}

	forkOutput, err := runCommand(ctx, o.RepoPath, o.MCTBinary, "session", "fork", detected.ForkSessionID)
	if err != nil {
		return fmt.Errorf("fork session %s: %w: %s", detected.ForkSessionID, err, strings.TrimSpace(string(forkOutput)))
	}
	forkedSessionID := strings.TrimSpace(string(forkOutput))
	if forkedSessionID == "" {
		return fmt.Errorf("fork session output is empty")
	}

	runOutput, err := runCommand(
		ctx,
		o.RepoPath,
		o.MCTBinary,
		"run",
		"--session-id",
		forkedSessionID,
		"--file",
		o.PromptTemplatePath,
	)
	if err != nil {
		return fmt.Errorf("run forked session %s: %w: %s", forkedSessionID, err, strings.TrimSpace(string(runOutput)))
	}

	deleteOutput, err := runCommand(
		ctx,
		o.RepoPath,
		o.MCTBinary,
		"session",
		"delete",
		forkedSessionID,
	)
	if err != nil {
		return fmt.Errorf("delete forked session %s: %w: %s", forkedSessionID, err, strings.TrimSpace(string(deleteOutput)))
	}

	syncOutput, err := runCommand(ctx, o.RepoPath, o.MCTBinary, "sync", "--include-docs")
	if err != nil {
		return fmt.Errorf("sync: %w: %s", err, strings.TrimSpace(string(syncOutput)))
	}
	if o.Logger != nil {
		o.Logger.Printf("sync trigger: orchestration complete for forked session %s", forkedSessionID)
	}
	return nil
}
