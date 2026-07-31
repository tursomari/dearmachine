package deviceclient

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func createPIDFile(path string) (func() error, error) {
	if path == "" {
		return func() error { return nil }, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create pidfile directory: %w", err)
	}
	content := strconv.Itoa(os.Getpid()) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("write pidfile %s: %w", path, err)
	}

	return func() error {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove pidfile %s: %w", path, err)
		}
		return nil
	}, nil
}
