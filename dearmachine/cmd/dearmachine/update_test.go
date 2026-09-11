package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateDelegatesWithoutTerminalAndRejectsAmbiguousFlags(t *testing.T) {
	home := t.TempDir()
	fixture := filepath.Join(home, "concierge")
	if err := os.WriteFile(fixture, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	deps := defaultDependencies()
	deps.userHomeDir = func() (string, error) { return home, nil }
	deps.stdout, deps.flagOutput = &output, &output
	env := func(key string) string {
		if key == "DEARMACHINE_CONCIERGE_BIN" {
			return fixture
		}
		return ""
	}
	if err := run([]string{"update", "--check"}, env, deps); err != nil {
		t.Fatal(err)
	}
	if output.String() != "update\n--check\n" {
		t.Fatalf("unexpected handoff: %q", output.String())
	}
	output.Reset()
	if err := run([]string{"update", "--check", "--json"}, env, deps); err != nil {
		t.Fatal(err)
	}
	if output.String() != "update\n--check\n--json\n" {
		t.Fatalf("unexpected JSON handoff: %q", output.String())
	}
	output.Reset()
	if err := run([]string{"update", "--check", "--recover"}, env, deps); err == nil {
		t.Fatal("contradictory flags accepted")
	}
	if strings.Contains(output.String(), "update\n") {
		t.Fatal("invalid command reached installer")
	}
	if err := run([]string{"update", "--recover", "--json"}, env, deps); err == nil {
		t.Fatal("JSON recovery accepted without a structured recovery contract")
	}
}

func TestUpdateControlStoppedRecordAndFreshHome(t *testing.T) {
	home := t.TempDir()
	deps := defaultDependencies()
	deps.userHomeDir = func() (string, error) { return home, nil }
	var output bytes.Buffer
	deps.stdout = &output
	if err := runUpdateControl([]string{"status"}, deps); err != nil {
		t.Fatal(err)
	}
	if output.String() != "{\"running\":false}\n" {
		t.Fatal(output.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".dearmachine")); !os.IsNotExist(err) {
		t.Fatal("fresh status created client state")
	}
	root := filepath.Join(home, ".dearmachine", "run")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "supervisor.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runUpdateControl([]string{"stop"}, deps); err != nil {
		t.Fatal(err)
	}
	if err := runUpdateControl([]string{"refresh"}, deps); err != nil {
		t.Fatal(err)
	}
}
