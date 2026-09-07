package client

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBackendRegistryAndPATHDetection(t *testing.T) {
	registry := Backends()
	var ids, executables []string
	for _, backend := range registry {
		ids = append(ids, backend.ID)
		executables = append(executables, backend.Executable)
	}
	if !reflect.DeepEqual(ids, []string{"codex", "codex-yolo", "forge", "omp", "claude"}) {
		t.Fatalf("backend IDs = %v", ids)
	}
	if !reflect.DeepEqual(executables, []string{"codex", "codex", "forge", "omp", "claude"}) {
		t.Fatalf("backend executables = %v", executables)
	}

	detections := DetectBackends(func(executable string) (string, error) {
		if executable == "codex" {
			return "/test/bin/" + executable, nil
		}
		return "", os.ErrNotExist
	})
	if len(detections) != 5 || !detections[0].Found || detections[0].Path != "/test/bin/codex" ||
		!detections[1].Found || detections[1].Path != "/test/bin/codex" || detections[2].Found || detections[3].Found || detections[4].Found {
		t.Fatalf("detections = %+v", detections)
	}
}

func TestDeviceConfigRoundTripAndDefaultPath(t *testing.T) {
	home := t.TempDir()
	path, err := DefaultDeviceConfigPath(func() (string, error) { return home, nil })
	if err != nil || !strings.HasSuffix(path, filepath.Join(".dearmachine", "config", "dearmachine.toml")) {
		t.Fatalf("DefaultDeviceConfigPath = %q, %v", path, err)
	}
	databasePath, err := DefaultDeviceDatabasePath(func() (string, error) { return home, nil })
	if err != nil || databasePath != filepath.Join(home, ".dearmachine", "state", "dearmachine.db") {
		t.Fatalf("DefaultDeviceDatabasePath = %q, %v", databasePath, err)
	}
	config := DeviceConfig{
		Version:  DeviceConfigVersion,
		Backends: []string{"codex", "forge"},
	}
	if err := SaveDeviceConfig(path, config); err != nil {
		t.Fatalf("SaveDeviceConfig: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %o, want 600", got)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if got := string(content); got != "version = 1\nbackends = [\"codex\",\"forge\"]\nresponse_tier = \"plain\"\n" {
		t.Fatalf("config content = %q", got)
	}
	loaded, err := LoadDeviceConfig(path)
	want := config
	want.ResponseTier = TierPlain
	if err != nil || !reflect.DeepEqual(loaded, want) {
		t.Fatalf("LoadDeviceConfig = %+v, %v", loaded, err)
	}
}

func TestDeviceConfigResponseTierRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dearmachine.toml")
	config := DeviceConfig{
		Version:      DeviceConfigVersion,
		Backends:     []string{"codex", "forge"},
		ResponseTier: TierFormatted,
	}
	if err := SaveDeviceConfig(path, config); err != nil {
		t.Fatalf("SaveDeviceConfig: %v", err)
	}
	loaded, err := LoadDeviceConfig(path)
	if err != nil || !reflect.DeepEqual(loaded, config) {
		t.Fatalf("LoadDeviceConfig = %+v, %v", loaded, err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if got := string(content); !strings.Contains(got, `response_tier = "formatted"`) {
		t.Fatalf("config content = %q", got)
	}
}

func TestDeviceConfigDefaultsMissingResponseTierToPlain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dearmachine.toml")
	content := []byte("version = 1\nbackends = [\"codex\"]\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
	loaded, err := LoadDeviceConfig(path)
	if err != nil {
		t.Fatalf("LoadDeviceConfig: %v", err)
	}
	if loaded.ResponseTier != TierPlain {
		t.Fatalf("ResponseTier = %q, want %q", loaded.ResponseTier, TierPlain)
	}
}

func TestDeviceConfigRejectsInvalidResponseTier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dearmachine.toml")
	content := []byte("version = 1\nbackends = [\"codex\"]\nresponse_tier = \"fancy\"\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := LoadDeviceConfig(path)
	if err == nil || !strings.Contains(err.Error(), "response tier must be one of plain, formatted, complete") {
		t.Fatalf("LoadDeviceConfig error = %v, want response tier parse failure", err)
	}
}

func TestDeviceConfigRejectsDuplicateResponseTier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dearmachine.toml")
	content := []byte("version = 1\nbackends = [\"codex\"]\nresponse_tier = \"plain\"\nresponse_tier = \"plain\"\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := LoadDeviceConfig(path)
	if err == nil || !strings.Contains(err.Error(), "duplicate response_tier") {
		t.Fatalf("LoadDeviceConfig error = %v, want duplicate response_tier", err)
	}
}

func TestLoadDeviceConfigCanonicalizesLegacyForgecodeID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dearmachine.toml")
	content := []byte("version = 1\nbackends = [\"forgecode\",\"codex\"]\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	loaded, err := LoadDeviceConfig(path)
	if err != nil || !reflect.DeepEqual(loaded.Backends, []string{"forge", "codex"}) {
		t.Fatalf("LoadDeviceConfig legacy IDs = %+v, %v", loaded, err)
	}
	if err := SaveDeviceConfig(filepath.Join(t.TempDir(), "new.toml"), DeviceConfig{
		Version:  DeviceConfigVersion,
		Backends: []string{"forgecode"},
	}); err == nil || !strings.Contains(err.Error(), `unknown backend "forgecode"`) {
		t.Fatalf("SaveDeviceConfig accepted legacy ID: %v", err)
	}
}

func TestDeviceConfigRejectsInvalidBackends(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "missing version", content: `backends = ["codex"]`, want: "version must be 1"},
		{name: "unknown", content: "version = 1\nbackends = [\"other\"]", want: "unknown backend"},
		{name: "removed backend", content: "version = 1\nbackends = [\"removed-backend\"]", want: "rerun dearmachine setup-agents"},
		{name: "duplicate", content: "version = 1\nbackends = [\"codex\",\"codex\"]", want: "duplicate backend"},
		{name: "path instead of ID", content: "version = 1\nbackends = [\"/bin/codex\"]", want: "unknown backend"},
		{name: "unknown key", content: "version = 1\nbackends = [\"codex\"]\npath = \"/bin/codex\"", want: "unknown key"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			_, err := LoadDeviceConfig(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadDeviceConfig error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSetupAgentsConfirmsDetectedDefaultOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dearmachine.toml")
	var output strings.Builder
	config, err := SetupAgents(
		strings.NewReader("\n"),
		&output,
		path,
		nil,
		fakeBackendLookup("forge", "codex", "omp", "pi"),
	)
	if err != nil {
		t.Fatalf("SetupAgents: %v", err)
	}
	want := []string{"codex", "forge", "omp"}
	if !reflect.DeepEqual(config.Backends, want) {
		t.Fatalf("configured backends = %v, want %v", config.Backends, want)
	}
	for _, text := range []string{
		"found   forge",
		"found   omp",
		"Default priority order: codex, forge, omp",
		"Saved approved backend order",
	} {
		if !strings.Contains(output.String(), text) {
			t.Errorf("setup output missing %q:\n%s", text, output.String())
		}
	}
	loaded, err := LoadDeviceConfig(path)
	if err != nil || !reflect.DeepEqual(loaded.Backends, want) {
		t.Fatalf("saved config = %+v, %v", loaded, err)
	}
}

func TestSetupAgentsConfirmsCustomDetectedOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dearmachine.toml")
	var output strings.Builder
	config, err := SetupAgents(
		strings.NewReader("n\nforge, codex\ny\n"),
		&output,
		path,
		nil,
		fakeBackendLookup("codex", "forge"),
	)
	if err != nil || !reflect.DeepEqual(config.Backends, []string{"forge", "codex"}) {
		t.Fatalf("custom SetupAgents = %+v, %v", config, err)
	}
	if !strings.Contains(output.String(), "Use forge, codex? [y/N]") {
		t.Fatalf("custom confirmation missing:\n%s", output.String())
	}
}

func TestSetupAgentsPreferredOverrideStillRequiresConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dearmachine.toml")
	config, err := SetupAgents(
		strings.NewReader("yes\n"),
		ioDiscard{},
		path,
		[]string{"codex"},
		fakeBackendLookup("codex", "pi"),
	)
	if err != nil || !reflect.DeepEqual(config.Backends, []string{"codex"}) {
		t.Fatalf("preferred SetupAgents = %+v, %v", config, err)
	}
	if _, err := SetupAgents(
		strings.NewReader("yes\n"),
		ioDiscard{},
		filepath.Join(t.TempDir(), "config.toml"),
		[]string{"forge"},
		fakeBackendLookup("codex"),
	); err == nil || !strings.Contains(err.Error(), "was not detected") {
		t.Fatalf("missing preferred backend error = %v", err)
	}
}

func TestSetupAgentsLabelsUnapprovedMissingBackendsAsSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dearmachine.toml")
	var output strings.Builder
	config, err := SetupAgents(
		strings.NewReader("yes\n"),
		&output,
		path,
		[]string{"forge"},
		fakeBackendLookup("forge"),
	)
	if err != nil || !reflect.DeepEqual(config.Backends, []string{"forge"}) {
		t.Fatalf("preferred SetupAgents = %+v, %v", config, err)
	}
	for _, want := range []string{
		"not on PATH codex",
		"not approved, skipped",
		"found   forge",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("setup output missing %q:\n%s", want, output.String())
		}
	}
}

func TestSetupAgentsRequiresExplicitCodexYoloOptIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dearmachine.toml")
	var output strings.Builder
	config, err := SetupAgents(
		strings.NewReader("yes\n"),
		&output,
		path,
		[]string{"codex-yolo"},
		fakeBackendLookup("codex"),
	)
	if err != nil || !reflect.DeepEqual(config.Backends, []string{"codex-yolo"}) {
		t.Fatalf("preferred codex-yolo SetupAgents = %+v, %v", config, err)
	}
	if !strings.Contains(output.String(), "codex-yolo") ||
		!strings.Contains(output.String(), "explicit opt-in") {
		t.Fatalf("codex-yolo opt-in notice missing:\n%s", output.String())
	}

	defaultPath := filepath.Join(t.TempDir(), "dearmachine.toml")
	defaultConfig, err := SetupAgents(
		strings.NewReader("yes\n"),
		ioDiscard{},
		defaultPath,
		nil,
		fakeBackendLookup("codex"),
	)
	if err != nil || !reflect.DeepEqual(defaultConfig.Backends, []string{"codex"}) {
		t.Fatalf("default SetupAgents selected unrestricted backend = %+v, %v", defaultConfig, err)
	}
}

func TestSetupAgentsOffersInstallHelpWhenNoneDetected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dearmachine.toml")
	var output strings.Builder
	_, err := SetupAgents(
		strings.NewReader(""),
		&output,
		path,
		nil,
		fakeBackendLookup(),
	)
	if err == nil || !strings.Contains(err.Error(), "no supported coding agents") {
		t.Fatalf("SetupAgents no agents error = %v", err)
	}
	for _, want := range []string{"Install help:", "Forge:", "Codex CLI:", "OMP:"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("install help missing %q:\n%s", want, output.String())
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("config written with no detected agents; stat error = %v", err)
	}
}

func TestSetupAgentsDoesNotSaveUnconfirmedSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dearmachine.toml")
	_, err := SetupAgents(
		strings.NewReader("n\nforge\nn\n"),
		ioDiscard{},
		path,
		nil,
		fakeBackendLookup("forge"),
	)
	if err == nil || !strings.Contains(err.Error(), "was not confirmed") {
		t.Fatalf("unconfirmed setup error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unconfirmed config was written; stat error = %v", err)
	}
}

func fakeBackendLookup(found ...string) func(string) (string, error) {
	available := make(map[string]struct{}, len(found))
	for _, executable := range found {
		available[executable] = struct{}{}
	}
	return func(executable string) (string, error) {
		if _, ok := available[executable]; ok {
			return "/test/bin/" + executable, nil
		}
		return "", errors.New("not found")
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(content []byte) (int, error) { return len(content), nil }
