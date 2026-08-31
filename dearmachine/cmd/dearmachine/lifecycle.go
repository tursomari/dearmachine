package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dearmachine/dearmachine/internal/client"
)

const daemonStartupTimeout = 15 * time.Second

func startBackground(args []string, logPath string) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("locate dearmachine executable: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, fmt.Errorf("create log directory: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open daemon log: %w", err)
	}
	command := exec.Command(executable, args...)
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return 0, fmt.Errorf("start background client: %w", err)
	}
	pid := command.Process.Pid
	if err := command.Process.Release(); err != nil {
		_ = logFile.Close()
		return 0, fmt.Errorf("release background client PID %d: %w", pid, err)
	}
	if err := logFile.Close(); err != nil {
		return 0, fmt.Errorf("close daemon log: %w", err)
	}
	return pid, nil
}

func runStatus(args []string, deps dependencies) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(outputOrDiscard(deps.flagOutput))
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected status arguments: %v", flags.Args())
	}
	lockPath, err := client.DefaultDaemonLockPath(deps.userHomeDir)
	if err != nil {
		return err
	}
	status := deps.daemonStatus
	if status == nil {
		status = client.DaemonStatus
	}
	pid, running, err := status(lockPath)
	if err != nil {
		return err
	}
	output := outputOrDiscard(deps.stdout)
	if running {
		_, err = fmt.Fprintf(output, "DearMachine is running (PID %d).\n", pid)
	} else {
		_, err = fmt.Fprintln(output, "DearMachine is stopped.")
	}
	if err != nil {
		return err
	}
	registryPath, err := client.DefaultPairRegistryPath(deps.userHomeDir)
	if err != nil {
		return err
	}
	registry, err := client.LoadPairRegistry(registryPath)
	if err != nil {
		return err
	}
	return printRegistry(output, registry)
}

func runDown(args []string, deps dependencies) error {
	flags := flag.NewFlagSet("down", flag.ContinueOnError)
	flags.SetOutput(outputOrDiscard(deps.flagOutput))
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected down arguments: %v", flags.Args())
	}
	lockPath, err := client.DefaultDaemonLockPath(deps.userHomeDir)
	if err != nil {
		return err
	}
	stop := deps.stopDaemon
	if stop == nil {
		stop = client.StopDaemon
	}
	if err := stop(lockPath, daemonStartupTimeout); err != nil {
		if errors.Is(err, client.ErrDaemonStopped) {
			_, err = fmt.Fprintln(outputOrDiscard(deps.stdout), "DearMachine is already stopped.")
			return err
		}
		return err
	}
	_, err = fmt.Fprintln(outputOrDiscard(deps.stdout), "Stopped DearMachine.")
	return err
}
