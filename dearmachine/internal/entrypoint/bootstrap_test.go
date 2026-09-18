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
	case "/fake/machtiani init --no-interactive --json":
		return []byte(`{"status":"initialized"}`), nil
	case "/fake/machtiani project show --json":
		return []byte(`{"store":"` + r.store + `"}`), nil
	case "/fake/machtiani sync --include-docs":
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
		AgentBinary: "/fake/machtiani",
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
		"git config --local user.name machtiani",
		"git config --local user.email ",
		"git config --local author.name machtiani",
		"git config --local author.email ",
		"git config --local committer.name machtiani",
		"git config --local committer.email ",
		"git config --local commit.gpgSign false",
		"git lfs install --local",
		"git add --all",
		"git commit -m " + skeletonCommitMessage,
		"git rev-parse HEAD",
		"/fake/machtiani init --no-interactive --json",
		"/fake/machtiani project show --json",
		"/fake/machtiani sync --include-docs",
		"git add --all",
		"git commit -m " + dearMachineCommitMessage,
		"git rev-parse HEAD",
		"/fake/machtiani sync --include-docs",
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
		RepoPath:    repo,
		AgentBinary: "/fake/machtiani",
		RunCommand:  runner.Run,
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
		RepoPath:    repo,
		AgentBinary: "/fake/machtiani",
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
		RepoPath:    repo,
		AgentBinary: "/fake/machtiani",
		RunCommand:  runner.Run,
	})
	if err == nil || !strings.Contains(err.Error(), "sync failed") {
		t.Fatalf("Initialize error = %v, want sync failure", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", bootstrapMarkerName)); err != nil {
		t.Fatalf("incomplete marker missing: %v", err)
	}
	_, err = Initialize(context.Background(), Options{
		RepoPath:    repo,
		AgentBinary: "/fake/machtiani",
	})
	if err == nil || !strings.Contains(err.Error(), "incomplete bootstrap") {
		t.Fatalf("retry error = %v, want incomplete-bootstrap diagnostic", err)
	}
}

func TestInitializeResumeContinuesAfterRecordedPhase(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(root, "entrypoint")
	runner := &fakeCommandRunner{store: filepath.Join(root, "store"), failSync: true}
	_, err := Initialize(context.Background(), Options{
		RepoPath: repo, AgentBinary: "/fake/machtiani", RunCommand: runner.Run,
	})
	if err == nil || !strings.Contains(err.Error(), "sync failed") {
		t.Fatalf("first Initialize error = %v", err)
	}
	runner.failSync = false
	result, err := Initialize(context.Background(), Options{
		RepoPath: repo, AgentBinary: "/fake/machtiani", RunCommand: runner.Run, Resume: true,
	})
	if err != nil {
		t.Fatalf("resumed Initialize: %v", err)
	}
	if result.SkeletonCommit != "skeleton-commit" || result.DearMachineCommit != "dearmachine-commit" {
		t.Fatalf("resumed commits = %q, %q", result.SkeletonCommit, result.DearMachineCommit)
	}
	if countCall(runner.calls, "git init") != 1 || countCall(runner.calls, "git commit -m "+skeletonCommitMessage) != 1 ||
		countCall(runner.calls, "/fake/machtiani init --no-interactive --json") != 1 {
		t.Fatalf("durable phases repeated on resume: %v", runner.calls)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", bootstrapMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("bootstrap journal remains after resume: %v", err)
	}
}

func TestInitializeResumeRejectsPublicBootstrapJournal(t *testing.T) {
	t.Parallel()
	repo := filepath.Join(t.TempDir(), "entrypoint")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(repo, ".git", bootstrapMarkerName)
	if err := os.WriteFile(marker, []byte(`{"version":1,"phase":"repository-initialized"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(marker, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Initialize(context.Background(), Options{RepoPath: repo, AgentBinary: "/fake/machtiani", Resume: true})
	if err == nil || !strings.Contains(err.Error(), "private regular file") {
		t.Fatalf("Initialize error = %v", err)
	}
}

func countCall(calls []string, target string) int {
	count := 0
	for _, call := range calls {
		if call == target {
			count++
		}
	}
	return count
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

func TestDefaultCommandRunnerSelectsManagedConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MACHTIANI_CONFIG", filepath.Join(home, "personal.toml"))
	output, err := DefaultCommandRunner(context.Background(), home, "sh", "-c", `printf '%s' "$MACHTIANI_CONFIG"`)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != filepath.Join(home, ".config/dearmachine/machtiani/config.toml") {
		t.Fatal("bootstrap child did not use DearMachine configuration")
	}
}
