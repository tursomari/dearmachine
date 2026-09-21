package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsIndependentMachtianiDoesNotBlockSetup(t *testing.T) {
	home := t.TempDir()
	independent := filepath.Join(home, ".machtiani", "external-project", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(independent), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(independent, []byte("independent project data"), 0600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	calls := 0
	deps := dependencies{
		stdin: strings.NewReader(""), stdout: &output, flagOutput: &output,
		userHomeDir:       func() (string, error) { return home, nil },
		isInteractive:     func(io.Reader) bool { return true },
		outputInteractive: func(io.Writer) bool { return true },
		lookPath:          func(name string) (string, error) { return name, nil },
		launchConcierge: func(_ string, args []string, _ io.Reader, _, _ io.Writer) error {
			calls++
			if len(args) != 3 || args[0] != "--concierge" || args[1] != "--source-root" {
				t.Fatalf("setup arguments: %v", args)
			}
			return nil
		},
	}
	getenv := func(name string) string {
		if name == "DEARMACHINE_SOURCE_ROOT" {
			return filepath.Join(home, "source")
		}
		return ""
	}
	if state := detectInstallation(home); state != "absent" {
		t.Fatalf("independent Machtiani data mistaken for DearMachine: %s", state)
	}
	if err := runBare(getenv, deps); err != nil || calls != 1 {
		t.Fatalf("fresh setup: calls=%d error=%v output=%s", calls, err, output.String())
	}
	// Actual incomplete DearMachine state must still require recovery.
	if err := os.Mkdir(filepath.Join(home, ".dearmachine"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := runBare(getenv, deps); err == nil || calls != 1 {
		t.Fatalf("partial DearMachine state bypassed recovery: calls=%d error=%v", calls, err)
	}
	if data, err := os.ReadFile(independent); err != nil || string(data) != "independent project data" {
		t.Fatalf("independent state changed: %q %v", data, err)
	}
}
