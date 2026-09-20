//go:build !windows

package client

import (
	"os"
	"path/filepath"
)

func commitAtomicConfig(temporaryPath, path string) error {
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
