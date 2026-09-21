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
	persist             func([]byte) error
}

func (j *sendmuxSubmissionJournal) save() error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return j.persist(data)
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
	data, persist, unlock, err := openSendmuxJournal(ctx, directory, key)
	if err != nil {
		return nil, nil, err
	}
	j := &sendmuxSubmissionJournal{Fingerprint: hash, persist: persist}
	if data == nil {
		return j, unlock, nil
	}
	if json.Unmarshal(data, j) != nil || j.Fingerprint != hash {
		unlock()
		return nil, nil, errors.New("Sendmux idempotency journal is invalid or belongs to a different reply")
	}
	return j, unlock, nil
}
