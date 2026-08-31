package client

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestDaemonLifecyclePathsAndStatus(t *testing.T) {
	home := t.TempDir()
	logPath, err := DefaultDaemonLogPath(func() (string, error) { return home, nil })
	if err != nil || logPath != filepath.Join(home, ".dearmachine", "log", "dearmachine.log") {
		t.Fatalf("DefaultDaemonLogPath = %q, %v", logPath, err)
	}
	lockPath, err := DefaultDaemonLockPath(func() (string, error) { return home, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, running, err := DaemonStatus(lockPath); err != nil || running {
		t.Fatalf("missing daemon status = %v, %v", running, err)
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pid, running, err := DaemonStatus(lockPath)
	if err != nil || !running || pid != os.Getpid() {
		t.Fatalf("live daemon status = %d, %v, %v", pid, running, err)
	}
	readyPath, err := DefaultDaemonReadyPath(func() (string, error) { return home, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readyPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WaitDaemonReady(lockPath, os.Getpid(), time.Second); err != nil {
		t.Fatalf("wait ready: %v", err)
	}
}
