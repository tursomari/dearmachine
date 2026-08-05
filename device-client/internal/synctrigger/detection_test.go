package synctrigger

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetectSessionsNoCommits(t *testing.T) {
	repo := initGitRepo(t)

	detected, err := DetectSessions(context.Background(), repo, func(context.Context, string) ([]SessionInfo, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatalf("DetectSessions: %v", err)
	}
	if !detected.LastSyncCommitTime.IsZero() {
		t.Fatalf("last sync commit time = %s, want zero", detected.LastSyncCommitTime)
	}
	if len(detected.NewSessions) != 0 {
		t.Fatalf("new sessions count = %d, want 0", len(detected.NewSessions))
	}
	if detected.ForkSessionID != "" {
		t.Fatalf("fork session = %q, want empty", detected.ForkSessionID)
	}
}

func TestDetectSessionsNoNewSessions(t *testing.T) {
	repo, commitTime := initGitRepoWithCommit(t)

	detected, err := DetectSessions(context.Background(), repo, func(context.Context, string) ([]SessionInfo, error) {
		return []SessionInfo{
			{
				SessionID: "older-1",
				UpdatedAt: commitTime.Add(-5 * time.Minute),
				Goal:      "already-synced-1",
			},
			{
				SessionID: "older-2",
				UpdatedAt: commitTime.Add(-2 * time.Hour),
				Goal:      "already-synced-2",
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("DetectSessions: %v", err)
	}
	if len(detected.NewSessions) != 0 {
		t.Fatalf("new sessions count = %d, want 0", len(detected.NewSessions))
	}
	if detected.ForkSessionID != "" {
		t.Fatalf("fork session = %q, want empty", detected.ForkSessionID)
	}
}

func TestDetectSessionsOneNewSession(t *testing.T) {
	repo, commitTime := initGitRepoWithCommit(t)

	detected, err := DetectSessions(context.Background(), repo, func(context.Context, string) ([]SessionInfo, error) {
		return []SessionInfo{
			{
				SessionID: "newest",
				UpdatedAt: commitTime.Add(3 * time.Minute),
				Goal:      "new session",
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("DetectSessions: %v", err)
	}
	if len(detected.NewSessions) != 1 {
		t.Fatalf("new sessions count = %d, want 1", len(detected.NewSessions))
	}
	if detected.NewSessions[0].SessionID != "newest" {
		t.Fatalf("new session = %q, want newest", detected.NewSessions[0].SessionID)
	}
	if detected.ForkSessionID != "" {
		t.Fatalf("fork session = %q, want empty", detected.ForkSessionID)
	}
}

func TestDetectSessionsTwoNewSessions(t *testing.T) {
	repo, commitTime := initGitRepoWithCommit(t)

	detected, err := DetectSessions(context.Background(), repo, func(context.Context, string) ([]SessionInfo, error) {
		return []SessionInfo{
			{
				SessionID: "older",
				UpdatedAt: commitTime.Add(10 * time.Minute),
				Goal:      "first new session",
			},
			{
				SessionID: "newest",
				UpdatedAt: commitTime.Add(20 * time.Minute),
				Goal:      "latest new session",
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("DetectSessions: %v", err)
	}
	if len(detected.NewSessions) != 2 {
		t.Fatalf("new sessions count = %d, want 2", len(detected.NewSessions))
	}
	if detected.ForkSessionID != "newest" {
		t.Fatalf("fork session = %q, want newest", detected.ForkSessionID)
	}
}

func TestDetectSessionsNonExistentRepo(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := DetectSessions(context.Background(), missing, func(context.Context, string) ([]SessionInfo, error) {
		return nil, nil
	})
	if err == nil {
		t.Fatal("DetectSessions = nil, want error")
	}
}

func TestDefaultGitLastCommitTimeUsesInternalReadmeMarker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo, syncedTime := initGitRepoWithCommit(t)
	syncedCommit := gitCommandOutput(t, repo, "rev-parse", "HEAD")

	laterTime := syncedTime.Add(2 * time.Hour)
	writeCommitAt(t, repo, "documentation.md", "later", laterTime)

	projectID := "test-entry-point"
	markerDir := filepath.Join(repo, ".machtiani")
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		t.Fatalf("create project marker dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, "project.uuid"), []byte(projectID+"\n"), 0o600); err != nil {
		t.Fatalf("write project marker: %v", err)
	}
	syncStateDir := filepath.Join(
		home,
		".machtiani",
		projectID,
		"artifacts",
		"readme",
		".state",
	)
	if err := os.MkdirAll(syncStateDir, 0o700); err != nil {
		t.Fatalf("create sync state dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(syncStateDir, "last_project_commit"),
		[]byte(syncedCommit+"\n"),
		0o600,
	); err != nil {
		t.Fatalf("write sync marker: %v", err)
	}

	got, err := DefaultGitLastCommitTime(repo)
	if err != nil {
		t.Fatalf("DefaultGitLastCommitTime: %v", err)
	}
	if !got.Equal(syncedTime) {
		t.Fatalf("last sync time = %s, want %s", got, syncedTime)
	}
}

func TestDefaultGitLastCommitTimeFallsBackToFirstCommit(t *testing.T) {
	repo, firstTime := initGitRepoWithCommit(t)
	writeCommitAt(t, repo, "later.txt", "later", firstTime.Add(3*time.Hour))

	got, err := DefaultGitLastCommitTime(repo)
	if err != nil {
		t.Fatalf("DefaultGitLastCommitTime: %v", err)
	}
	if !got.Equal(firstTime) {
		t.Fatalf("fallback sync time = %s, want first commit %s", got, firstTime)
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGitCommand(t, repo, []string{}, "init")
	return repo
}

func initGitRepoWithCommit(t *testing.T) (string, time.Time) {
	t.Helper()
	repo := initGitRepo(t)
	commitTime := time.Date(2026, 7, 15, 10, 12, 0, 0, time.UTC)

	readme := filepath.Join(repo, "README.md")
	if err := os.WriteFile(readme, []byte("seed"), 0o600); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	runGitCommand(t, repo, []string{
		"GIT_AUTHOR_NAME=Synctrigger",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_AUTHOR_DATE=" + commitTime.Format(time.RFC3339),
		"GIT_COMMITTER_NAME=Synctrigger",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_COMMITTER_DATE=" + commitTime.Format(time.RFC3339),
	}, "add", "README.md")
	runGitCommand(t, repo, []string{
		"GIT_AUTHOR_NAME=Synctrigger",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_AUTHOR_DATE=" + commitTime.Format(time.RFC3339),
		"GIT_COMMITTER_NAME=Synctrigger",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_COMMITTER_DATE=" + commitTime.Format(time.RFC3339),
	}, "commit", "-m", "initial", "--date", commitTime.Format(time.RFC3339))
	return repo, commitTime
}

func runGitCommand(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()

	command := exec.Command("git", args...)
	command.Dir = dir
	if len(env) > 0 {
		command.Env = append(os.Environ(), env...)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf(
			"git %s: %v: %s",
			strings.Join(args, " "),
			err,
			strings.TrimSpace(string(output)),
		)
	}
}

func gitCommandOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

func writeCommitAt(t *testing.T, repo, name, content string, commitTime time.Time) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	env := []string{
		"GIT_AUTHOR_NAME=Synctrigger",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_AUTHOR_DATE=" + commitTime.Format(time.RFC3339),
		"GIT_COMMITTER_NAME=Synctrigger",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_COMMITTER_DATE=" + commitTime.Format(time.RFC3339),
	}
	runGitCommand(t, repo, env, "add", name)
	runGitCommand(t, repo, env, "commit", "-m", "later", "--date", commitTime.Format(time.RFC3339))
}
