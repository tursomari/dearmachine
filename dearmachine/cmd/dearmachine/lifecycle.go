package main

import (
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"time"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/supervisor"
)

const daemonStartupTimeout = 15 * time.Second

func runStatus(args []string, deps dependencies) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(outputOrDiscard(deps.flagOutput))
	details := flags.Bool("details", false, "show PIDs, retry counts, service configuration, and saved permissions")
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
	s := supervisor.Status{Installation: detectStateRoot(stateFromLock(lockPath)), Supervisor: "not detected", Daemon: "stopped"}
	var stateErr error
	managed := false
	if present {
		s, stateErr = querySupervisor(stateFromLock(lockPath))
		managed = stateErr == nil
		if !managed {
			s.Supervisor, s.Daemon = "unreachable", "unknown"
		} else if s.Installation != "installed" {
			stateErr = fmt.Errorf("installation is %s; inspect setup before repairing", s.Installation)
		} else if s.Supervisor == "failed" || s.Daemon == "unknown" {
			stateErr = fmt.Errorf("supervisor is %s; inspect daemon log", s.Supervisor)
		}
	} else {
		pid, running, err := status(lockPath)
		if err != nil {
			s.Daemon, stateErr = "unknown", err
		} else if running {
			s.Daemon, s.DaemonPID = "running", pid
		}
	}
	output := outputOrDiscard(deps.stdout)
	m := nativeServiceManager(filepath.Dir(stateFromLock(lockPath)))
	if err := writeStatusReport(output, s, managed, m.observeStartup(), *details); err != nil {
		return err
	}
	if *details {
		if err := m.writeConsentDetails(output); err != nil {
			return err
		}
	}
	registryPath, err := client.DefaultPairRegistryPath(deps.userHomeDir)
	if err != nil {
		return err
	}
	registry, err := client.LoadPairRegistry(registryPath)
	if err != nil {
		return err
	}
	if *details {
		err = printRegistry(output, registry)
	} else {
		err = printStatusPairs(output, registry)
	}
	if err != nil {
		return err
	}
	return stateErr
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
