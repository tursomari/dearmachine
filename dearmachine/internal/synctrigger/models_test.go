package synctrigger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMaintenanceModelFailureCleanupAndRecovery(t *testing.T) {
	for _, role := range []struct{ key, flag string }{
		{"default_model", "--model"}, {"answer_model", "--answer-model"},
		{"file_discovery_model", "--file-discovery-model"}, {"shell_agent_model", "--shell-agent-model"},
	} {
		t.Run(role.key, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			path := filepath.Join(home, ".config/dearmachine/machtiani/config.toml")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			writeConfig := func(alias string) {
				t.Helper()
				config := ""
				for _, key := range []string{"default_model", "answer_model", "file_discovery_model", "shell_agent_model"} {
					value := "current-" + key
					if key == role.key {
						value = alias
					}
					config += key + " = \"" + value + "\"\n"
				}
				if err := os.WriteFile(path, []byte(config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeConfig("unavailable")
			var calls []string
			o := &Orchestrator{
				RepoPath: t.TempDir(), AgentBinary: "machtiani", PromptTemplatePath: "/prompt.md",
				StatePath: filepath.Join(t.TempDir(), "checkpoint.json"),
				Lister: func(context.Context, string) ([]SessionInfo, error) {
					return []SessionInfo{{SessionID: "historical", UpdatedAt: time.Unix(1, 0)}, {SessionID: "held", UpdatedAt: time.Unix(2, 0)}}, nil
				},
				GitLastCommitTime: func(string) (time.Time, error) { return time.Time{}, nil },
				RunCommand: func(_ context.Context, _, _ string, args ...string) ([]byte, error) {
					calls = append(calls, strings.Join(args, " "))
					switch args[0] {
					case "session":
						if args[1] == "fork" {
							return []byte("temporary-fork"), nil
						}
					case "run":
						for _, flag := range []string{"--model", "--answer-model", "--file-discovery-model", "--shell-agent-model"} {
							if !slices.Contains(args, flag) {
								t.Fatalf("missing explicit role %s: %q", flag, args)
							}
						}
						index := slices.Index(args, role.flag)
						if args[index+1] == "unavailable" {
							return []byte("Model resolution error: model alias unavailable not found"), errors.New("run failed")
						}
						if args[index+1] != "recovered" {
							t.Fatalf("did not refresh model selection: %q", args)
						}
					}
					return nil, nil
				},
			}
			if err := o.OrchestrateSync(context.Background()); err == nil || !strings.Contains(err.Error(), "Model resolution error") {
				t.Fatalf("error = %v, want model resolution error", err)
			}
			if len(calls) != 3 || calls[2] != "session delete temporary-fork" {
				t.Fatalf("failed fork was not cleaned up before sync: %q", calls)
			}
			checkpoint, err := loadReviewCheckpoint(o.StatePath)
			if err != nil || checkpoint.SessionID != "" {
				t.Fatalf("failure advanced checkpoint: %+v, %v", checkpoint, err)
			}
			writeConfig("recovered")
			calls = nil
			if err := o.OrchestrateSync(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(calls) != 4 || calls[2] != "session delete temporary-fork" || !strings.HasPrefix(calls[3], "sync --include-docs --model ") {
				t.Fatalf("unexpected recovery pipeline: %q", calls)
			}
			checkpoint, err = loadReviewCheckpoint(o.StatePath)
			if err != nil || checkpoint.SessionID != "historical" {
				t.Fatalf("recovery checkpoint = %+v, %v", checkpoint, err)
			}
		})
	}
}

func TestMaintenanceInvalidModelConfigurationDoesNotFork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(home, ".config/dearmachine/machtiani/config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("default_model = ["), 0600); err != nil {
		t.Fatal(err)
	}
	o := &Orchestrator{}
	_, err := o.reviewSession(context.Background(), func(context.Context, string, string, ...string) ([]byte, error) {
		t.Fatal("created a fork with invalid model configuration")
		return nil, nil
	}, "historical")
	if err == nil || !strings.Contains(err.Error(), "decode run model configuration") {
		t.Fatalf("error = %v, want configuration error", err)
	}
}
