package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
)

func launchConciergeForeground(binary string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.Command(binary, args...)
	native, err := os.Executable()
	if err != nil {
		return err
	}
	cmd.Env = conciergeEnvironment(os.Environ(), native)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return &conciergeExitError{code: exit.ExitCode(), cause: err}
	}
	return err
}
