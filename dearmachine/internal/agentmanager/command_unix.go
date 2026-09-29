//go:build !windows

package agentmanager

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const shellLookPathTimeout = 10 * time.Second

// Service managers start Agent Manager with a minimal PATH, so backends
// installed through shell-managed toolchains such as nvm are invisible to
// exec.LookPath. Fall back to the user's interactive shell, which is where
// those toolchains add themselves to PATH.
func backendLookPath(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err == nil || strings.ContainsRune(name, '/') {
		return path, err
	}
	if shellPath, ok := shellLookPath(name); ok {
		return shellPath, nil
	}
	return "", err
}

func shellLookPath(name string) (string, bool) {
	shell := os.Getenv("SHELL")
	if !filepath.IsAbs(shell) {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), shellLookPathTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, shell, "-ic", `command -v -- "$1"`, "_", name)
	output, err := command.Output()
	if err != nil {
		return "", false
	}
	// Interactive startup files may print banners; the lookup result is the
	// last line.
	lines := strings.Split(strings.TrimSpace(string(bytes.ReplaceAll(output, []byte("\r"), nil))), "\n")
	path := strings.TrimSpace(lines[len(lines)-1])
	if !filepath.IsAbs(path) {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return "", false
	}
	return path, true
}
