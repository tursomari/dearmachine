//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/supervisor"
)

func TestDetachedSupervisorCLI(t *testing.T) {
	home, err := os.MkdirTemp("", "dm-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	t.Setenv("DEARMACHINE_CLI_TEST_HELPER", "1")
	root := filepath.Join(home, ".dearmachine")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(home, "cli")
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(binary, "'", "'\\''") + "' -test.run=^TestSupervisorCLIHelper$ -- \"$@\"\n"
	if err := os.WriteFile(launcher, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s, err := supervisor.Request(root, "status", time.Second); err == nil {
			_ = syscall.Kill(s.SupervisorPID, syscall.SIGTERM)
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(supervisor.SocketPath(root)); os.IsNotExist(err) {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Error("detached fixture supervisor did not exit")
		}
	}()
	var wg sync.WaitGroup
	pids := make(chan int, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pid, err := startSupervised(launcher, []string{"fixture-daemon", root}, root)
			if err != nil {
				t.Error(err)
				return
			}
			pids <- pid
		}()
	}
	wg.Wait()
	close(pids)
	first := 0
	for pid := range pids {
		if first == 0 {
			first = pid
		}
		if pid != first {
			t.Fatal("concurrent starts created different daemons")
		}
	}
	if first == 0 {
		t.Fatal("no daemon started")
	}
	sid, err := unix.Getsid(0)
	if err != nil {
		t.Fatal(err)
	}
	state, err := supervisor.Request(root, "status", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ownerSID, err := unix.Getsid(state.SupervisorPID)
	if err != nil || ownerSID == sid || ownerSID != state.SupervisorPID {
		t.Fatalf("supervisor was not detached: %d %v", ownerSID, err)
	}
	if err := client.WaitDaemonReady(filepath.Join(root, "run", "dearmachine.pid"), first, time.Second); err != nil {
		t.Fatal(err)
	}
	state, err = supervisor.Request(root, "restart", 5*time.Second)
	if err != nil || state.DaemonPID == first {
		t.Fatalf("restart: %+v %v", state, err)
	}
	if _, err := supervisor.Request(root, "down", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(supervisor.LogPath(root))
	if err != nil || !strings.Contains(string(data), "fixture ready") {
		t.Fatalf("log: %q %v", data, err)
	}
}
