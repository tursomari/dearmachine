package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/supervisor"
)

func runUpdate(args []string, getenv func(string) string, deps dependencies) error {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(outputOrDiscard(deps.flagOutput))
	check := flags.Bool("check", false, "check the coordinated release without changing the installation")
	recover := flags.Bool("recover", false, "restore the previous release after interrupted activation")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*check && *recover) {
		return errors.New("usage: dearmachine update [--check | --recover]")
	}
	home, err := deps.userHomeDir()
	if err != nil {
		return err
	}
	binary, err := discoverConcierge(getenv("DEARMACHINE_CONCIERGE_BIN"), home, deps.lookPath)
	if err != nil {
		return fmt.Errorf("managed updater requires machtiani-installer: %w", err)
	}
	command := exec.Command(binary, append([]string{"update"}, args...)...)
	command.Stdin, command.Stdout, command.Stderr = deps.stdin, deps.stdout, deps.flagOutput
	return command.Run()
}

// Private versioned handoff for the coordinated installer. It changes no pair,
// model, transport, or persistence choices. Unreachable owners fail closed.
func runUpdateControl(args []string, deps dependencies) error {
	if len(args) != 1 {
		return errors.New("_update-control requires status, stop, or refresh")
	}
	home, err := deps.userHomeDir()
	if err != nil {
		return err
	}
	root := filepath.Join(home, ".dearmachine")
	manager := nativeServiceManager(home)
	consent, err := manager.load()
	if err != nil {
		return err
	}
	if args[0] == "refresh" {
		if consent.UseSystemd {
			return manager.configure("systemd", "on")
		}
		return nil
	}
	if args[0] != "status" && args[0] != "stop" {
		return errors.New("unknown update control")
	}
	present, err := supervisor.HasRecord(root)
	if err != nil {
		return err
	}
	running := false
	if present {
		s, err := querySupervisor(root)
		if err != nil {
			// A shutdown leaves its lock file behind. Probe the lock before
			// treating an unreachable socket as stopped; never trust a stale PID.
			if available := supervisor.CheckAvailable(root); available != nil {
				return err
			}
			_, live, probeErr := client.DaemonStatus(filepath.Join(root, "run", "dearmachine.pid"))
			if probeErr != nil {
				return probeErr
			}
			if live {
				return errors.New("a foreground client still owns this installation")
			}
			s.Daemon = "stopped"
			present = false
		}
		if s.Daemon != "running" && s.Daemon != "stopped" {
			return errors.New("client state is uncertain; inspect dearmachine status before updating")
		}
		running = s.Daemon == "running"
		if args[0] == "stop" && present {
			if consent.UseSystemd {
				if _, err := manager.run("systemctl", "--user", "stop", conciergeUnit); err != nil {
					return err
				}
			} else {
				if _, err := supervisor.Request(root, "shutdown", 5*time.Second); err != nil {
					return err
				}
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if err := supervisor.CheckAvailable(root); err == nil {
					return nil
				}
				time.Sleep(25 * time.Millisecond)
			}
			return errors.New("supervisor shutdown was not confirmed")
		}
	} else {
		_, running, err = client.DaemonStatus(filepath.Join(root, "run", "dearmachine.pid"))
		if err != nil {
			return err
		}
		if running {
			return errors.New("client is not owned by the native supervisor; stop it before updating")
		}
	}
	if args[0] == "status" {
		return json.NewEncoder(outputOrDiscard(deps.stdout)).Encode(map[string]bool{"running": running})
	}
	return nil
}
