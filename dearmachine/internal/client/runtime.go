package client

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const RuntimeConfigVersion = 1

// RuntimeConfig is the machine-global launch profile shared by every pair in
// the single DearMachine daemon. It contains no transport or provider secrets.
type RuntimeConfig struct {
	Version                int    `toml:"version"`
	Project                string `toml:"project"`
	Model                  string `toml:"model"`
	AgentBinary            string `toml:"agent_binary"`
	DeviceConfig           string `toml:"device_config"`
	ManagerPath            string `toml:"manager_path"`
	EntryPointRepo         string `toml:"entry_point_repo"`
	EntryPointPrompt       string `toml:"entry_point_prompt"`
	PollInterval           string `toml:"poll_interval"`
	Concurrency            int    `toml:"concurrency"`
	MaintenanceMinTurns    int    `toml:"maintenance_min_turns"`
	MaintenanceMinTurnsSet bool   `toml:"maintenance_min_turns_set"`
	MagnificaHumanitas     bool   `toml:"magnifica_humanitas"`
	MinimalFooter          bool   `toml:"minimal_footer"`
	Verbose                bool   `toml:"verbose"`
}

func DefaultRuntimeConfigPath(userHomeDir func() (string, error)) (string, error) {
	root, err := resolveDeviceHome(userHomeDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".dearmachine", "config", "runtime.toml"), nil
}

func LoadRuntimeConfig(path string) (RuntimeConfig, bool, error) {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return RuntimeConfig{}, false, nil
	}
	if err != nil {
		return RuntimeConfig{}, false, fmt.Errorf("read runtime config: %w", err)
	}
	var config RuntimeConfig
	metadata, err := toml.Decode(string(content), &config)
	if err != nil {
		return RuntimeConfig{}, false, fmt.Errorf("parse runtime config: %w", err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) != 0 {
		return RuntimeConfig{}, false, fmt.Errorf("runtime config contains unknown key %q", undecoded[0])
	}
	if err := validateRuntimeConfig(config); err != nil {
		return RuntimeConfig{}, false, err
	}
	return config, true, nil
}

func SaveRuntimeConfig(path string, config RuntimeConfig) error {
	if config.Version == 0 {
		config.Version = RuntimeConfigVersion
	}
	if err := validateRuntimeConfig(config); err != nil {
		return err
	}
	var content bytes.Buffer
	if err := toml.NewEncoder(&content).Encode(config); err != nil {
		return fmt.Errorf("encode runtime config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create runtime config directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("secure runtime config directory: %w", err)
	}
	if err := writeAtomicConfig(path, content.Bytes()); err != nil {
		return fmt.Errorf("write runtime config: %w", err)
	}
	return nil
}

func validateRuntimeConfig(config RuntimeConfig) error {
	if config.Version != RuntimeConfigVersion {
		return fmt.Errorf("runtime config version must be %d", RuntimeConfigVersion)
	}
	if strings.TrimSpace(config.Project) == "" {
		return fmt.Errorf("runtime config project is required")
	}
	if strings.TrimSpace(config.AgentBinary) == "" {
		return fmt.Errorf("runtime config agent binary is required")
	}
	if strings.TrimSpace(config.PollInterval) == "" {
		return fmt.Errorf("runtime config poll interval is required")
	}
	if config.Concurrency < 1 {
		return fmt.Errorf("runtime config concurrency must be at least 1")
	}
	if config.MaintenanceMinTurns < 0 {
		return fmt.Errorf("runtime config maintenance minimum turns must not be negative")
	}
	return nil
}
