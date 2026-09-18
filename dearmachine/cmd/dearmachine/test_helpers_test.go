package main

import (
	"os"
	"os/exec"
	"testing"
)

func testExecutable(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// Unix socket names on macOS are limited to 104 bytes. Do not include a long
// test name in a temporary home that will contain the native control socket.
func socketTestHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("", "dm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	return home
}
