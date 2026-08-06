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

	backendcatalog "github.com/dearmachine/dearmachine/internal/backends"
)

const DeviceConfigVersion = 1

type Backend = backendcatalog.Backend

type BackendDetection struct {
	Backend Backend
	Path    string
	Found   bool
}

type DeviceConfig struct {
	Version  int
	Backends []string
}

func Backends() []Backend {
	return backendcatalog.All()
}

func DetectBackends(lookPath func(string) (string, error)) []BackendDetection {
	registered := Backends()
	detections := make([]BackendDetection, 0, len(registered))
	for _, backend := range registered {
		path, err := lookPath(backend.Executable)
		detections = append(detections, BackendDetection{
			Backend: backend,
			Path:    path,
			Found:   err == nil,
		})
	}
	return detections
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

func DefaultDeviceDatabasePath(userHomeDir func() (string, error)) (string, error) {
	root, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("user home directory is empty")
	}
	return filepath.Join(root, ".dearmachine", "state", "device-client.db"), nil
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
	config.Backends = backendcatalog.CanonicalizeIDs(config.Backends)
	if err := validateBackendIDs(config.Backends, false); err != nil {
		return DeviceConfig{}, fmt.Errorf(
			"validate device config: %w; rerun device-client setup-agents",
			err,
		)
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
		for _, backend := range Backends() {
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
	return backendcatalog.ValidateIDs(ids, allowEmpty)
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
