package entrypoint

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	skeletonCommitMessage    = "feat: initialize user entry-point scaffold"
	dearMachineCommitMessage = "docs: add DearMachine reference and runbook"
	bootstrapMarkerName      = "dearmachine-bootstrap-in-progress"
)

//go:embed all:seed
var seedFiles embed.FS

// CommandRunner executes one bootstrap command in a working directory.
type CommandRunner func(ctx context.Context, dir, name string, args ...string) ([]byte, error)

// DefaultCommandRunner runs commands directly and returns combined output.
func DefaultCommandRunner(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	return command.CombinedOutput()
}

// Options configures a new entry-point repository bootstrap.
type Options struct {
	RepoPath    string
	MCTBinary   string
	SnapshotDir string
	RunCommand  CommandRunner
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

// Initialize creates a two-stage, user-centered entry-point repository.
// Existing Git repositories are never modified.
func Initialize(ctx context.Context, options Options) (Result, error) {
	repoPath, err := absolutePath(options.RepoPath, "entry-point repository")
	if err != nil {
		return Result{}, err
	}
	mctBinary := strings.TrimSpace(options.MCTBinary)
	if mctBinary == "" {
		return Result{}, fmt.Errorf("mct-agent executable is required")
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
	newRepo, err := prepareRepositoryPath(repoPath)
	if err != nil {
		return Result{}, err
	}
	if !newRepo {
		result.AlreadyInitialized = true
		return result, nil
	}

	if _, err := run(ctx, runCommand, repoPath, "git", "init"); err != nil {
		return Result{}, err
	}
	markerPath := filepath.Join(repoPath, ".git", bootstrapMarkerName)
	if err := os.WriteFile(markerPath, []byte("bootstrap in progress\n"), 0o600); err != nil {
		return Result{}, fmt.Errorf("write bootstrap marker: %w", err)
	}
	completed := false
	defer func() {
		if completed {
			_ = os.Remove(markerPath)
		}
	}()

	if _, err := run(ctx, runCommand, repoPath, "git", "lfs", "install", "--local"); err != nil {
		return Result{}, err
	}
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

	if _, err := run(ctx, runCommand, repoPath, mctBinary, "init", "--no-interactive", "--json"); err != nil {
		return Result{}, err
	}
	projectOutput, err := run(ctx, runCommand, repoPath, mctBinary, "project", "show", "--json")
	if err != nil {
		return Result{}, err
	}
	var project projectDetails
	if err := json.Unmarshal(projectOutput, &project); err != nil {
		return Result{}, fmt.Errorf("parse mct-agent project details: %w", err)
	}
	result.ProjectStore, err = absolutePath(project.Store, "mct-agent project store")
	if err != nil {
		return Result{}, err
	}
	readmePath := filepath.Join(result.ProjectStore, "artifacts", "readme", "internal-readme.md")

	if snapshotDir != "" {
		path, err := captureSnapshot(snapshotDir, "01-before-skeleton-sync.md", readmePath)
		if err != nil {
			return Result{}, err
		}
		result.Snapshots = append(result.Snapshots, path)
	}
	if _, err := run(ctx, runCommand, repoPath, mctBinary, "sync", "--include-docs"); err != nil {
		return Result{}, err
	}
	if snapshotDir != "" {
		path, err := captureSnapshot(snapshotDir, "02-after-skeleton-sync.md", readmePath)
		if err != nil {
			return Result{}, err
		}
		result.Snapshots = append(result.Snapshots, path)
	}

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
	if snapshotDir != "" {
		path, err := captureSnapshot(snapshotDir, "03-before-dearmachine-sync.md", readmePath)
		if err != nil {
			return Result{}, err
		}
		result.Snapshots = append(result.Snapshots, path)
	}
	if _, err := run(ctx, runCommand, repoPath, mctBinary, "sync", "--include-docs"); err != nil {
		return Result{}, err
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

func prepareRepositoryPath(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("inspect entry-point repository: %w", err)
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			return false, fmt.Errorf("create entry-point repository: %w", err)
		}
		return true, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("entry-point repository path must be a directory, not a symlink or file: %s", path)
	}
	gitPath := filepath.Join(path, ".git")
	if _, err := os.Stat(gitPath); err == nil {
		if _, markerErr := os.Stat(filepath.Join(gitPath, bootstrapMarkerName)); markerErr == nil {
			return false, fmt.Errorf("entry-point repository has an incomplete bootstrap: %s", path)
		}
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect entry-point Git metadata: %w", err)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, fmt.Errorf("read entry-point repository: %w", err)
	}
	if len(entries) != 0 {
		return false, fmt.Errorf("entry-point repository exists, is not a Git repository, and is not empty: %s", path)
	}
	return true, nil
}

func installSeed(repoPath, phase string) error {
	root := filepath.Join("seed", phase)
	return fs.WalkDir(seedFiles, root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		target := filepath.Join(repoPath, relative)
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
