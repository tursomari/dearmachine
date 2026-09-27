package machtianiconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func syncFixture(t *testing.T, home, config, profile string) {
	t.Helper()
	for path, content := range map[string]string{
		".config/dearmachine/machtiani/config.toml": config,
		".config/machtiani/model-profile.json":      profile,
	} {
		full := filepath.Join(home, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

const selectorConfig = `default_model = "dearmachine"
answer_model = "dearmachine"
[models.dearmachine-sync]
provider = "dearmachine-host"
model = "@machtiani/sync"
`

func TestSyncSelectionPrecedence(t *testing.T) {
	for _, test := range []struct{ name, profile, cli, model, effort, alias string }{
		{"default", `{"version":1,"selectionVersion":1,"provider":"fixture","model":"default","reasoningEffort":"high"}`, "", "default", "high", "dearmachine-sync"},
		{"override clears reasoning", `{"version":1,"selectionVersion":1,"provider":"fixture","model":"default","reasoningEffort":"high","overrides":{"sync":{"version":1,"provider":"other","model":"sync"}}}`, "", "sync", "", "dearmachine-sync"},
		{"legacy profile", `{"version":1,"provider":"fixture","model":"legacy","reasoningEffort":"low"}`, "", "legacy", "low", "dearmachine-sync"},
		{"one run bypasses saved error", `invalid`, "cli-model", "", "", "cli-model"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			syncFixture(t, home, selectorConfig, test.profile)
			got, err := ResolveSync(home, test.cli)
			if err != nil {
				t.Fatal(err)
			}
			if got.Alias != test.alias || got.Model != test.model || got.ReasoningEffort != test.effort {
				t.Fatalf("selection = %#v", got)
			}
			if test.model == "sync" && got.Provider != "other" {
				t.Fatalf("provider = %s", got.Provider)
			}
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			args, err := SyncArgs(test.cli, true)
			want := []string{"sync", "--include-docs", "--model", test.alias, "--answer-model", test.alias, "--file-discovery-model", test.alias}
			if err != nil || !reflect.DeepEqual(args, want) {
				t.Fatalf("args = %q, err = %v", args, err)
			}
		})
	}
}

func TestSyncLegacyAndMissingConfiguration(t *testing.T) {
	home := t.TempDir()
	got, err := ResolveSync(home, "")
	if err != nil || got.Alias != "" {
		t.Fatalf("missing = %#v, %v", got, err)
	}
	for _, config := range []string{`default_model = "legacy"`, "default_model = \"other\"\nanswer_model = \"legacy\""} {
		syncFixture(t, home, config, "unused")
		got, err := ResolveSync(home, "")
		if err != nil || got.Alias != "legacy" {
			t.Fatalf("legacy = %#v, %v", got, err)
		}
	}
}

func TestSyncRejectsInvalidPersistedSelection(t *testing.T) {
	for _, profile := range []string{`invalid`, `{"version":1,"selectionVersion":2}`, `{"version":1,"selectionVersion":1,"provider":"p","model":"m","overrides":{"sync":null}}`, `{"version":1,"selectionVersion":1,"provider":"p","model":"m","overrides":{"sync":{"model":"incomplete"}}}`} {
		home := t.TempDir()
		syncFixture(t, home, selectorConfig, profile)
		if _, err := ResolveSync(home, ""); err == nil {
			t.Fatal("accepted invalid persisted profile")
		}
	}
}

func TestSyncReadsChangesWithoutRestart(t *testing.T) {
	home := t.TempDir()
	syncFixture(t, home, selectorConfig, `{"version":1,"selectionVersion":1,"provider":"p","model":"first"}`)
	if got, _ := ResolveSync(home, ""); got.Model != "first" {
		t.Fatal(got)
	}
	syncFixture(t, home, selectorConfig, `{"version":1,"selectionVersion":1,"provider":"p","model":"second"}`)
	if got, _ := ResolveSync(home, ""); got.Model != "second" {
		t.Fatal(got)
	}
}
