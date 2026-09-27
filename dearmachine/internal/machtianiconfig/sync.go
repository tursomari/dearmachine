package machtianiconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// SyncSelection describes the effective selection behind the explicit harness
// alias. Provider credentials stay with model-host; they never enter CLI args.
type SyncSelection struct {
	Alias           string
	Provider        string
	Model           string
	ReasoningEffort string
}

type modelProfile struct {
	Version          int                        `json:"version"`
	SelectionVersion int                        `json:"selectionVersion"`
	Provider         string                     `json:"provider"`
	Model            string                     `json:"model"`
	ReasoningEffort  string                     `json:"reasoningEffort"`
	Overrides        map[string]json.RawMessage `json:"overrides"`
}

// ResolveSync applies one-run alias > sync override > Default > legacy config.
// Generated @machtiani/sync models resolve the full selected provider profile in
// model-host at request time, including an explicitly absent reasoning effort.
func ResolveSync(home, oneRun string) (SyncSelection, error) {
	if strings.TrimSpace(oneRun) != "" {
		return SyncSelection{Alias: oneRun}, nil
	}
	configPath := filepath.Join(home, ".config", "dearmachine", "machtiani", "config.toml")
	var config struct {
		DefaultModel string `toml:"default_model"`
		AnswerModel  string `toml:"answer_model"`
		Models       map[string]struct {
			Provider string
			Model    string
		} `toml:"models"`
	}
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return SyncSelection{}, nil
	}
	if err != nil {
		return SyncSelection{}, fmt.Errorf("read sync configuration: %w", err)
	}
	if _, err := toml.Decode(string(data), &config); err != nil {
		return SyncSelection{}, fmt.Errorf("decode sync configuration: %w", err)
	}
	if model, ok := config.Models["dearmachine-sync"]; ok && model.Model == "@machtiani/sync" && model.Provider == "dearmachine-host" {
		data, err := os.ReadFile(filepath.Join(home, ".config", "machtiani", "model-profile.json"))
		if err != nil {
			return SyncSelection{}, fmt.Errorf("read sync model selection: %w", err)
		}
		var profile modelProfile
		if err := json.Unmarshal(data, &profile); err != nil {
			return SyncSelection{}, fmt.Errorf("invalid sync model selection: %w", err)
		}
		if profile.SelectionVersion != 0 && profile.SelectionVersion != 1 {
			return SyncSelection{}, fmt.Errorf("unsupported model selection version")
		}
		if raw, ok := profile.Overrides["sync"]; ok {
			if profile.SelectionVersion != 1 {
				return SyncSelection{}, fmt.Errorf("model overrides require selectionVersion 1")
			}
			// Unmarshal into a fresh value: absent fields must not inherit Default.
			var override modelProfile
			if err := json.Unmarshal(raw, &override); err != nil {
				return SyncSelection{}, err
			}
			profile = override
		}
		if profile.Version != 1 || strings.TrimSpace(profile.Provider) == "" || strings.TrimSpace(profile.Model) == "" || strings.ContainsAny(profile.Provider+profile.Model+profile.ReasoningEffort, "\r\n\x00") {
			return SyncSelection{}, fmt.Errorf("invalid effective sync model selection")
		}
		return SyncSelection{Alias: "dearmachine-sync", Provider: profile.Provider, Model: profile.Model, ReasoningEffort: profile.ReasoningEffort}, nil
	}
	alias := config.AnswerModel
	if alias == "" {
		alias = config.DefaultModel
	}
	return SyncSelection{Alias: alias}, nil
}

// SyncArgs explicitly selects all prompt roles used by sync. In particular,
// file discovery must not inherit a different model from the environment.
func SyncArgs(oneRun string, includeDocs bool) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	selection, err := ResolveSync(home, oneRun)
	if err != nil {
		return nil, err
	}
	args := []string{"sync"}
	if includeDocs {
		args = append(args, "--include-docs")
	}
	if selection.Alias != "" {
		args = append(args, "--model", selection.Alias, "--answer-model", selection.Alias, "--file-discovery-model", selection.Alias)
	}
	return args, nil
}
