package agentmanager

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsNpmBackendPayload(t *testing.T) {
	root := t.TempDir()
	shim := filepath.Join(root, "codex.cmd")
	if err := os.WriteFile(shim, []byte("@echo off\r\nexit /b 77\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := backendLookPath(shim); err == nil || !strings.Contains(err.Error(), "native payload") {
		t.Fatalf("missing native payload: %v", err)
	}
	native := filepath.Join(root, "node_modules", "@openai", "codex-win32-x64", "vendor", "x86_64-pc-windows-msvc", "bin", "codex.exe")
	if err := os.MkdirAll(filepath.Dir(native), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	path, err := backendLookPath(shim)
	if err != nil || path != native {
		t.Fatalf("resolved %q: %v", path, err)
	}
}

func TestWindowsBackendArgumentsDoNotEnterAShell(t *testing.T) {
	root := t.TempDir()
	shim := filepath.Join(root, "claude.cmd")
	if err := os.WriteFile(shim, []byte("@echo off\r\nexit /b 77\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(root, "node_modules", "@anthropic-ai", "claude-code-win32-x64", "claude.exe")
	if err := os.MkdirAll(filepath.Dir(native), 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination, err := os.Create(native)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(destination, source)
	destination.Close()
	if err != nil {
		t.Fatal(err)
	}
	prompt := `A&B | %PATH% $(command) "quoted" C:\Users\Example Ω\Documents`
	command := backendCommandContext(context.Background(), shim, "-test.run=^TestWindowsBackendArgumentHelper$", "--", prompt)
	command.Env = append(os.Environ(), "DM_TEST_BACKEND_ARGUMENT="+prompt)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native payload failed: %v\n%s", err, data)
	}
}
func TestWindowsBackendArgumentHelper(t *testing.T) {
	expected := os.Getenv("DM_TEST_BACKEND_ARGUMENT")
	if expected == "" {
		t.Skip("subprocess helper")
	}
	if os.Args[len(os.Args)-1] != expected {
		t.Fatalf("argument changed: %q", os.Args[len(os.Args)-1])
	}
}
