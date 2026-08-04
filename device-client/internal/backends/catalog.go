package backends

import (
	"encoding/json"
	"fmt"
	"strings"
)

const EnvironmentVariable = "DEARMACHINE_BACKENDS"

type Backend struct {
	ID          string
	DisplayName string
	Executable  string
	InstallHelp string
}

var catalog = []Backend{
	{
		ID:          "codex",
		DisplayName: "Codex CLI",
		Executable:  "codex",
		InstallHelp: "Install Codex CLI, then ensure codex is on PATH.",
	},
	{
		ID:          "forgecode",
		DisplayName: "Forgecode",
		Executable:  "forge",
		InstallHelp: "Install Forgecode, then ensure forge is on PATH.",
	},
}

func All() []Backend {
	return append([]Backend(nil), catalog...)
}

func Lookup(id string) (Backend, bool) {
	for _, backend := range catalog {
		if backend.ID == id {
			return backend, true
		}
	}
	return Backend{}, false
}

func ValidateIDs(ids []string, allowEmpty bool) error {
	if len(ids) == 0 && !allowEmpty {
		return fmt.Errorf("at least one backend is required")
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := Lookup(id); !ok {
			return fmt.Errorf("unknown backend %q", id)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate backend %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func Encode(ids []string) (string, error) {
	if err := ValidateIDs(ids, false); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("encode backend list: %w", err)
	}
	return string(encoded), nil
}

func Decode(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("%s is required", EnvironmentVariable)
	}
	var ids []string
	if err := json.Unmarshal([]byte(value), &ids); err != nil {
		return nil, fmt.Errorf("parse %s: %w", EnvironmentVariable, err)
	}
	if err := ValidateIDs(ids, false); err != nil {
		return nil, fmt.Errorf("validate %s: %w", EnvironmentVariable, err)
	}
	return ids, nil
}
