package agentmanager

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
)

func backendCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	path, err := backendLookPath(name)
	if err != nil {
		command := exec.CommandContext(ctx, name, args...)
		command.Err = err
		return command
	}
	command := exec.CommandContext(ctx, path, args...)
	command.Args[0] = name
	// A backend found outside PATH (for example through the user's shell)
	// may need sibling programs such as node from its own directory.
	dir := filepath.Dir(path)
	if current := os.Getenv("PATH"); !slices.Contains(filepath.SplitList(current), dir) {
		command.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+current)
	}
	return command
}
