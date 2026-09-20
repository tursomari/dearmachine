//go:build !windows

package agentmanager

import (
	"errors"
	"os/exec"
	"syscall"
)

func processWasSignaled(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	return ok && status.Signaled()
}

func recordProcessFailure(meta *Meta, err error) {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if ok && status.Signaled() {
		meta.Signal = status.Signal().String()
		return
	}
	exitCode := exitErr.ExitCode()
	if exitCode >= 0 {
		meta.ExitCode = &exitCode
	}
}
