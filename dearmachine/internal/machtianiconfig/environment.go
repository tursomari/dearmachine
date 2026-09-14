// Package machtianiconfig selects DearMachine's private harness configuration.
package machtianiconfig

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func Apply(command *exec.Cmd) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	env := command.Env
	if env == nil {
		env = os.Environ()
	}
	result := make([]string, 0, len(env)+1)
	for _, value := range env {
		if !strings.HasPrefix(value, "MACHTIANI_CONFIG=") {
			result = append(result, value)
		}
	}
	command.Env = append(result, "MACHTIANI_CONFIG="+filepath.Join(home, ".config", "dearmachine", "machtiani", "config.toml"))
	return nil
}

// Migrate imports the former shared config before starting an existing native
// client. The native writer owns credential extraction and no-overwrite rules.
func Migrate(ctx context.Context, binary, home string) error {
	target := filepath.Join(home, ".config/dearmachine/machtiani/config.toml")
	if _, err := os.Lstat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	source := filepath.Join(home, ".machtiani/config.toml")
	if _, err := os.Stat(source); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	args := []string{"config", "import", "--source", source}
	file := filepath.Join(home, ".config/dearmachine/backends.env")
	if _, err := os.Stat(file); err == nil {
		args = append(args, "--credentials-file", file)
	} else if !os.IsNotExist(err) {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = home
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "HOME=") && !strings.HasPrefix(value, "MACHTIANI_CONFIG=") && !strings.HasPrefix(value, "MACHTIANI_UPDATE_REEXEC=") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, "HOME="+home, "MACHTIANI_CONFIG="+target, "MACHTIANI_UPDATE_REEXEC=1")
	if err := command.Run(); err != nil {
		return fmt.Errorf("could not import the legacy Machtiani configuration into DearMachine's private configuration: %w", err)
	}
	return nil
}
