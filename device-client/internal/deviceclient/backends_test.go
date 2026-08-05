package deviceclient

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
	if !reflect.DeepEqual(ids, []string{"codex", "forgecode"}) {
		t.Fatalf("backend IDs = %v", ids)
	}
	if !reflect.DeepEqual(executables, []string{"codex", "forge"}) {
		t.Fatalf("backend executables = %v", executables)
	}

	detections := DetectBackends(func(executable string) (string, error) {
		if executable == "codex" {
			return "/test/bin/" + executable, nil
		}
		return "", os.ErrNotExist
	})
	if len(detections) != 2 || !detections[0].Found || detections[0].Path != "/test/bin/codex" ||
		detections[1].Found {
		t.Fatalf("detections = %+v", detections)
	}
}

func TestDeviceConfigRoundTripAndDefaultPath(t *testing.T) {
	home := t.TempDir()
	path, err := DefaultDeviceConfigPath(func() (string, error) { return home, nil })
	if err != nil || !strings.HasSuffix(path, filepath.Join(".dearmachine", "config", "device-client.toml")) {
		t.Fatalf("DefaultDeviceConfigPath = %q, %v", path, err)
	}
	databasePath, err := DefaultDeviceDatabasePath(func() (string, error) { return home, nil })
	if err != nil || databasePath != filepath.Join(home, ".dearmachine", "state", "device-client.db") {
		t.Fatalf("DefaultDeviceDatabasePath = %q, %v", databasePath, err)
	}
	config := DeviceConfig{
		Version:  DeviceConfigVersion,
		Backends: []string{"codex", "forgecode"},
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
	if got := string(content); got != "version = 1\nbackends = [\"codex\",\"forgecode\"]\n" {
		t.Fatalf("config content = %q", got)
	}
	loaded, err := LoadDeviceConfig(path)
	if err != nil || !reflect.DeepEqual(loaded, config) {
		t.Fatalf("LoadDeviceConfig = %+v, %v", loaded, err)
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
		{name: "removed alpha backend", content: "version = 1\nbackends = [\"claude\"]", want: "rerun device-client setup-agents"},
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
	path := filepath.Join(t.TempDir(), "device-client.toml")
	var output strings.Builder
	config, err := SetupAgents(
		strings.NewReader("\n"),
		&output,
		path,
		nil,
		fakeBackendLookup("forge", "codex", "pi"),
	)
	if err != nil {
		t.Fatalf("SetupAgents: %v", err)
	}
	want := []string{"codex", "forgecode"}
	if !reflect.DeepEqual(config.Backends, want) {
		t.Fatalf("configured backends = %v, want %v", config.Backends, want)
	}
	for _, text := range []string{
		"found   forgecode",
		"Default priority order: codex, forgecode",
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
	path := filepath.Join(t.TempDir(), "device-client.toml")
	var output strings.Builder
	config, err := SetupAgents(
		strings.NewReader("n\nforgecode, codex\ny\n"),
		&output,
		path,
		nil,
		fakeBackendLookup("codex", "forge"),
	)
	if err != nil || !reflect.DeepEqual(config.Backends, []string{"forgecode", "codex"}) {
		t.Fatalf("custom SetupAgents = %+v, %v", config, err)
	}
	if !strings.Contains(output.String(), "Use forgecode, codex? [y/N]") {
		t.Fatalf("custom confirmation missing:\n%s", output.String())
	}
}

func TestSetupAgentsPreferredOverrideStillRequiresConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device-client.toml")
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
		[]string{"forgecode"},
		fakeBackendLookup("codex"),
	); err == nil || !strings.Contains(err.Error(), "was not detected") {
		t.Fatalf("missing preferred backend error = %v", err)
	}
}

func TestSetupAgentsOffersInstallHelpWhenNoneDetected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device-client.toml")
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
	for _, want := range []string{"Install help:", "Forgecode:", "Codex CLI:"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("install help missing %q:\n%s", want, output.String())
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("config written with no detected agents; stat error = %v", err)
	}
}

func TestSetupAgentsDoesNotSaveUnconfirmedSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device-client.toml")
	_, err := SetupAgents(
		strings.NewReader("n\nforgecode\nn\n"),
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
