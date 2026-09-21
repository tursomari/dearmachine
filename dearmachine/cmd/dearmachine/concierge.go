package main

import (
	"errors"
	"fmt"
	"github.com/dearmachine/dearmachine/internal/hostos"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/supervisor"
)

const conciergeRelaunchExitCode = 75

// Detection is deliberately independent of daemon liveness. A partial or
// inaccessible installation must never route into a fresh setup implicitly.
func detectInstallation(home string) string {
	state := detectStateRoot(filepath.Join(home, ".dearmachine"))
	if state != "absent" {
		return state
	}
	// Windows uninstall preserves independent Machtiani projects and modes.
	// Those stores do not establish a DearMachine installation or block setup.
	if runtime.GOOS == "windows" {
		return "absent"
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
	if !info.IsDir() || !hostos.Owned(root, info) {
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
	if errors.As(err, &childExit) && childExit.code == conciergeRelaunchExitCode {
		launcher, validateErr := managedRelaunchPath(home, getenv)
		if validateErr != nil {
			_, _ = fmt.Fprintln(output, "The update is active, but the managed launcher could not be validated. Run ~/.local/bin/dearmachine to open the updated concierge.")
			return nil
		}
		execProcess := deps.execProcess
		if execProcess == nil {
			execProcess = hostos.Exec
		}
		return execProcess(launcher, []string{launcher}, cleanRelaunchEnvironment(os.Environ()))
	}
	if err != nil && !errors.As(err, &childExit) {
		guidance()
	}
	return err
}

func managedRelaunchPath(home string, getenv func(string) string) (string, error) {
	launcher := filepath.Join(home, ".local", "bin", "dearmachine")
	dataHome := getenv("DEARMACHINE_MANAGED_DATA_HOME")
	if dataHome == "" {
		dataHome = getenv("XDG_DATA_HOME")
	}
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	if !filepath.IsAbs(dataHome) {
		return "", errors.New("managed data home is not absolute")
	}
	expected := filepath.Join(dataHome, "dearmachine", "current", "bin", "dearmachine")
	info, err := os.Lstat(launcher)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return "", errors.New("managed launcher is not a symbolic link")
	}
	target, err := os.Readlink(launcher)
	if err != nil || target != expected {
		return "", errors.New("managed launcher target is unexpected")
	}
	resolved, err := os.Stat(launcher)
	if err != nil || !resolved.Mode().IsRegular() || resolved.Mode().Perm()&0111 == 0 {
		return "", errors.New("managed launcher target is not executable")
	}
	return launcher, nil
}

func cleanRelaunchEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		if strings.HasPrefix(entry, "DEARMACHINE_NATIVE_BIN=") || strings.HasPrefix(entry, "DEARMACHINE_CONCIERGE_BIN=") ||
			strings.HasPrefix(entry, "DEARMACHINE_SOURCE_ROOT=") {
			continue
		}
		result = append(result, entry)
	}
	return result
}

type conciergeExitError struct {
	code  int
	cause error
}

func (e *conciergeExitError) Error() string { return e.cause.Error() }
func (e *conciergeExitError) Unwrap() error { return e.cause }

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

// The launching native CLI is authoritative for later local control and consent.
func conciergeEnvironment(environment []string, native string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, "DEARMACHINE_NATIVE_BIN=") {
			result = append(result, entry)
		}
	}
	return append(result, "DEARMACHINE_NATIVE_BIN="+native)
}
