package machtianiconfig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeStartupImportsBeforeUsingManagedConfig(t *testing.T) {
	home := t.TempDir()
	for _, dir := range []string{".machtiani", ".config/dearmachine"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(home, ".machtiani/config.toml")
	if err := os.WriteFile(source, []byte("source sentinel\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config/dearmachine/backends.env"), []byte("KEY=fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(home, "machtiani")
	script := `#!/bin/sh
set -eu
test "$MACHTIANI_UPDATE_REEXEC" = 1
printf '%s\n' "$@" > "$HOME/args"
mkdir -p "$HOME/.config/dearmachine/machtiani"
printf 'managed sentinel\n' > "$MACHTIANI_CONFIG"
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MACHTIANI_CONFIG", "/unrelated/personal.toml")
	if err := Migrate(context.Background(), binary, home); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(filepath.Join(home, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "config\nimport\n--source\n"+source) || !strings.Contains(string(args), "--credentials-file\n") {
		t.Fatal("wrong native import arguments")
	}
	if data, _ := os.ReadFile(source); string(data) != "source sentinel\n" {
		t.Fatal("personal config changed")
	}
	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), binary, home); err != nil {
		t.Fatal("existing target should not invoke importer", err)
	}
}
