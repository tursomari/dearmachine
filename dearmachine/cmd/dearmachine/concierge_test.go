package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBareDispatchTTYGuard(t *testing.T) {
	for _, tty := range []struct{ in, out bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		home := t.TempDir()
		var output strings.Builder
		deps := dependencies{stdout: &output, userHomeDir: func() (string, error) { return home, nil }, isInteractive: func(io.Reader) bool { return tty.in }, outputInteractive: func(io.Writer) bool { return tty.out }}
		if err := run(nil, func(string) string { return "" }, deps); err != nil {
			t.Fatal(err)
		}
		want := "Usage:"
		if tty.in && tty.out {
			want = "Installer mode"
		}
		if !strings.Contains(output.String(), want) {
			t.Fatalf("TTY %v: %s", tty, output.String())
		}
		entries, _ := os.ReadDir(home)
		if len(entries) != 0 {
			t.Fatal("bare invocation mutated state")
		}
	}
}

func TestBareInstalledAndPartialRouting(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	deps.outputInteractive = func(io.Writer) bool { return true }
	var output strings.Builder
	deps.stdout = &output
	// Config alone is partial, never a reason to run a fresh installation.
	if err := run(nil, func(string) string { return "" }, deps); err == nil || !strings.Contains(output.String(), "Recovery") {
		t.Fatalf("partial: %s %v", output.String(), err)
	}
	makeUpTestPair(t, deps, "concierge")
	output.Reset()
	if err := run(nil, func(string) string { return "" }, deps); err != nil || !strings.Contains(output.String(), "Concierge mode") {
		t.Fatalf("installed: %s %v", output.String(), err)
	}
	output.Reset()
	if err := run([]string{"--help"}, func(string) string { return "" }, deps); err != nil || !strings.Contains(output.String(), "Usage:") {
		t.Fatalf("help: %s %v", output.String(), err)
	}
}

func TestConservativeInstallDetection(t *testing.T) {
	for _, kind := range []string{"other-install", "symlink", "file", "unreadable", "invalid-registry"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			root := filepath.Join(home, ".dearmachine")
			switch kind {
			case "other-install":
				os.Mkdir(filepath.Join(home, ".machtiani"), 0700)
			case "symlink":
				os.Symlink(t.TempDir(), root)
			case "file":
				os.WriteFile(root, []byte("partial"), 0600)
			case "unreadable":
				os.Mkdir(root, 0000)
				t.Cleanup(func() { os.Chmod(root, 0700) })
			case "invalid-registry":
				os.Mkdir(root, 0700)
				os.WriteFile(filepath.Join(root, "pairs.toml"), []byte("bad toml ["), 0600)
			}
			state := detectInstallation(home)
			if state == "absent" || state == "installed" {
				t.Fatalf("unsafe detection: %s", state)
			}
		})
	}
}

func TestDevNullIsNotTTY(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if defaultDependencies().isInteractive(f) {
		t.Fatal("character device mistaken for TTY")
	}
}
