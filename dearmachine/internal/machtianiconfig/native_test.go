package machtianiconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This opt-in boundary test uses an independently built Machtiani executable.
// Sync contacts only the loopback fixture; session runs use the real resume and
// model-resolution paths with --dry-run, without invoking a model or backend.
func TestNativeResumeOverridesRemovedAliases(t *testing.T) {
	binary := os.Getenv("DEARMACHINE_TEST_MACHTIANI")
	if binary == "" {
		t.Skip("set DEARMACHINE_TEST_MACHTIANI to an absolute Machtiani executable")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("DEARMACHINE_TEST_MACHTIANI must be absolute")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model    string          `json:"model"`
			Messages json.RawMessage `json:"messages"`
			Stream   bool            `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Model != "current" {
			t.Errorf("sync used model %q, want current", request.Model)
		}
		content := "# Internal README\n\nModel selection fixture.\n"
		if strings.Contains(string(request.Messages), "BEGIN_RELEVANT_FILES[file-discovery]") && strings.Contains(string(request.Messages), "file_search") {
			content = "BEGIN_RELEVANT_FILES[file-discovery]\nREADME.md\nEND_RELEVANT_FILES[file-discovery]\n"
		}
		choice := map[string]any{"index": 0, "finish_reason": "stop"}
		messageKey := "message"
		if request.Stream {
			messageKey = "delta"
		}
		choice[messageKey] = map[string]string{"role": "assistant", "content": content}
		payload := map[string]any{"id": "fixture", "model": request.Model, "choices": []any{choice}}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			data, _ := json.Marshal(payload)
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
		} else {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(payload)
		}
	}))
	defer server.Close()
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	configPath := filepath.Join(home, ".config/dearmachine/machtiani/config.toml")
	environment := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "USERPROFILE=" + home,
		"MACHTIANI_CONFIG=" + configPath, "MACHTIANI_UPDATE_REEXEC=1",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid",
	}
	run := func(name string, args ...string) (string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, name, args...)
		command.Dir, command.Env = project, environment
		output, err := command.CombinedOutput()
		return string(output), err
	}
	mustRun := func(t *testing.T, name string, args ...string) string {
		t.Helper()
		output, err := run(name, args...)
		if err != nil {
			t.Fatalf("%s %q: %v\n%s", filepath.Base(name), args, err, output)
		}
		return output
	}
	config := fmt.Sprintf("default_model = \"current\"\n[providers.fixture]\nbase_url = %q\napi_key = \"fixture\"\n[models.current]\nprovider = \"fixture\"\nmodel = \"current\"\n", server.URL+"/v1")
	syncFixture(t, home, config, "unused")
	mustRun(t, "git", "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("# Model fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "git", "add", "README.md")
	mustRun(t, "git", "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	mustRun(t, binary, "init", "--no-interactive")
	syncArgs, err := SyncArgs("", true)
	if err != nil {
		t.Fatal(err)
	}
	// Prove explicit sync selection wins over an unavailable inherited alias.
	environment = append(environment, "MACHTIANI_FILE_DISCOVERY_MODEL=removed-inherited")
	mustRun(t, binary, syncArgs...)

	for _, flag := range []string{"--model", "--answer-model", "--file-discovery-model", "--shell-agent-model"} {
		t.Run(flag, func(t *testing.T) {
			// Seed one historical role using an alias that will then be removed.
			syncFixture(t, home, config+"\n[models.historical]\nprovider = \"fixture\"\nmodel = \"historical\"\n", "unused")
			id := "fixture-" + strings.TrimPrefix(flag, "--")
			priorEnvironment := environment
			t.Cleanup(func() { environment = priorEnvironment })
			environment = append(environment, "MACHTIANI_SESSION_ID="+id)
			args, err := RunModelArgs("")
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < len(args); i += 2 {
				if args[i] == flag {
					args[i+1] = "historical"
				}
			}
			mustRun(t, binary, append([]string{"run", "--dry-run", "--max-turns", "1", "--prompt", "Fixture task"}, args...)...)
			environment = environment[:len(environment)-1]
			syncFixture(t, home, config, "unused")
			resume := []string{"run", "--resume", id, "--dry-run", "--max-turns", "1", "--prompt", "Continue fixture task"}
			output, err := run(binary, resume...)
			if err == nil || !strings.Contains(output, `model alias "historical" not found`) {
				t.Fatalf("resume without overrides did not reproduce removed alias: %v\n%s", err, output)
			}
			args, err = RunModelArgs("")
			if err != nil {
				t.Fatal(err)
			}
			mustRun(t, binary, append(resume, args...)...)
			fork := strings.TrimSpace(mustRun(t, binary, "session", "fork", id))
			resume[2] = fork
			mustRun(t, binary, append(resume, args...)...)
			mustRun(t, binary, "session", "delete", fork)
		})
	}
}
