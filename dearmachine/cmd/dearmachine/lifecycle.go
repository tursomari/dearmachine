package main

import (
	"encoding/json"
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
	asJSON := flags.Bool("json", false, "show read-only installation and runtime observations as JSON")
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
	s, managed, stateErr := observeStatus(lockPath, deps.daemonStatus)
	if *asJSON {
		// A successful observation can describe an unhealthy or unknown runtime.
		// Do not include pair identities or private exit diagnostics in this API.
		s.LastExit = ""
		return json.NewEncoder(outputOrDiscard(deps.stdout)).Encode(supervisor.Response{Version: 1, OK: true, Status: s})
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

// Installation validity is independent of whether its supervisor responds.
func observeStatus(lockPath string, legacyStatus func(string) (int, bool, error)) (supervisor.Status, bool, error) {
	root := stateFromLock(lockPath)
	s := supervisor.Status{Installation: detectStateRoot(root), Supervisor: "stopped", Daemon: "stopped", Persistence: "unknown"}
	present, err := supervisor.HasRecord(root)
	if err != nil {
		s.Supervisor, s.Daemon = "unreachable", "unknown"
		return s, false, err
	}
	if present {
		observed, queryErr := querySupervisor(root)
		if queryErr == nil {
			observed.Installation = s.Installation
			if s.Installation != "installed" {
				err = fmt.Errorf("installation is %s; inspect setup before repairing", s.Installation)
			} else if observed.Supervisor == "failed" || observed.Daemon == "unknown" {
				err = fmt.Errorf("supervisor is %s; inspect daemon log", observed.Supervisor)
			}
			return observed, true, err
		}
		// Clean shutdown leaves a lock file behind. Check ownership and the
		// foreground client before calling that leftover record stopped.
		owned, ownerErr := supervisor.OwnerPresent(root)
		if !owned && ownerErr == nil {
			_, running, daemonErr := client.DaemonStatus(lockPath)
			if !running && daemonErr == nil {
				return s, false, nil
			}
		}
		s.Supervisor, s.Daemon = "unreachable", "unknown"
		return s, false, queryErr
	}
	if legacyStatus == nil {
		legacyStatus = client.DaemonStatus
	}
	pid, running, err := legacyStatus(lockPath)
	if err != nil {
		s.Daemon = "unknown"
	} else if running {
		s.Daemon, s.DaemonPID = "running", pid
	}
	return s, false, err
}
