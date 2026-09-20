package agentmanager

import (
	"context"
	"os/exec"
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
	return command
}
