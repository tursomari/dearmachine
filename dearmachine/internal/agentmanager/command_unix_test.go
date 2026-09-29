//go:build !windows

package agentmanager

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestBackendLookPathFallsBackToInteractiveShell(t *testing.T) {
	dir := t.TempDir()
	toolDir := filepath.Join(dir, "nvm", "bin")
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(toolDir, "fake-backend")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	shell := filepath.Join(dir, "shell")
	script := "#!/bin/sh\n[ \"$1\" = -ic ] || exit 2\necho 'welcome banner'\n[ \"$4\" = fake-backend ] && echo " + tool + "\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(dir, "empty"))
	t.Setenv("SHELL", shell)

	path, err := backendLookPath("fake-backend")
	if err != nil || path != tool {
		t.Fatalf("backendLookPath() = %q, %v; want %q", path, err, tool)
	}
	command := backendCommandContext(context.Background(), "fake-backend")
	if command.Err != nil || command.Path != tool {
		t.Fatalf("command = %q, %v; want %q", command.Path, command.Err, tool)
	}
	var paths []string
	for _, entry := range command.Environ() {
		if len(entry) > 5 && entry[:5] == "PATH=" {
			paths = filepath.SplitList(entry[5:])
		}
	}
	if !slices.Contains(paths, toolDir) {
		t.Fatalf("command PATH %q does not include %q", paths, toolDir)
	}
	if _, err := backendLookPath("missing-backend"); err == nil {
		t.Fatal("backendLookPath(missing-backend) succeeded")
	}
}
