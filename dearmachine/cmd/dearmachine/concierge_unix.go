//go:build !windows

package main

import (
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// Foreground gives the child its own terminal process group, so terminal SIGINT
// goes directly to TS. The parent does not handle or forward SIGINT, daemonize,
// or kill the child on parent death. Wait restores the caller's foreground group.
func launchConciergeForeground(binary string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	terminal, ok := stdin.(*os.File)
	if !ok {
		return errors.New("concierge launch requires a terminal file")
	}
	fd := int(terminal.Fd())
	group, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil {
		return fmt.Errorf("read terminal foreground group: %w", err)
	}
	cmd := exec.Command(binary, args...)
	native, err := os.Executable()
	if err != nil {
		return err
	}
	cmd.Env = conciergeEnvironment(os.Environ(), native)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Foreground: true, Ctty: fd}
	// A background/orphaned parent must ignore SIGTTOU while restoring the
	// terminal. Do this only after the child has exited, preserving its signals.
	defer func() {
		ignored := signal.Ignored(syscall.SIGTTOU)
		signal.Ignore(syscall.SIGTTOU)
		defer func() {
			if !ignored {
				signal.Reset(syscall.SIGTTOU)
			}
		}()
		_ = unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, group)
	}()
	if err := cmd.Start(); err != nil {
		return err
	}
	err = cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			code = 128 + int(status.Signal())
		}
		return &conciergeExitError{code: code, cause: err}
	}
	return err
}
