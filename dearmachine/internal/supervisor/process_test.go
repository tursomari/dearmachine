package supervisor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLockExclusiveAndReusable(t *testing.T) {
	root := privateTempDir(t)
	lock, err := acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := acquire(root); !errors.Is(err, ErrLocked) {
		if other != nil {
			other.Close()
		}
		t.Fatalf("second lock: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
	// Never unlink a flock file: contenders must keep using the same inode.
	if _, err := os.Stat(filepath.Join(root, "run", "supervisor.lock")); err != nil {
		t.Fatal(err)
	}
}

func TestLockRejectsSymlinkAndPublicState(t *testing.T) {
	for _, mode := range []string{"symlink", "public"} {
		t.Run(mode, func(t *testing.T) {
			root := privateTempDir(t)
			if mode == "public" {
				os.Chmod(root, 0755)
			} else {
				os.Mkdir(filepath.Join(root, "run"), 0700)
				os.Symlink(filepath.Join(root, "target"), filepath.Join(root, "run", "supervisor.lock"))
			}
			lock, err := acquire(root)
			if err == nil {
				lock.Close()
				t.Fatal("accepted unsafe state")
			}
		})
	}
}

func TestForegroundChildLogAndReap(t *testing.T) {
	root := privateTempDir(t)
	lock, err := acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	log, err := openLog(root)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	child, err := spawn([]string{"/bin/sh", "-c", "echo stdout; echo stderr >&2; exec sleep 60"}, log)
	if err != nil {
		t.Fatal(err)
	}
	defer child.kill()
	time.Sleep(30 * time.Millisecond)
	child.terminate()
	select {
	case <-child.done:
	case <-time.After(time.Second):
		t.Fatal("child was not reaped")
	}
	data, err := os.ReadFile(LogPath(root))
	if err != nil || !strings.Contains(string(data), "stdout\nstderr\n") {
		t.Fatalf("log %q: %v", data, err)
	}
}
