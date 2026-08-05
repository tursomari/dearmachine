package synctrigger

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SessionInfo holds the minimal info about an mct-agent session needed
// for the sync trigger decision.
type SessionInfo struct {
	SessionID string
	UpdatedAt time.Time
	Goal      string
}

// DetectionResult is the output of DetectSessions.
type DetectionResult struct {
	// NewSessions are sessions created after the last sync commit timestamp.
	NewSessions []SessionInfo
	// ForkSessionID is the SessionID of the most recent new session,
	// or empty if fewer than 2 new sessions were found.
	ForkSessionID string
	// LastSyncCommitTime is the timestamp of the last internal-readme commit
	// (or the repo's first commit if no internal readme exists).
	LastSyncCommitTime time.Time
}

// SessionLister returns mct-agent sessions for a repo.
type SessionLister func(ctx context.Context, repoDir string) ([]SessionInfo, error)

// GitLastCommitTime returns the timestamp of the project commit most recently
// processed by internal-README sync.
type GitLastCommitTime func(repoDir string) (time.Time, error)

type sessionRecord struct {
	SessionID string `json:"session_id"`
	UpdatedAt string `json:"updated_at"`
	Goal      string `json:"goal"`
	Status    string `json:"status"`
}

// DetectSessions returns detection context for determining whether a sync fork is needed.
//
// lister controls how mct-agent sessions are discovered (for tests).
// gitLastCommitTime controls how the last sync commit timestamp is fetched.
// If gitLastCommitTime is not supplied, DefaultGitLastCommitTime is used.
func DetectSessions(
	ctx context.Context,
	entryPointRepo string,
	lister SessionLister,
	gitLastCommitTime ...GitLastCommitTime,
) (*DetectionResult, error) {
	resolver := DefaultGitLastCommitTime
	if len(gitLastCommitTime) > 0 && gitLastCommitTime[0] != nil {
		resolver = gitLastCommitTime[0]
	}
	if lister == nil {
		lister = DefaultSessionLister
	}

	lastSyncTime, err := resolver(entryPointRepo)
	if err != nil {
		return nil, err
	}

	sessions, err := lister(ctx, entryPointRepo)
	if err != nil {
		return nil, err
	}

	newSessions := make([]SessionInfo, 0, len(sessions))
	for _, session := range sessions {
		if session.UpdatedAt.After(lastSyncTime) {
			newSessions = append(newSessions, session)
		}
	}

	sort.Slice(newSessions, func(i, j int) bool {
		return newSessions[i].UpdatedAt.After(newSessions[j].UpdatedAt)
	})

	result := &DetectionResult{
		NewSessions:        newSessions,
		LastSyncCommitTime: lastSyncTime,
	}
	if len(newSessions) >= 2 {
		result.ForkSessionID = newSessions[0].SessionID
	}
	return result, nil
}

// DefaultSessionLister lists sessions using `mct-agent session list --json`.
func DefaultSessionLister(ctx context.Context, repoDir string) ([]SessionInfo, error) {
	command := exec.CommandContext(ctx, "mct-agent", "session", "list", "--json")
	command.Dir = repoDir

	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf(
			"mct-agent session list: %w: %s",
			err,
			strings.TrimSpace(string(output)),
		)
	}

	var sessions []sessionRecord
	if err := json.Unmarshal(output, &sessions); err != nil {
		var wrapped struct {
			Sessions []sessionRecord `json:"sessions"`
		}
		if err := json.Unmarshal(output, &wrapped); err != nil {
			return nil, fmt.Errorf("parse session list output: %w", err)
		}
		sessions = wrapped.Sessions
	}

	decoded := make([]SessionInfo, 0, len(sessions))
	for _, raw := range sessions {
		parsedTime, err := time.Parse(time.RFC3339, raw.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse session updated_at %q: %w", raw.UpdatedAt, err)
		}
		decoded = append(decoded, SessionInfo{
			SessionID: raw.SessionID,
			UpdatedAt: parsedTime,
			Goal:      raw.Goal,
		})
	}
	return decoded, nil
}

// DefaultGitLastCommitTime returns the timestamp of the project commit most
// recently processed by mct-agent's internal-README sync. If the project has
// not been synced yet, it returns the first commit timestamp so later project
// commits do not erase unsummarized session activity.
func DefaultGitLastCommitTime(repoDir string) (time.Time, error) {
	if strings.TrimSpace(repoDir) == "" {
		return time.Time{}, fmt.Errorf("entry-point repo path is required")
	}

	info, err := os.Stat(repoDir)
	if err != nil {
		return time.Time{}, fmt.Errorf("entry-point repo access: %w", err)
	}
	if !info.IsDir() {
		return time.Time{}, fmt.Errorf("entry-point repo path is not a directory: %s", repoDir)
	}

	checkRepo := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	checkRepo.Dir = repoDir
	if err := checkRepo.Run(); err != nil {
		return time.Time{}, fmt.Errorf("entry-point path is not a git repository: %w", err)
	}

	commit, found, err := lastSyncedProjectCommit(repoDir)
	if err != nil {
		return time.Time{}, err
	}
	if !found {
		commit, err = firstProjectCommit(repoDir)
		if err != nil {
			return time.Time{}, err
		}
		if commit == "" {
			return time.Time{}, nil
		}
	}
	return projectCommitTime(repoDir, commit)
}

func lastSyncedProjectCommit(repoDir string) (string, bool, error) {
	markerPath := filepath.Join(repoDir, ".machtiani", "project.uuid")
	marker, err := os.ReadFile(markerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read entry-point project marker: %w", err)
	}
	uuid := strings.TrimSpace(string(marker))
	if uuid == "" || filepath.Base(uuid) != uuid || strings.ContainsAny(uuid, `/\\`) {
		return "", false, fmt.Errorf("entry-point project marker contains an invalid UUID")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, fmt.Errorf("resolve home directory: %w", err)
	}
	lastCommitPath := filepath.Join(
		home,
		".machtiani",
		uuid,
		"artifacts",
		"readme",
		".state",
		"last_project_commit",
	)
	data, err := os.ReadFile(lastCommitPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read internal-README sync marker: %w", err)
	}
	commit := strings.TrimSpace(string(data))
	if commit == "" {
		return "", false, nil
	}
	return commit, true, nil
}

func firstProjectCommit(repoDir string) (string, error) {
	command := exec.Command("git", "rev-list", "--max-parents=0", "HEAD")
	command.Dir = repoDir
	output, err := command.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(output))
		if strings.Contains(text, "does not have any commits yet") ||
			strings.Contains(text, "bad revision 'HEAD'") ||
			strings.Contains(text, "unknown revision") {
			return "", nil
		}
		return "", fmt.Errorf("determine first project commit: %w: %s", err, text)
	}
	commits := strings.Fields(string(output))
	if len(commits) == 0 {
		return "", nil
	}
	return commits[0], nil
}

func projectCommitTime(repoDir, commit string) (time.Time, error) {
	command := exec.Command("git", "show", "-s", "--format=%cI", commit)
	command.Dir = repoDir
	output, err := command.CombinedOutput()
	if err != nil {
		return time.Time{}, fmt.Errorf(
			"determine sync commit time for %s: %w: %s",
			commit,
			err,
			strings.TrimSpace(string(output)),
		)
	}
	text := strings.TrimSpace(string(output))
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse last sync commit time %q: %w", text, err)
	}
	return parsed, nil
}
