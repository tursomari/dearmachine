package synctrigger

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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

// GitLastCommitTime returns the timestamp of the latest commit in a repo.
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

// DefaultGitLastCommitTime returns the latest commit timestamp for the repo using git.
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

	command := exec.Command("git", "log", "-1", "--format=%cI")
	command.Dir = repoDir
	output, err := command.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(output))
		if text == "" ||
			strings.Contains(text, "does not have any commits yet") ||
			strings.Contains(text, "bad revision 'HEAD'") ||
			strings.Contains(text, "unknown revision") {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("determine last sync commit time: %w: %s", err, text)
	}

	text := strings.TrimSpace(string(output))
	if text == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse last sync commit time %q: %w", text, err)
	}
	return parsed, nil
}
