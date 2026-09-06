package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/supervisor"
)

// Detection is deliberately independent of daemon liveness. A partial or
// inaccessible installation must never route into a fresh setup implicitly.
func detectInstallation(home string) string {
	state := detectStateRoot(filepath.Join(home, ".dearmachine"))
	if state != "absent" {
		return state
	}
	if _, err := os.Lstat(filepath.Join(home, ".machtiani")); os.IsNotExist(err) {
		return "absent"
	}
	return "partial"
}

func detectStateRoot(root string) string {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return "absent"
	}
	if err != nil {
		return "unreadable"
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || stat.Uid != uint32(os.Getuid()) {
		return "partial"
	}
	if info.Mode().Perm()&0500 != 0500 {
		return "unreadable"
	}
	// Check metadata before parsing; don't follow substituted registry files.
	registryPath := filepath.Join(root, "pairs.toml")
	info, err = os.Lstat(registryPath)
	if os.IsNotExist(err) {
		return "partial"
	}
	if err != nil {
		return "unreadable"
	}
	if !info.Mode().IsRegular() {
		return "partial"
	}
	if info.Mode().Perm()&0400 == 0 {
		return "unreadable"
	}
	registry, err := client.LoadPairRegistry(registryPath)
	if err != nil || len(registry.Pairs) == 0 {
		return "partial"
	}
	return "installed"
}

// runBare delegates the conversation to the existing TS entry. Detection does
// not inspect liveness or imply permission to install or start the daemon.
func runBare(getenv func(string) string, deps dependencies) error {
	if !inputIsInteractive(deps) || deps.outputInteractive == nil || !deps.outputInteractive(deps.stdout) {
		return globalHelp(deps.stdout)
	}
	home, err := deps.userHomeDir()
	if err != nil {
		return err
	}
	socket, err := supervisor.DefaultSocketPath(func() (string, error) { return home, nil })
	if err != nil {
		return err
	}
	output := outputOrDiscard(deps.stdout)
	state := detectInstallation(home)
	if state != "absent" && state != "installed" {
		_, _ = fmt.Fprintln(output, "Recovery: existing installation state is partial or unreadable. Inspect dearmachine status and dearmachine --help before repairing setup.")
		return errors.New("installation needs recovery; automatic fresh setup was not selected")
	}
	guidance := func() {
		if state == "absent" {
			_, _ = fmt.Fprintln(output, "Installer mode: run machtiani-installer --concierge --source-root <absolute-source-root> to open guided setup. Set DEARMACHINE_SOURCE_ROOT to that absolute source root for bare launch.")
		} else {
			_, _ = fmt.Fprintf(output, "Concierge mode: an installation is configured. Run machtiani-installer --concierge for local management, or use dearmachine status, up, down, or restart. Native control: %s\n", socket)
		}
		_, _ = fmt.Fprintln(output, "Set DEARMACHINE_CONCIERGE_BIN to the concierge executable, or install machtiani-installer on PATH.")
	}
	resolved, err := discoverConcierge(getenv("DEARMACHINE_CONCIERGE_BIN"), home, deps.lookPath)
	if err != nil {
		guidance()
		return fmt.Errorf("find concierge executable: %w", err)
	}
	args := []string{"--concierge"}
	if state == "absent" {
		source := getenv("DEARMACHINE_SOURCE_ROOT")
		if source != "" {
			if !filepath.IsAbs(source) {
				guidance()
				return errors.New("DEARMACHINE_SOURCE_ROOT must be absolute")
			}
			args = append(args, "--source-root", source)
		}
	}
	launch := deps.launchConcierge
	if launch == nil {
		launch = launchConciergeForeground
	}
	err = launch(resolved, args, deps.stdin, deps.stdout, deps.flagOutput)
	var childExit *conciergeExitError
	if err != nil && !errors.As(err, &childExit) {
		guidance()
	}
	return err
}

type conciergeExitError struct {
	code  int
	cause error
}

func (e *conciergeExitError) Error() string { return e.cause.Error() }
func (e *conciergeExitError) Unwrap() error { return e.cause }

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

// An explicit override is authoritative, even when it cannot be executed.
func discoverConcierge(override, home string, lookPath func(string) (string, error)) (string, error) {
	if lookPath == nil {
		return "", errors.New("concierge executable is unavailable")
	}
	if override != "" {
		if !filepath.IsAbs(override) && filepath.Base(override) != override {
			return "", errors.New("DEARMACHINE_CONCIERGE_BIN must be an absolute path or PATH executable name")
		}
		return lookPath(override)
	}
	var err error
	for _, candidate := range []string{"machtiani-installer", filepath.Join(home, ".local", "bin", "machtiani-installer"), filepath.Join(home, ".nix-profile", "bin", "machtiani-installer")} {
		var resolved string
		resolved, err = lookPath(candidate)
		if err == nil {
			return resolved, nil
		}
	}
	return "", err
}
