package agentmanager

import (
	"errors"
	"os/exec"
)

func processWasSignaled(err error) bool { return false }
func recordProcessFailure(meta *Meta, err error) {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		meta.ExitCode = &code
	}
}
