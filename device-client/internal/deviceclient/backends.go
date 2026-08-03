package deviceclient

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const DeviceConfigVersion = 1

type Backend struct {
	ID          string
	DisplayName string
	Executable  string
	InstallHelp string
}

type BackendDetection struct {
	Backend Backend
	Path    string
	Found   bool
}

type DeviceConfig struct {
	Version  int
	Backends []string
}

var backendRegistry = []Backend{
	{
		ID:          "codex",
		DisplayName: "Codex CLI",
		Executable:  "codex",
		InstallHelp: "Install Codex CLI, then ensure codex is on PATH.",
	},
	{
		ID:          "forgecode",
		DisplayName: "Forgecode",
		Executable:  "mct-forge",
		InstallHelp: "Install Forgecode and the mct-forge wrapper, then ensure mct-forge is on PATH.",
	},
	{
		ID:          "claude",
		DisplayName: "Claude Code",
		Executable:  "claude",
		InstallHelp: "Install Claude Code, then ensure claude is on PATH.",
	},
	{
		ID:          "pi",
		DisplayName: "Pi",
		Executable:  "pi",
		InstallHelp: "Install Pi, then ensure pi is on PATH.",
	},
}

func Backends() []Backend {
	return append([]Backend(nil), backendRegistry...)
}

func DetectBackends(lookPath func(string) (string, error)) []BackendDetection {
	detections := make([]BackendDetection, 0, len(backendRegistry))
	for _, backend := range backendRegistry {
		path, err := lookPath(backend.Executable)
		detections = append(detections, BackendDetection{
			Backend: backend,
			Path:    path,
			Found:   err == nil,
		})
	}
	return detections
}

// ResolveDelegationBackends validates the optional override against the user's
// approved list and performs a fresh PATH lookup for every selected backend.
func ResolveDelegationBackends(
	approved,
	override []string,
	lookPath func(string) (string, error),
) ([]BackendDetection, error) {
	selected, err := selectedBackendIDs(approved, override)
	if err != nil {
		return nil, err
	}

	registry := backendByID()
	available := make([]BackendDetection, 0, len(selected))
	for _, id := range selected {
		backend := registry[id]
		path, err := lookPath(backend.Executable)
		if err == nil {
			available = append(available, BackendDetection{
				Backend: backend,
				Path:    path,
				Found:   true,
			})
		}
	}
	return available, nil
}

func selectedBackendIDs(approved, override []string) ([]string, error) {
	if err := validateBackendIDs(approved, false); err != nil {
		return nil, fmt.Errorf("validate approved backends: %w", err)
	}
	if len(override) == 0 {
		return append([]string(nil), approved...), nil
	}
	if err := validateBackendIDs(override, false); err != nil {
		return nil, fmt.Errorf("validate backend override: %w", err)
	}
	approvedSet := make(map[string]struct{}, len(approved))
	for _, id := range approved {
		approvedSet[id] = struct{}{}
	}
	for _, id := range override {
		if _, ok := approvedSet[id]; !ok {
			return nil, fmt.Errorf("backend override %q was not approved by the user", id)
		}
	}
	return append([]string(nil), override...), nil
}

func DefaultDeviceConfigPath(userHomeDir func() (string, error)) (string, error) {
	root, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("user home directory is empty")
	}
	return filepath.Join(root, ".dearmachine", "config", "device-client.toml"), nil
}

func DefaultAgentManagerPath(userHomeDir func() (string, error)) (string, error) {
	root, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("user home directory is empty")
	}
	return filepath.Join(root, ".dearmachine", "agent-manager", "agent-manager"), nil
}

func LoadDeviceConfig(path string) (DeviceConfig, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return DeviceConfig{}, fmt.Errorf("read device config: %w", err)
	}
	config := DeviceConfig{}
	seenVersion := false
	seenBackends := false
	for lineNumber, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			return DeviceConfig{}, fmt.Errorf("parse device config line %d", lineNumber+1)
		}
		switch strings.TrimSpace(key) {
		case "version":
			if seenVersion {
				return DeviceConfig{}, fmt.Errorf("device config contains duplicate version")
			}
			config.Version, err = strconv.Atoi(strings.TrimSpace(value))
			seenVersion = true
		case "backends":
			if seenBackends {
				return DeviceConfig{}, fmt.Errorf("device config contains duplicate backends")
			}
			err = json.Unmarshal([]byte(strings.TrimSpace(value)), &config.Backends)
			seenBackends = true
		default:
			return DeviceConfig{}, fmt.Errorf("device config contains unknown key %q", strings.TrimSpace(key))
		}
		if err != nil {
			return DeviceConfig{}, fmt.Errorf("parse device config line %d: %w", lineNumber+1, err)
		}
	}
	if !seenVersion || config.Version != DeviceConfigVersion {
		return DeviceConfig{}, fmt.Errorf("device config version must be %d", DeviceConfigVersion)
	}
	if !seenBackends {
		return DeviceConfig{}, fmt.Errorf("device config backends are required")
	}
	if err := validateBackendIDs(config.Backends, false); err != nil {
		return DeviceConfig{}, fmt.Errorf("validate device config: %w", err)
	}
	return config, nil
}

func SaveDeviceConfig(path string, config DeviceConfig) error {
	if config.Version == 0 {
		config.Version = DeviceConfigVersion
	}
	if config.Version != DeviceConfigVersion {
		return fmt.Errorf("device config version must be %d", DeviceConfigVersion)
	}
	if err := validateBackendIDs(config.Backends, false); err != nil {
		return fmt.Errorf("validate device config: %w", err)
	}
	encodedBackends, err := json.Marshal(config.Backends)
	if err != nil {
		return fmt.Errorf("encode device config backends: %w", err)
	}
	content := []byte(fmt.Sprintf(
		"version = %d\nbackends = %s\n",
		config.Version,
		encodedBackends,
	))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create device config directory: %w", err)
	}
	if err := writeAtomicConfig(path, content); err != nil {
		return fmt.Errorf("write device config: %w", err)
	}
	return nil
}

func SetupAgents(
	input io.Reader,
	output io.Writer,
	configPath string,
	preferred []string,
	lookPath func(string) (string, error),
) (DeviceConfig, error) {
	if len(preferred) > 0 {
		if err := validateBackendIDs(preferred, false); err != nil {
			return DeviceConfig{}, fmt.Errorf("validate preferred backends: %w", err)
		}
	}
	detections := DetectBackends(lookPath)
	found := make(map[string]BackendDetection)
	defaultOrder := make([]string, 0, len(detections))
	fmt.Fprintln(output, "Coding agents on PATH:")
	for _, detection := range detections {
		if detection.Found {
			found[detection.Backend.ID] = detection
			defaultOrder = append(defaultOrder, detection.Backend.ID)
			fmt.Fprintf(output, "  found   %-10s %s\n", detection.Backend.ID, detection.Path)
		} else {
			fmt.Fprintf(output, "  missing %-10s (%s)\n", detection.Backend.ID, detection.Backend.Executable)
		}
	}
	if len(found) == 0 {
		fmt.Fprintln(output, "\nNo supported coding agents were found. Install help:")
		for _, backend := range backendRegistry {
			fmt.Fprintf(output, "  %s: %s\n", backend.DisplayName, backend.InstallHelp)
		}
		return DeviceConfig{}, fmt.Errorf("no supported coding agents found on PATH")
	}

	selection := defaultOrder
	if len(preferred) > 0 {
		for _, id := range preferred {
			if _, ok := found[id]; !ok {
				return DeviceConfig{}, fmt.Errorf("preferred backend %q was not detected on PATH", id)
			}
		}
		selection = append([]string(nil), preferred...)
	}
	fmt.Fprintf(output, "\nDefault priority order: %s\n", strings.Join(selection, ", "))
	reader := bufio.NewReader(input)
	fmt.Fprint(output, "Use these agents in this order? [Y/n] ")
	answer, err := readSetupLine(reader)
	if err != nil {
		return DeviceConfig{}, err
	}
	if answer != "" && !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
		if !strings.EqualFold(answer, "n") && !strings.EqualFold(answer, "no") {
			return DeviceConfig{}, fmt.Errorf("confirmation must be yes or no")
		}
		fmt.Fprint(output, "Enter a comma-separated subset in priority order: ")
		custom, err := readSetupLine(reader)
		if err != nil {
			return DeviceConfig{}, err
		}
		selection = splitBackendList(custom)
		if err := validateBackendIDs(selection, false); err != nil {
			return DeviceConfig{}, err
		}
		for _, id := range selection {
			if _, ok := found[id]; !ok {
				return DeviceConfig{}, fmt.Errorf("backend %q was not detected on PATH", id)
			}
		}
		fmt.Fprintf(output, "Use %s? [y/N] ", strings.Join(selection, ", "))
		confirmed, err := readSetupLine(reader)
		if err != nil {
			return DeviceConfig{}, err
		}
		if !strings.EqualFold(confirmed, "y") && !strings.EqualFold(confirmed, "yes") {
			return DeviceConfig{}, fmt.Errorf("backend selection was not confirmed")
		}
	}

	config := DeviceConfig{Version: DeviceConfigVersion, Backends: selection}
	if err := SaveDeviceConfig(configPath, config); err != nil {
		return DeviceConfig{}, err
	}
	fmt.Fprintf(output, "Saved approved backend order to %s\n", configPath)
	return config, nil
}

func validateBackendIDs(ids []string, allowEmpty bool) error {
	if len(ids) == 0 && !allowEmpty {
		return fmt.Errorf("at least one backend is required")
	}
	registry := backendByID()
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := registry[id]; !ok {
			return fmt.Errorf("unknown backend %q", id)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate backend %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func backendByID() map[string]Backend {
	registry := make(map[string]Backend, len(backendRegistry))
	for _, backend := range backendRegistry {
		registry[backend.ID] = backend
	}
	return registry
}

func splitBackendList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if id := strings.TrimSpace(part); id != "" {
			result = append(result, id)
		}
	}
	return result
}

func readSetupLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read setup response: %w", err)
	}
	if errors.Is(err, io.EOF) && line == "" {
		return "", fmt.Errorf("setup input ended before confirmation")
	}
	return strings.TrimSpace(line), nil
}

func writeAtomicConfig(path string, content []byte) (returnErr error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".device-client-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		if err := os.Remove(temporaryPath); err != nil && !os.IsNotExist(err) && returnErr == nil {
			returnErr = err
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
