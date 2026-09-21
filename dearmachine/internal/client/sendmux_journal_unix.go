//go:build !windows

package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// Keep the existing Unix atomic-file and directory-fsync protocol unchanged.
func openSendmuxJournal(ctx context.Context, directory, key string) ([]byte, func([]byte) error, func(), error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, nil, nil, err
	}
	if err := syncDirectory(filepath.Dir(directory)); err != nil {
		return nil, nil, nil, err
	}
	path := filepath.Join(directory, key+".json")
	unlock, err := privateFileLock(ctx, path+".lock")
	if err != nil {
		return nil, nil, nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data, err = nil, nil
	}
	if err != nil {
		unlock()
		return nil, nil, nil, err
	}
	return data, func(data []byte) error { return writeAtomicConfig(path, data) }, unlock, nil
}
