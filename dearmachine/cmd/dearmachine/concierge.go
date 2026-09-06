package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

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

// runBare currently supplies an honest local handoff, with no provider session
// or daemon mutation. The TS launcher still needs native endpoint selection.
func runBare(deps dependencies) error {
	if !inputIsInteractive(deps) || deps.outputInteractive == nil || !deps.outputInteractive(deps.stdout) {
		return globalHelp(deps.stdout)
	}
	home, err := deps.userHomeDir()
	if err != nil {
		return err
	}
	if home == "" {
		return errors.New("user home directory is empty")
	}
	output := outputOrDiscard(deps.stdout)
	switch detectInstallation(home) {
	case "absent":
		_, err = fmt.Fprintln(output, "Installer mode: run machtiani-installer --concierge --source-root <absolute-source-root> to open guided setup. Source discovery and automatic handoff are pending.")
	case "installed":
		_, err = fmt.Fprintf(output, "Concierge mode: an installation is configured. Use dearmachine status, up, down, or restart for local management. The TS shell is available via machtiani-installer --concierge; native endpoint wiring is pending. Native control: %s\n", supervisor.SocketPath(filepath.Join(home, ".dearmachine")))
	default:
		_, _ = fmt.Fprintln(output, "Recovery: existing installation state is partial or unreadable. Inspect dearmachine status and dearmachine --help before repairing setup.")
		return errors.New("installation needs recovery; automatic fresh setup was not selected")
	}
	return err
}
