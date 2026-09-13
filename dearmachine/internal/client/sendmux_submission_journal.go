package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Persist identifiers and fingerprints only. An uncertain creation can be
// recovered by a positive provider receipt, never by treating absence as proof
// of failure: native mailbox reads can temporarily lag accepted mutations.
type sendmuxSubmissionJournal struct {
	Fingerprint         string `json:"fingerprint"`
	Account             string `json:"account"`
	EmailAttempted      bool   `json:"email_attempted"`
	EmailID             string `json:"email_id"`
	SubmissionAttempted bool   `json:"submission_attempted"`
	SubmissionID        string `json:"submission_id"`
	path                string
}

func (j *sendmuxSubmissionJournal) save() error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return writeAtomicConfig(j.path, data)
}
func (s *sendmuxJMAPSender) openSubmissionJournal(ctx context.Context, key, hash string) (*sendmuxSubmissionJournal, func(), error) {
	directory := s.journalDir
	if directory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, err
		}
		directory = filepath.Join(home, ".dearmachine", "state", "sendmux-submissions")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, nil, err
	}
	if err := syncDirectory(filepath.Dir(directory)); err != nil {
		return nil, nil, err
	}
	path := filepath.Join(directory, key+".json")
	unlock, err := privateFileLock(ctx, path+".lock")
	if err != nil {
		return nil, nil, err
	}
	j := &sendmuxSubmissionJournal{Fingerprint: hash, path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return j, unlock, nil
	}
	if err != nil {
		unlock()
		return nil, nil, err
	}
	if json.Unmarshal(data, j) != nil || j.Fingerprint != hash {
		unlock()
		return nil, nil, errors.New("Sendmux idempotency journal is invalid or belongs to a different reply")
	}
	return j, unlock, nil
}
