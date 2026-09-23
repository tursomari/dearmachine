package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/dearmachine/dearmachine/internal/hostos"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/supervisor"
)

func stateFromLock(lockPath string) string { return filepath.Dir(filepath.Dir(lockPath)) }

func startBackground(args []string, logPath string) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	return startSelected(executable, args, filepath.Dir(filepath.Dir(logPath)))
}

func startSupervised(executable string, args []string, root string) (int, error) {
	directory, err := os.Getwd()
	if err != nil {
		return 0, err
	}
	argv := append([]string{executable}, args...)
	if s, err := supervisor.Request(root, "status", time.Second); err == nil {
		if err := checkOtherOwner(root, s); err != nil {
			return 0, err
		}
		s, err := supervisor.RequestUpInDirectory(root, argv, directory, daemonStartupTimeout+5*time.Second)
		return s.DaemonPID, err
	}
	// Refuse a foreign daemon before creating a supervisor record, but allow
	// a concurrent supervisor to finish publishing its control endpoint.
	owned, err := supervisor.OwnerPresent(root)
	if err != nil {
		return 0, err
	}
	if !owned {
		if err := checkOtherOwner(root, supervisor.Status{}); err != nil {
			return 0, err
		}
	}
	available := supervisor.CheckAvailable(root)
	if available != nil && !errors.Is(available, supervisor.ErrLocked) {
		return 0, fmt.Errorf("supervisor control unavailable: %w", available)
	}
	if available == nil {
		ownerArgs := append([]string{executable, "_supervise", "--state-dir", root, "--"}, argv...)
		if _, err := supervisor.StartDetached(root, ownerArgs); err != nil {
			return 0, err
		}
	}
	// A concurrent starter may hold the lock before publishing its socket.
	// Join that owner instead of creating another child or failing prematurely.
	deadline := time.Now().Add(daemonStartupTimeout)
	for time.Now().Before(deadline) {
		if s, err := supervisor.Request(root, "status", 100*time.Millisecond); err == nil {
			if err := checkOtherOwner(root, s); err != nil {
				return 0, err
			}
			s, err := supervisor.RequestUpInDirectory(root, argv, directory, daemonStartupTimeout+5*time.Second)
			return s.DaemonPID, err
		}
		time.Sleep(25 * time.Millisecond)
	}
	return 0, fmt.Errorf("supervisor did not become reachable; inspect %s", supervisor.LogPath(root))
}

func runSupervise(args []string, deps dependencies) error {
	flags := flag.NewFlagSet("_supervise", flag.ContinueOnError)
	flags.SetOutput(outputOrDiscard(deps.flagOutput))
	root := flags.String("state-dir", "", "private installation state directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || flags.NArg() == 0 {
		return errors.New("_supervise requires --state-dir and -- foreground-command [args]")
	}
	ctx, cancel := hostos.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ready := func(pid int) bool {
		expected := strconv.Itoa(pid)
		for _, name := range []string{"dearmachine.pid", "dearmachine.ready"} {
			data, err := os.ReadFile(filepath.Join(*root, "run", name))
			if err != nil || strings.TrimSpace(string(data)) != expected {
				return false
			}
		}
		return true
	}
	beforeStart := func() error {
		_, running, err := client.DaemonStatus(filepath.Join(*root, "run", "dearmachine.pid"))
		if err != nil {
			return err
		}
		if running {
			return errors.New("another foreground or service daemon owns this installation")
		}
		return nil
	}
	return supervisor.Run(ctx, supervisor.Config{StateDir: *root, Command: flags.Args(), Ready: ready, Persistence: nativeServiceManager(filepath.Dir(*root)).persistence, BeforeStart: beforeStart, Installation: func() string { return detectStateRoot(*root) }})
}

func managedDaemonStatus(lockPath string) (int, bool, error) {
	root := stateFromLock(lockPath)
	present, err := supervisor.HasRecord(root)
	if err != nil {
		return 0, false, err
	}
	if !present {
		return client.DaemonStatus(lockPath)
	}
	s, err := querySupervisor(root)
	if err != nil {
		return 0, false, err
	}
	if s.Installation != "installed" {
		return s.DaemonPID, false, fmt.Errorf("installation is %s; inspect setup before repairing", s.Installation)
	}
	if s.Supervisor == "failed" || s.Daemon == "unknown" {
		return s.DaemonPID, false, fmt.Errorf("supervisor is %s; inspect status and daemon log", s.Supervisor)
	}
	return s.DaemonPID, s.Daemon == "running", nil
}

func stopManagedDaemon(lockPath string, timeout time.Duration) error {
	root := stateFromLock(lockPath)
	present, err := supervisor.HasRecord(root)
	if err != nil {
		return err
	}
	if !present {
		return client.StopDaemon(lockPath, timeout)
	}
	s, err := supervisor.Request(root, "down", timeout)
	if err != nil {
		return err
	}
	if err := checkOtherOwner(root, s); err != nil {
		return err
	}
	if s.Supervisor != "stopped" || s.Daemon != "stopped" {
		return errors.New("supervisor stop was not confirmed; inspect status")
	}
	return nil
}

func runRestart(args []string, getenv func(string) string, deps dependencies) error {
	flags := flag.NewFlagSet("restart", flag.ContinueOnError)
	flags.SetOutput(outputOrDiscard(deps.flagOutput))
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected restart arguments: %v", flags.Args())
	}
	lockPath, err := client.DefaultDaemonLockPath(deps.userHomeDir)
	if err != nil {
		return err
	}
	root := stateFromLock(lockPath)
	present, err := supervisor.HasRecord(root)
	if err != nil {
		return err
	}
	if !present {
		status := deps.daemonStatus
		if status == nil {
			status = managedDaemonStatus
		}
		_, running, err := status(lockPath)
		if err != nil {
			return err
		}
		if running {
			return errors.New("daemon has another owner; use its existing foreground or service lifecycle to restart")
		}
		return runUp(nil, getenv, deps)
	}
	s, err := supervisor.Request(root, "restart", daemonStartupTimeout+5*time.Second)
	if err != nil {
		return err
	}
	if s.Supervisor != "running" || s.Daemon != "running" {
		return errors.New("restart was not confirmed; inspect status")
	}
	_, err = fmt.Fprintf(outputOrDiscard(deps.stdout), "Restarted DearMachine (PID %d).\n", s.DaemonPID)
	return err
}

// A stopped resident supervisor must not mask a foreground/service process.
// Report conflicting ownership without signalling a PID read from a file.
func checkOtherOwner(root string, s supervisor.Status) error {
	pid, running, err := client.DaemonStatus(filepath.Join(root, "run", "dearmachine.pid"))
	if err != nil {
		return err
	}
	if running && pid != s.DaemonPID {
		return errors.New("daemon has another foreground or service owner; inspect that owner's lifecycle before switching supervision")
	}
	return nil
}

func querySupervisor(root string) (supervisor.Status, error) {
	s, err := supervisor.Request(root, "status", 5*time.Second)
	if err != nil {
		return s, err
	}
	return s, checkOtherOwner(root, s)
}

// Headless bootstrap validates native state without setup, prompts or provider work.
// The detached owner's flock remains the only supervisor ownership authority.
func runBootstrap(getenv func(string) string, deps dependencies) error {
	home, err := deps.userHomeDir()
	if err != nil {
		return err
	}
	root := filepath.Join(home, ".dearmachine")
	if override := getenv("DEARMACHINE_SUPERVISOR_SOCKET"); override != "" && override != supervisor.SocketPath(root) {
		return errors.New("bootstrap socket must match HOME/.dearmachine/run/supervisor.sock; custom owners must be started explicitly")
	}
	if detectInstallation(home) != "installed" {
		return errors.New("no verified installation; inspect dearmachine status and setup before starting")
	}
	starter := deps.startBackground
	if starter == nil {
		starter = startBackground
	}
	_, err = starter([]string{"up", "--foreground"}, supervisor.LogPath(root))
	return err
}
