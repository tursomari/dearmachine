package client

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRuntimeConfigRoundTripIsPrivate(t *testing.T) {
	home := t.TempDir()
	path, err := DefaultRuntimeConfigPath(func() (string, error) { return home, nil })
	if err != nil {
		t.Fatal(err)
	}
	want := RuntimeConfig{
		Version:                RuntimeConfigVersion,
		Project:                "/selected/project",
		Model:                  "selected-model",
		AgentBinary:            "/selected/machtiani",
		DeviceConfig:           "/selected/dearmachine.toml",
		ManagerPath:            "/selected/agent-manager",
		EntryPointRepo:         "/selected/entry-point",
		EntryPointPrompt:       "/selected/prompt.md",
		PollInterval:           "7s",
		Concurrency:            4,
		MaintenanceMinTurns:    9,
		MaintenanceMinTurnsSet: true,
		MagnificaHumanitas:     true,
		MinimalFooter:          true,
		Verbose:                true,
	}
	if err := SaveRuntimeConfig(path, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("runtime config mode = %o, want 600", info.Mode().Perm())
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("runtime config directory mode = %o, want 700", directoryInfo.Mode().Perm())
	}
	got, found, err := LoadRuntimeConfig(path)
	if err != nil || !found {
		t.Fatalf("LoadRuntimeConfig: found=%v, err=%v", found, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime config = %+v, want %+v", got, want)
	}
}

func TestRuntimeConfigMissingAndInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.toml")
	if _, found, err := LoadRuntimeConfig(path); err != nil || found {
		t.Fatalf("missing runtime config = found %v, err %v", found, err)
	}
	if err := os.WriteFile(path, []byte("version = 2\nunknown = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadRuntimeConfig(path); err == nil ||
		(!strings.Contains(err.Error(), "unknown key") && !strings.Contains(err.Error(), "version")) {
		t.Fatalf("invalid runtime config error = %v", err)
	}
}
