package backends

import (
	"encoding/json"
	"fmt"
	"strings"
)

const EnvironmentVariable = "DEARMACHINE_BACKENDS"

const legacyForgecodeID = "forgecode"

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
		ID:          "forge",
		DisplayName: "Forge",
		Executable:  "forge",
		InstallHelp: "Install Forge, then ensure forge is on PATH.",
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

// ValidateIDsWithCustom validates that every id exists in the hardcoded
// catalog or in the custom backends slice.  allowEmpty and duplicate
// checks work the same as ValidateIDs.
func ValidateIDsWithCustom(ids []string, custom []Backend, allowEmpty bool) error {
	if len(ids) == 0 && !allowEmpty {
		return fmt.Errorf("at least one backend is required")
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := Lookup(id); !ok {
			// Also check custom backends
			found := false
			for _, cb := range custom {
				if cb.ID == id {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("unknown backend %q", id)
			}
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate backend %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
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

// CanonicalizeIDs translates identifiers written by older Device Client
// versions. Callers must still validate the returned identifiers.
func CanonicalizeIDs(ids []string) []string {
	canonical := append([]string(nil), ids...)
	for index, id := range canonical {
		if id == legacyForgecodeID {
			canonical[index] = "forge"
		}
	}
	return canonical
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
	ids = CanonicalizeIDs(ids)
	if err := ValidateIDs(ids, false); err != nil {
		return nil, fmt.Errorf("validate %s: %w", EnvironmentVariable, err)
	}
	return ids, nil
}
