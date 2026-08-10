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

	backendcatalog "github.com/dearmachine/dearmachine/internal/backends"
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

func agentManagedCommandRunner(managerPath string, backends []string) (CommandRunner, error) {
	managerPath = strings.TrimSpace(managerPath)
	if managerPath == "" || !filepath.IsAbs(managerPath) {
		return nil, fmt.Errorf("absolute agent-manager path is required")
	}
	encodedBackends, err := backendcatalog.Encode(backends)
	if err != nil {
		return nil, fmt.Errorf("encode configured agent backends: %w", err)
	}
	environment := managedEnvironment(os.Environ(), managerPath, encodedBackends)
	return func(
		ctx context.Context,
		dir string,
		name string,
		args ...string,
	) ([]byte, error) {
		command := exec.CommandContext(ctx, name, args...)
		command.Dir = dir
		command.Env = environment
		return command.CombinedOutput()
	}, nil
}

func managedEnvironment(environment []string, managerPath, encodedBackends string) []string {
	blocked := map[string]struct{}{
		"AGENT_MANAGER_PATH":               {},
		"DEARMACHINE_BACKEND":              {},
		backendcatalog.EnvironmentVariable: {},
		"MACHTIANI_SESSION_ID":             {},
	}
	filtered := make([]string, 0, len(environment)+2)
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if _, remove := blocked[key]; found && remove {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(
		filtered,
		"AGENT_MANAGER_PATH="+managerPath,
		backendcatalog.EnvironmentVariable+"="+encodedBackends,
	)
}

// Orchestrator coordinates sync-trigger session orchestration.
type Orchestrator struct {
	RepoPath            string
	MCTBinary           string
	AgentManagerPath    string
	Backends            []string
	PromptTemplatePath  string
	StatePath           string
	MaintenanceMinTurns int
	Lister              SessionLister
	GitLastCommitTime   GitLastCommitTime
	TurnCounter         func(since time.Time) (int, error)
	RunCommand          CommandRunner
	Logger              *log.Logger
}

type reviewCheckpoint struct {
	SessionID        string    `json:"session_id"`
	UpdatedAt        time.Time `json:"updated_at"`
	TurnsAccumulated int       `json:"turns_accumulated"`
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
		configuredRunner, err := agentManagedCommandRunner(o.AgentManagerPath, o.Backends)
		if err != nil {
			return fmt.Errorf("configure sync-trigger command: %w", err)
		}
		runCommand = configuredRunner
	}
	checkpointPath := o.StatePath
	if strings.TrimSpace(checkpointPath) == "" {
		checkpointPath = filepath.Join(o.RepoPath, "state", "sync-trigger.json")
	}
	checkpoint, err := loadReviewCheckpoint(checkpointPath)
	if err != nil {
		return fmt.Errorf("load sync-trigger checkpoint: %w", err)
	}
	effectiveTurns := checkpoint.TurnsAccumulated
	if o.MaintenanceMinTurns > 0 && o.TurnCounter != nil {
		countedTurns, err := o.TurnCounter(checkpoint.UpdatedAt)
		if err != nil {
			return fmt.Errorf("count accumulated turns: %w", err)
		}
		effectiveTurns += countedTurns
	}
	if o.MaintenanceMinTurns > 0 && effectiveTurns < o.MaintenanceMinTurns {
		if o.Logger != nil {
			o.Logger.Printf(
				"sync trigger: %d accumulated turn(s) below threshold %d; skipping maintenance",
				effectiveTurns,
				o.MaintenanceMinTurns,
			)
		}
		return nil
	}
	var detected *DetectionResult
	if checkpoint.UpdatedAt.IsZero() {
		detected, err = DetectSessions(ctx, o.RepoPath, lister, gitLastCommitTime)
	} else {
		detected, err = detectSessionsAfter(ctx, o.RepoPath, lister, sessionCursor{
			SessionID: checkpoint.SessionID,
			UpdatedAt: checkpoint.UpdatedAt,
		})
	}
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
		o.Logger.Printf(
			"sync trigger: reviewing source session %s; holding %d newer session(s)",
			detected.ForkSessionID,
			len(detected.NewSessions)-1,
		)
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
		cleanupContext, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		deleteOutput, deleteErr := runCommand(
			cleanupContext,
			o.RepoPath,
			o.MCTBinary,
			"session",
			"delete",
			forkedSessionID,
		)
		cancelCleanup()
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
	reviewed := detected.NewSessions[0]
	if err := saveReviewCheckpoint(checkpointPath, reviewCheckpoint{
		SessionID:        reviewed.SessionID,
		UpdatedAt:        reviewed.UpdatedAt,
		TurnsAccumulated: 0,
	}); err != nil {
		return fmt.Errorf("save sync-trigger checkpoint: %w", err)
	}
	if o.Logger != nil {
		o.Logger.Printf(
			"sync trigger: checkpoint advanced source=%s updated_at=%s fork=%s",
			reviewed.SessionID,
			reviewed.UpdatedAt.Format(time.RFC3339Nano),
			forkedSessionID,
		)
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
