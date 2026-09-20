package client

import (
	"github.com/dearmachine/dearmachine/internal/hostos"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsDaemonCleanupWithStatusReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "dearmachine.pid")
	release, err := CreateDaemonLock(path)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := hostos.Open(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := release(); err != nil {
		t.Fatalf("status reader blocked daemon cleanup: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("daemon file remains: %v", err)
	}
	release, err = CreateDaemonLock(path)
	if err != nil {
		t.Fatalf("could not restart after cleanup: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}
