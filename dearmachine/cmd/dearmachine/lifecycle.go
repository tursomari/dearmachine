package main

import (
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/supervisor"
)

const daemonStartupTimeout = 15 * time.Second

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
		status = managedDaemonStatus
	}
	present, err := supervisor.HasRecord(stateFromLock(lockPath))
	if err != nil {
		return err
	}
	if present {
		s, err := querySupervisor(stateFromLock(lockPath))
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(outputOrDiscard(deps.stdout), "Installation: %s. Supervisor: %s. Daemon: %s (PID %d). Persistence: %s.\n", s.Installation, s.Supervisor, s.Daemon, s.DaemonPID, s.Persistence)
		if err != nil {
			return err
		}
		if s.RetryInMs != nil {
			if _, err := fmt.Fprintf(outputOrDiscard(deps.stdout), "Retry in %d ms.\n", *s.RetryInMs); err != nil {
				return err
			}
		}
		if s.LastExit != "" {
			if _, err := fmt.Fprintf(outputOrDiscard(deps.stdout), "Last exit: %s\n", s.LastExit); err != nil {
				return err
			}
		}
		if s.Installation != "installed" {
			return fmt.Errorf("installation is %s; inspect setup before repairing", s.Installation)
		}
		if s.Supervisor == "failed" || s.Daemon == "unknown" {
			return fmt.Errorf("supervisor is %s; inspect daemon log", s.Supervisor)
		}
		registryPath, err := client.DefaultPairRegistryPath(deps.userHomeDir)
		if err != nil {
			return err
		}
		registry, err := client.LoadPairRegistry(registryPath)
		if err != nil {
			return err
		}
		return printRegistry(outputOrDiscard(deps.stdout), registry)
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
		stop = stopManagedDaemon
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
