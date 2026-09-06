package supervisor

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSocketReportsObservedPersistence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{StateDir: root, Command: []string{"/bin/sleep", "60"}, Persistence: func() string { return "disabled" }})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s, err := Request(root, "status", time.Second)
		if err == nil {
			if s.Persistence != "disabled" {
				t.Fatalf("saved choice replaced observation: %+v", s)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("owner unavailable")
}
