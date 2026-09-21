package entrypoint

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dearmachine/dearmachine/internal/hostos"
	"github.com/dearmachine/dearmachine/internal/machtianiconfig"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

const (
	skeletonCommitMessage    = "feat: initialize user entry-point scaffold"
	dearMachineCommitMessage = "docs: add Dear Machine, reference and runbook"
	bootstrapMarkerName      = "dearmachine-bootstrap-in-progress"
	bootstrapJournalVersion  = 1
)

type bootstrapPhase string

const (
	phaseRepositoryInitialized bootstrapPhase = "repository-initialized"
	phaseLFSInstalled          bootstrapPhase = "lfs-installed"
	phaseSkeletonCommitted     bootstrapPhase = "skeleton-committed"
	phaseMachtianiInitialized  bootstrapPhase = "machtiani-initialized"
	phaseSkeletonSynced        bootstrapPhase = "skeleton-synced"
	phaseDearMachineCommitted  bootstrapPhase = "dearmachine-committed"
	phaseFinalSynced           bootstrapPhase = "final-synced"
)

var localExcludePatterns = []string{
	"/.scratch/",
	"/.secrets/",
}

//go:embed all:seed
var seedFiles embed.FS

// CommandRunner executes one bootstrap command in a working directory.
type CommandRunner func(ctx context.Context, dir, name string, args ...string) ([]byte, error)

// DefaultCommandRunner runs commands directly and returns combined output.
func DefaultCommandRunner(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	if err := machtianiconfig.Apply(command); err != nil {
		return nil, err
	}
	return command.CombinedOutput()
}

// Options configures a new entry-point repository bootstrap.
type Options struct {
	RepoPath    string
	AgentBinary string
	SnapshotDir string
	RunCommand  CommandRunner
	Resume      bool
	Progress    func(string)
}

// Result describes a completed or skipped bootstrap.
type Result struct {
	AlreadyInitialized bool
	RepoPath           string
	ProjectStore       string
	SkeletonCommit     string
	DearMachineCommit  string
	Snapshots          []string
}

type projectDetails struct {
	Store string `json:"store"`
}

type bootstrapJournal struct {
	Version           int            `json:"version"`
	Phase             bootstrapPhase `json:"phase"`
	ProjectStore      string         `json:"project_store,omitempty"`
	SkeletonCommit    string         `json:"skeleton_commit,omitempty"`
	DearMachineCommit string         `json:"dearmachine_commit,omitempty"`
}

// Initialize creates a two-stage, user-centered entry-point repository.
// Existing Git repositories are never modified.
func Initialize(ctx context.Context, options Options) (Result, error) {
	repoPath, err := absolutePath(options.RepoPath, "entry-point repository")
	if err != nil {
		return Result{}, err
	}
	agentBinary := strings.TrimSpace(options.AgentBinary)
	if agentBinary == "" {
		return Result{}, fmt.Errorf("machtiani executable is required")
	}
	snapshotDir := ""
	if strings.TrimSpace(options.SnapshotDir) != "" {
		snapshotDir, err = absolutePath(options.SnapshotDir, "snapshot directory")
		if err != nil {
			return Result{}, err
		}
	}
	runCommand := options.RunCommand
	if runCommand == nil {
		runCommand = DefaultCommandRunner
	}

	result := Result{RepoPath: repoPath}
	newRepo, journal, err := prepareRepositoryPath(repoPath, options.Resume)
	if err != nil {
		return Result{}, err
	}
	if !newRepo {
		if journal == nil {
			result.AlreadyInitialized = true
			return result, nil
		}
		result.ProjectStore = journal.ProjectStore
		result.SkeletonCommit = journal.SkeletonCommit
		result.DearMachineCommit = journal.DearMachineCommit
	}

	markerPath := filepath.Join(repoPath, ".git", bootstrapMarkerName)
	if newRepo {
		if _, err := run(ctx, runCommand, repoPath, "git", "init"); err != nil {
			return Result{}, err
		}
		journal = &bootstrapJournal{Version: bootstrapJournalVersion, Phase: phaseRepositoryInitialized}
		if err := saveBootstrapJournal(markerPath, *journal); err != nil {
			return Result{}, err
		}
		progress(options, "Repository initialized")
	}
	if err := configureWorkspaceGit(ctx, runCommand, repoPath); err != nil {
		return Result{}, err
	}
	if err := installLocalExcludes(repoPath); err != nil {
		return Result{}, err
	}
	completed := false
	defer func() {
		if completed {
			_ = os.Remove(markerPath)
		}
	}()

	if phaseBefore(journal.Phase, phaseLFSInstalled) {
		if _, err := run(ctx, runCommand, repoPath, "git", "lfs", "install", "--local"); err != nil {
			return Result{}, err
		}
		journal.Phase = phaseLFSInstalled
		if err := saveBootstrapJournal(markerPath, *journal); err != nil {
			return Result{}, err
		}
		progress(options, "Git LFS configured")
	}
	if phaseBefore(journal.Phase, phaseSkeletonCommitted) {
		if err := installSeed(repoPath, "skeleton"); err != nil {
			return Result{}, err
		}
		if err := commitAll(ctx, runCommand, repoPath, skeletonCommitMessage); err != nil {
			return Result{}, err
		}
		result.SkeletonCommit, err = commandText(ctx, runCommand, repoPath, "git", "rev-parse", "HEAD")
		if err != nil {
			return Result{}, err
		}
		journal.SkeletonCommit = result.SkeletonCommit
		journal.Phase = phaseSkeletonCommitted
		if err := saveBootstrapJournal(markerPath, *journal); err != nil {
			return Result{}, err
		}
		progress(options, "Entry-point scaffold committed")
	}

	if phaseBefore(journal.Phase, phaseMachtianiInitialized) {
		if _, err := run(ctx, runCommand, repoPath, agentBinary, "init", "--no-interactive", "--json"); err != nil {
			return Result{}, err
		}
		projectOutput, err := run(ctx, runCommand, repoPath, agentBinary, "project", "show", "--json")
		if err != nil {
			return Result{}, err
		}
		var project projectDetails
		if err := json.Unmarshal(projectOutput, &project); err != nil {
			return Result{}, fmt.Errorf("parse machtiani project details: %w", err)
		}
		result.ProjectStore, err = absolutePath(project.Store, "machtiani project store")
		if err != nil {
			return Result{}, err
		}
		journal.ProjectStore = result.ProjectStore
		journal.Phase = phaseMachtianiInitialized
		if err := saveBootstrapJournal(markerPath, *journal); err != nil {
			return Result{}, err
		}
		progress(options, "Machtiani project initialized")
	}
	result.ProjectStore = journal.ProjectStore
	readmePath := filepath.Join(result.ProjectStore, "artifacts", "readme", "internal-readme.md")

	if snapshotDir != "" && phaseBefore(journal.Phase, phaseSkeletonSynced) {
		path, err := captureSnapshot(snapshotDir, "01-before-skeleton-sync.md", readmePath)
		if err != nil {
			return Result{}, err
		}
		result.Snapshots = append(result.Snapshots, path)
	}
	if phaseBefore(journal.Phase, phaseSkeletonSynced) {
		if _, err := run(ctx, runCommand, repoPath, agentBinary, "sync", "--include-docs"); err != nil {
			return Result{}, err
		}
		journal.Phase = phaseSkeletonSynced
		if err := saveBootstrapJournal(markerPath, *journal); err != nil {
			return Result{}, err
		}
		progress(options, "Entry-point scaffold synchronized")
	}
	if snapshotDir != "" && journal.Phase == phaseSkeletonSynced {
		path, err := captureSnapshot(snapshotDir, "02-after-skeleton-sync.md", readmePath)
		if err != nil {
			return Result{}, err
		}
		result.Snapshots = append(result.Snapshots, path)
	}

	if phaseBefore(journal.Phase, phaseDearMachineCommitted) {
		if err := installSeed(repoPath, "dearmachine"); err != nil {
			return Result{}, err
		}
		if err := commitAll(ctx, runCommand, repoPath, dearMachineCommitMessage); err != nil {
			return Result{}, err
		}
		result.DearMachineCommit, err = commandText(ctx, runCommand, repoPath, "git", "rev-parse", "HEAD")
		if err != nil {
			return Result{}, err
		}
		journal.DearMachineCommit = result.DearMachineCommit
		journal.Phase = phaseDearMachineCommitted
		if err := saveBootstrapJournal(markerPath, *journal); err != nil {
			return Result{}, err
		}
		progress(options, "Dear Machine documentation committed")
	}
	if snapshotDir != "" && phaseBefore(journal.Phase, phaseFinalSynced) {
		path, err := captureSnapshot(snapshotDir, "03-before-dearmachine-sync.md", readmePath)
		if err != nil {
			return Result{}, err
		}
		result.Snapshots = append(result.Snapshots, path)
	}
	if phaseBefore(journal.Phase, phaseFinalSynced) {
		if _, err := run(ctx, runCommand, repoPath, agentBinary, "sync", "--include-docs"); err != nil {
			return Result{}, err
		}
		journal.Phase = phaseFinalSynced
		if err := saveBootstrapJournal(markerPath, *journal); err != nil {
			return Result{}, err
		}
		progress(options, "Dear Machine entry point synchronized")
	}
	if snapshotDir != "" {
		path, err := captureSnapshot(snapshotDir, "04-after-dearmachine-sync.md", readmePath)
		if err != nil {
			return Result{}, err
		}
		result.Snapshots = append(result.Snapshots, path)
	}

	completed = true
	return result, nil
}

func installLocalExcludes(repoPath string) error {
	excludePath := filepath.Join(repoPath, ".git", "info", "exclude")
	data, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read local Git excludes: %w", err)
	}

	contents := string(data)
	lines := strings.Split(contents, "\n")
	for _, pattern := range localExcludePatterns {
		present := false
		for _, line := range lines {
			if strings.TrimSpace(line) == pattern {
				present = true
				break
			}
		}
		if present {
			continue
		}
		if contents != "" && !strings.HasSuffix(contents, "\n") {
			contents += "\n"
		}
		contents += pattern + "\n"
		lines = append(lines, pattern)
	}

	if err := os.MkdirAll(filepath.Dir(excludePath), 0o700); err != nil {
		return fmt.Errorf("create local Git info directory: %w", err)
	}
	if err := os.WriteFile(excludePath, []byte(contents), 0o600); err != nil {
		return fmt.Errorf("write local Git excludes: %w", err)
	}
	return nil
}

func absolutePath(path, label string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("%s path is required", label)
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s path: %w", label, err)
	}
	return filepath.Clean(resolved), nil
}

func prepareRepositoryPath(path string, resume bool) (bool, *bootstrapJournal, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, nil, fmt.Errorf("inspect entry-point repository: %w", err)
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			return false, nil, fmt.Errorf("create entry-point repository: %w", err)
		}
		return true, nil, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, nil, fmt.Errorf("entry-point repository path must be a directory, not a symlink or file: %s", path)
	}
	gitPath := filepath.Join(path, ".git")
	if _, err := os.Stat(gitPath); err == nil {
		markerPath := filepath.Join(gitPath, bootstrapMarkerName)
		if _, markerErr := os.Stat(markerPath); markerErr == nil {
			if !resume {
				return false, nil, fmt.Errorf("entry-point repository has an incomplete bootstrap: %s; retry with --resume", path)
			}
			journal, err := loadBootstrapJournal(markerPath)
			if err != nil {
				return false, nil, err
			}
			return false, &journal, nil
		}
		return false, nil, nil
	} else if !os.IsNotExist(err) {
		return false, nil, fmt.Errorf("inspect entry-point Git metadata: %w", err)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, nil, fmt.Errorf("read entry-point repository: %w", err)
	}
	if len(entries) != 0 {
		return false, nil, fmt.Errorf("entry-point repository exists, is not a Git repository, and is not empty: %s", path)
	}
	return true, nil, nil
}

func phaseBefore(current, target bootstrapPhase) bool {
	order := map[bootstrapPhase]int{
		phaseRepositoryInitialized: 1,
		phaseLFSInstalled:          2,
		phaseSkeletonCommitted:     3,
		phaseMachtianiInitialized:  4,
		phaseSkeletonSynced:        5,
		phaseDearMachineCommitted:  6,
		phaseFinalSynced:           7,
	}
	return order[current] < order[target]
}

func saveBootstrapJournal(path string, journal bootstrapJournal) error {
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return fmt.Errorf("encode bootstrap journal: %w", err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".dearmachine-bootstrap-*.tmp")
	if err != nil {
		return fmt.Errorf("create bootstrap journal: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write bootstrap journal: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish bootstrap journal: %w", err)
	}
	return nil
}

func loadBootstrapJournal(path string) (bootstrapJournal, error) {
	metadata, err := os.Lstat(path)
	if err != nil {
		return bootstrapJournal{}, fmt.Errorf("inspect bootstrap journal: %w", err)
	}
	if !metadata.Mode().IsRegular() || metadata.Mode()&os.ModeSymlink != 0 || !hostos.Private(path, metadata, 0o077) {
		return bootstrapJournal{}, errors.New("bootstrap journal must be a private regular file owned by the current user")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return bootstrapJournal{}, fmt.Errorf("read bootstrap journal: %w", err)
	}
	var journal bootstrapJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return bootstrapJournal{}, fmt.Errorf("parse bootstrap journal: %w", err)
	}
	if journal.Version != bootstrapJournalVersion || !knownBootstrapPhase(journal.Phase) {
		return bootstrapJournal{}, fmt.Errorf("unsupported bootstrap journal version or phase in %s", path)
	}
	return journal, nil
}

func knownBootstrapPhase(phase bootstrapPhase) bool {
	return !phaseBefore(phase, phaseRepositoryInitialized) && !phaseBefore(phaseFinalSynced, phase)
}

func progress(options Options, message string) {
	if options.Progress != nil {
		options.Progress(message)
	}
}

func installSeed(repoPath, phase string) error {
	// Embedded FS paths always use slashes, including on Windows. Convert to
	// native paths only when writing the extracted files to the workspace.
	root := path.Join("seed", phase)
	return fs.WalkDir(seedFiles, root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative := strings.TrimPrefix(path, root+"/")
		target := filepath.Join(repoPath, filepath.FromSlash(relative))
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if _, err := os.Stat(target); err == nil {
			return fmt.Errorf("seed target already exists: %s", target)
		} else if !os.IsNotExist(err) {
			return err
		}
		data, err := seedFiles.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return fmt.Errorf("write seed file %s: %w", relative, err)
		}
		return nil
	})
}

// configureWorkspaceGit applies only to a new repository or our resumable
// bootstrap. Existing repositories return before this point. Explicit empty
// email fields prevent Git from inheriting or guessing the user's address.
func configureWorkspaceGit(ctx context.Context, runner CommandRunner, repoPath string) error {
	for _, setting := range [][2]string{
		{"user.name", "machtiani"}, {"user.email", ""},
		{"author.name", "machtiani"}, {"author.email", ""},
		{"committer.name", "machtiani"}, {"committer.email", ""},
		{"commit.gpgSign", "false"},
	} {
		if _, err := run(ctx, runner, repoPath, "git", "config", "--local", setting[0], setting[1]); err != nil {
			return err
		}
	}
	return nil
}

func commitAll(ctx context.Context, runner CommandRunner, repoPath, message string) error {
	if _, err := run(ctx, runner, repoPath, "git", "add", "--all"); err != nil {
		return err
	}
	if _, err := run(ctx, runner, repoPath, "git", "commit", "-m", message); err != nil {
		return err
	}
	return nil
}

func commandText(ctx context.Context, runner CommandRunner, dir, name string, args ...string) (string, error) {
	output, err := run(ctx, runner, dir, name, args...)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(output))
	if value == "" {
		return "", fmt.Errorf("command returned empty output: %s %s", name, strings.Join(args, " "))
	}
	return value, nil
}

func run(ctx context.Context, runner CommandRunner, dir, name string, args ...string) ([]byte, error) {
	output, err := runner(ctx, dir, name, args...)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = ctx.Err()
		}
		return nil, fmt.Errorf(
			"run %s %s: %w: %s",
			name,
			strings.Join(args, " "),
			err,
			strings.TrimSpace(string(output)),
		)
	}
	return output, nil
}

func captureSnapshot(snapshotDir, name, readmePath string) (string, error) {
	if err := os.MkdirAll(snapshotDir, 0o700); err != nil {
		return "", fmt.Errorf("create snapshot directory: %w", err)
	}
	data, err := os.ReadFile(readmePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("read internal README for snapshot: %w", err)
		}
		data = []byte("# Internal README snapshot\n\nNo internal README existed at this sync boundary.\n")
	}
	target := filepath.Join(snapshotDir, name)
	if err := os.WriteFile(target, data, 0o600); err != nil {
		return "", fmt.Errorf("write internal README snapshot: %w", err)
	}
	return target, nil
}
