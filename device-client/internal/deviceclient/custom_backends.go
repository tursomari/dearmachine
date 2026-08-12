package deviceclient

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	backendcatalog "github.com/dearmachine/dearmachine/internal/backends"
)

// CustomBackend represents a user-defined backend loaded from a TOML
// configuration file.  It embeds the shared Backend struct and carries
// additional fields needed to construct a ConfigurableAdapter.
type CustomBackend struct {
	Backend
	OutputFormat string
	Arguments    []string
	Environment  map[string]string
}

// customBackendDef is the TOML schema for a single custom backend.
type customBackendDef struct {
	Name         string            `toml:"name"`
	Executable   string            `toml:"executable"`
	OutputFormat string            `toml:"output_format"`
	Arguments    []string          `toml:"arguments"`
	Environment  map[string]string `toml:"environment"`
	InstallHelp  string            `toml:"install_help"`
}

// LoadCustomBackends reads a TOML file at path and returns parsed
// custom-backend definitions.  If the file does not exist, it returns
// nil (no custom backends, not an error).
func LoadCustomBackends(path string) ([]CustomBackend, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read custom backends config: %w", err)
	}

	var parsed map[string]customBackendDef
	if err := toml.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parse custom backends config: %w", err)
	}

	result := make([]CustomBackend, 0, len(parsed))
	for id, def := range parsed {
		if id == "" {
			return nil, fmt.Errorf("custom backend has empty key")
		}
		if def.Executable == "" {
			return nil, fmt.Errorf("custom backend %q: executable is required", id)
		}
		format := def.OutputFormat
		if format == "" {
			format = "plain"
		}
		if format != "json-stream" && format != "plain" {
			return nil, fmt.Errorf("custom backend %q: output_format must be \"json-stream\" or \"plain\", got %q", id, format)
		}
		displayName := def.Name
		if displayName == "" {
			displayName = id
		}
		result = append(result, CustomBackend{
			Backend: backendcatalog.Backend{
				ID:          id,
				DisplayName: displayName,
				Executable:  def.Executable,
				InstallHelp: def.InstallHelp,
			},
			OutputFormat: format,
			Arguments:    def.Arguments,
			Environment:  def.Environment,
		})
	}
	return result, nil
}

// customBackendCatalog derives a custom-backend TOML path from the
// agent-manager binary path and returns catalog-compatible Backend
// values suitable for ValidateIDsWithCustom.
func customBackendCatalog(managerPath string) []backendcatalog.Backend {
	configPath := filepath.Join(
		filepath.Dir(filepath.Dir(managerPath)),
		"config",
		"custom-backends.toml",
	)
	custom, err := LoadCustomBackends(configPath)
	if err != nil {
		return nil
	}
	backends := make([]backendcatalog.Backend, 0, len(custom))
	for _, cb := range custom {
		backends = append(backends, cb.Backend)
	}
	return backends
}

// LoadCustomBackendsFromManager loads custom backend definitions using the
// agent-manager binary path to derive the config location. Returns the
// catalog-compatible Backend values.
func LoadCustomBackendsFromManager(managerPath string) ([]backendcatalog.Backend, error) {
    return customBackendCatalog(managerPath), nil
}
