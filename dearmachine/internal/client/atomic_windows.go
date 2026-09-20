package client

import (
	"errors"
	"golang.org/x/sys/windows"
	"path/filepath"
	"strings"
)

// The caller has flushed and closed the temporary file. Windows cannot fsync
// an ordinary directory handle; publish with the native write-through move.
func commitAtomicConfig(temporaryPath, path string) error {
	from, err := windowsPath(temporaryPath)
	if err != nil {
		return err
	}
	to, err := windowsPath(path)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
func windowsPath(path string) (*uint16, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(absolute, `\\?\`) {
		if strings.HasPrefix(absolute, `\\`) {
			absolute = `\\?\UNC\` + absolute[2:]
		} else {
			absolute = `\\?\` + absolute
		}
	}
	return windows.UTF16PtrFromString(absolute)
}

// Sendmux's submission journal requires a durable directory-creation barrier.
// Keep that unverified transport fail-closed in the native Windows proof.
func syncDirectory(string) error {
	return errors.New("durable directory synchronization is not supported on Windows")
}
