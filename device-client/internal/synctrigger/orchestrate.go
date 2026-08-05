package synctrigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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
	StatePath          string
	Lister             SessionLister
	GitLastCommitTime  GitLastCommitTime
	RunCommand         CommandRunner
	Logger             *log.Logger
}

type reviewCheckpoint struct {
	SessionID string    `json:"session_id"`
	UpdatedAt time.Time `json:"updated_at"`
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
	checkpointPath := o.StatePath
	if strings.TrimSpace(checkpointPath) == "" {
		checkpointPath = filepath.Join(o.RepoPath, "state", "sync-trigger.json")
	}
	checkpoint, err := loadReviewCheckpoint(checkpointPath)
	if err != nil {
		return fmt.Errorf("load sync-trigger checkpoint: %w", err)
	}
	effectiveBoundary := func(repoPath string) (time.Time, error) {
		syncTime, err := gitLastCommitTime(repoPath)
		if err != nil {
			return time.Time{}, err
		}
		if checkpoint.UpdatedAt.After(syncTime) {
			return checkpoint.UpdatedAt, nil
		}
		return syncTime, nil
	}

	detected, err := DetectSessions(ctx, o.RepoPath, lister, effectiveBoundary)
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
		runErr := fmt.Errorf(
			"run forked session %s: %w: %s",
			forkedSessionID,
			err,
			strings.TrimSpace(string(runOutput)),
		)
		deleteOutput, deleteErr := runCommand(
			ctx,
			o.RepoPath,
			o.MCTBinary,
			"session",
			"delete",
			forkedSessionID,
		)
		if deleteErr != nil {
			return errors.Join(
				runErr,
				fmt.Errorf(
					"delete failed fork %s: %w: %s",
					forkedSessionID,
					deleteErr,
					strings.TrimSpace(string(deleteOutput)),
				),
			)
		}
		return runErr
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
	latest := detected.NewSessions[0]
	if err := saveReviewCheckpoint(checkpointPath, reviewCheckpoint{
		SessionID: latest.SessionID,
		UpdatedAt: latest.UpdatedAt,
	}); err != nil {
		return fmt.Errorf("save sync-trigger checkpoint: %w", err)
	}
	if o.Logger != nil {
		o.Logger.Printf("sync trigger: orchestration complete for forked session %s", forkedSessionID)
	}
	return nil
}

func loadReviewCheckpoint(path string) (reviewCheckpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return reviewCheckpoint{}, nil
		}
		return reviewCheckpoint{}, err
	}
	var checkpoint reviewCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return reviewCheckpoint{}, err
	}
	return checkpoint, nil
}

func saveReviewCheckpoint(path string, checkpoint reviewCheckpoint) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".sync-trigger-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
