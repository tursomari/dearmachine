package entrypoint

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type fakeCommandRunner struct {
	store       string
	calls       []string
	commitCount int
	syncCount   int
	failSync    bool
}

func (r *fakeCommandRunner) Run(
	_ context.Context,
	dir string,
	name string,
	args ...string,
) ([]byte, error) {
	command := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, command)
	switch command {
	case "git init":
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
			return nil, err
		}
	case "git commit -m " + skeletonCommitMessage, "git commit -m " + dearMachineCommitMessage:
		r.commitCount++
	case "git rev-parse HEAD":
		if r.commitCount == 1 {
			return []byte("skeleton-commit\n"), nil
		}
		return []byte("dearmachine-commit\n"), nil
	case "/fake/mct-agent init --no-interactive --json":
		return []byte(`{"status":"initialized"}`), nil
	case "/fake/mct-agent project show --json":
		return []byte(`{"store":"` + r.store + `"}`), nil
	case "/fake/mct-agent sync --include-docs":
		r.syncCount++
		if r.failSync {
			return []byte("sync diagnostic"), errors.New("sync failed")
		}
		readmeDir := filepath.Join(r.store, "artifacts", "readme")
		if err := os.MkdirAll(readmeDir, 0o700); err != nil {
			return nil, err
		}
		content := "# Skeleton internal README\n"
		if r.syncCount == 2 {
			content = "# Entry-point internal README\n\nDear Machine, is one documented service.\n"
		}
		if err := os.WriteFile(filepath.Join(readmeDir, "internal-readme.md"), []byte(content), 0o600); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func TestInitializeCreatesTwoStageBootstrapAndSnapshots(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(root, "entrypoint")
	store := filepath.Join(root, "store")
	snapshots := filepath.Join(root, "snapshots")
	runner := &fakeCommandRunner{store: store}

	result, err := Initialize(context.Background(), Options{
		RepoPath:    repo,
		MCTBinary:   "/fake/mct-agent",
		SnapshotDir: snapshots,
		RunCommand:  runner.Run,
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if result.AlreadyInitialized || result.RepoPath != repo || result.ProjectStore != store {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.SkeletonCommit != "skeleton-commit" || result.DearMachineCommit != "dearmachine-commit" {
		t.Fatalf("commit results = %q, %q", result.SkeletonCommit, result.DearMachineCommit)
	}
	wantSnapshots := []string{
		filepath.Join(snapshots, "01-before-skeleton-sync.md"),
		filepath.Join(snapshots, "02-after-skeleton-sync.md"),
		filepath.Join(snapshots, "03-before-dearmachine-sync.md"),
		filepath.Join(snapshots, "04-after-dearmachine-sync.md"),
	}
	if !slices.Equal(result.Snapshots, wantSnapshots) {
		t.Fatalf("snapshots = %v, want %v", result.Snapshots, wantSnapshots)
	}

	assertFileContains(t, filepath.Join(repo, "README.md"), "durable, machine-level entry point")
	assertFileContains(t, filepath.Join(repo, "documentation", "dearmachine-architecture.md"), "one machine-level service")
	updatePrompt := filepath.Join(repo, "documentation", "update-prompt-template.md")
	assertFileContains(t, updatePrompt, "6. Maintain todo/")
	assertFileContains(t, updatePrompt, "7. Decide whether to commit")
	assertFileNotContains(t, updatePrompt, "**7. Maintain todo/**")
	assertFileContains(t, filepath.Join(repo, "process", "configure-dearmachine.md"), "Configure the DearMachine Client")
	assertFileContains(t, filepath.Join(repo, "todo", "README.md"), "reminders, follow-ups, and flags")
	todoEntries, err := os.ReadDir(filepath.Join(repo, "todo"))
	if err != nil {
		t.Fatalf("read seeded todo directory: %v", err)
	}
	if len(todoEntries) != 1 || todoEntries[0].Name() != "README.md" {
		t.Fatalf("seeded todo entries = %v, want only README.md", todoEntries)
	}
	assertFileContains(t, filepath.Join(repo, ".gitignore"), "!state/README.md")
	assertFileContains(t, filepath.Join(repo, ".git", "info", "exclude"), "/.scratch/")
	assertFileContains(t, filepath.Join(repo, ".git", "info", "exclude"), "/.secrets/")
	assertFileNotContains(t, filepath.Join(repo, ".gitignore"), ".secrets")
	if _, err := os.Stat(filepath.Join(repo, ".git", bootstrapMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("bootstrap marker remains after success: %v", err)
	}

	assertFileContains(t, wantSnapshots[0], "No internal README existed")
	assertFileContains(t, wantSnapshots[1], "Skeleton internal README")
	assertFileContains(t, wantSnapshots[2], "Skeleton internal README")
	assertFileContains(t, wantSnapshots[3], "Dear Machine, is one documented service")

	wantCalls := []string{
		"git init",
		"git lfs install --local",
		"git add --all",
		"git commit -m " + skeletonCommitMessage,
		"git rev-parse HEAD",
		"/fake/mct-agent init --no-interactive --json",
		"/fake/mct-agent project show --json",
		"/fake/mct-agent sync --include-docs",
		"git add --all",
		"git commit -m " + dearMachineCommitMessage,
		"git rev-parse HEAD",
		"/fake/mct-agent sync --include-docs",
	}
	if !slices.Equal(runner.calls, wantCalls) {
		t.Fatalf("commands =\n%s\nwant =\n%s", strings.Join(runner.calls, "\n"), strings.Join(wantCalls, "\n"))
	}
}

func TestInitializeLeavesExistingRepositoryUntouched(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("user content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeCommandRunner{}
	result, err := Initialize(context.Background(), Options{
		RepoPath:   repo,
		MCTBinary:  "/fake/mct-agent",
		RunCommand: runner.Run,
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !result.AlreadyInitialized || len(runner.calls) != 0 {
		t.Fatalf("result/calls = %+v / %v", result, runner.calls)
	}
	assertFileContains(t, filepath.Join(repo, "README.md"), "user content")
}

func TestInstallLocalExcludesPreservesExistingEntriesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	infoDir := filepath.Join(repo, ".git", "info")
	if err := os.MkdirAll(infoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	excludePath := filepath.Join(infoDir, "exclude")
	if err := os.WriteFile(excludePath, []byte("/local-only/\n/.scratch/\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := installLocalExcludes(repo); err != nil {
		t.Fatalf("installLocalExcludes first call: %v", err)
	}
	if err := installLocalExcludes(repo); err != nil {
		t.Fatalf("installLocalExcludes second call: %v", err)
	}

	data, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	want := "/local-only/\n/.scratch/\n/.secrets/\n"
	if string(data) != want {
		t.Fatalf("exclude contents = %q, want %q", data, want)
	}
}

func TestInitializeRejectsNonEmptyNonRepository(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "keep.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Initialize(context.Background(), Options{
		RepoPath:  repo,
		MCTBinary: "/fake/mct-agent",
	})
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("Initialize error = %v, want non-empty rejection", err)
	}
	assertFileContains(t, filepath.Join(repo, "keep.txt"), "keep")
}

func TestInitializeFailureLeavesExplicitIncompleteMarker(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(root, "entrypoint")
	runner := &fakeCommandRunner{store: filepath.Join(root, "store"), failSync: true}
	_, err := Initialize(context.Background(), Options{
		RepoPath:   repo,
		MCTBinary:  "/fake/mct-agent",
		RunCommand: runner.Run,
	})
	if err == nil || !strings.Contains(err.Error(), "sync failed") {
		t.Fatalf("Initialize error = %v, want sync failure", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", bootstrapMarkerName)); err != nil {
		t.Fatalf("incomplete marker missing: %v", err)
	}
	_, err = Initialize(context.Background(), Options{
		RepoPath:  repo,
		MCTBinary: "/fake/mct-agent",
	})
	if err == nil || !strings.Contains(err.Error(), "incomplete bootstrap") {
		t.Fatalf("retry error = %v, want incomplete-bootstrap diagnostic", err)
	}
}

func TestSkeletonSeedDoesNotFrameDearMachineAsEntryPointPurpose(t *testing.T) {
	t.Parallel()
	var combined strings.Builder
	err := fs.WalkDir(seedFiles, "seed/skeleton", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, err := seedFiles.ReadFile(path)
		if err != nil {
			return err
		}
		combined.Write(data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk skeleton seed: %v", err)
	}
	if strings.Contains(strings.ToLower(combined.String()), "dearmachine") {
		t.Fatal("skeleton seed contains DearMachine-specific framing")
	}
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s does not contain %q:\n%s", path, want, data)
	}
}

func assertFileNotContains(t *testing.T, path, unwanted string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.Contains(string(data), unwanted) {
		t.Fatalf("%s unexpectedly contains %q:\n%s", path, unwanted, data)
	}
}
