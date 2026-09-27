package machtianiconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// RunModelArgs selects every run role explicitly so a resumed conversation
// cannot restore historical model aliases over DearMachine's current choices.
// Read the selectors for each launch; model-host resolves their current profiles.
// A one-run model overrides the planner, while configured role choices remain
// independent. Unset roles follow the effective planner, as on a fresh run.
func RunModelArgs(oneRun string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	var config struct {
		DefaultModel       string `toml:"default_model"`
		AnswerModel        string `toml:"answer_model"`
		FileDiscoveryModel string `toml:"file_discovery_model"`
		ShellAgentModel    string `toml:"shell_agent_model"`
	}
	path := filepath.Join(home, ".config", "dearmachine", "machtiani", "config.toml")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read run model configuration: %w", err)
	}
	if err == nil {
		if _, err := toml.Decode(string(data), &config); err != nil {
			return nil, fmt.Errorf("decode run model configuration: %w", err)
		}
	}
	planner := strings.TrimSpace(oneRun)
	if planner == "" {
		planner = strings.TrimSpace(config.DefaultModel)
	}
	var args []string
	for _, role := range []struct{ flag, alias string }{
		{"--model", planner},
		{"--answer-model", config.AnswerModel},
		{"--file-discovery-model", config.FileDiscoveryModel},
		{"--shell-agent-model", config.ShellAgentModel},
	} {
		alias := strings.TrimSpace(role.alias)
		if alias == "" {
			alias = planner
		}
		if alias != "" {
			args = append(args, role.flag, alias)
		}
	}
	return args, nil
}
