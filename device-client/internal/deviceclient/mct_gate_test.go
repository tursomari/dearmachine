package deviceclient

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"testing"
	"time"
)

func TestMCTRunnerInvocationCanBeGated(t *testing.T) {
	runner, err := NewMCTRunner("mct-agent", t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan []string, 1)
	release := make(chan struct{})
	want := errors.New("injected runner result")
	runner.invoke = func(command *exec.Cmd) error {
		started <- slices.Clone(command.Args)
		<-release
		return want
	}
	done := make(chan error, 1)
	go func() {
		done <- runner.Sync(context.Background())
	}()

	select {
	case args := <-started:
		if !slices.Equal(args, []string{"mct-agent", "sync"}) {
			t.Fatalf("command args = %v", args)
		}
	case <-time.After(time.Second):
		t.Fatal("runner invocation did not start")
	}
	select {
	case err := <-done:
		t.Fatalf("runner completed before gate release: %v", err)
	default:
	}
	close(release)
	if err := <-done; !errors.Is(err, want) {
		t.Fatalf("Sync error = %v, want wrapped %v", err, want)
	}
}
