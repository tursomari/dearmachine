package supervisor

import (
	"errors"
	"github.com/dearmachine/dearmachine/internal/hostos"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsOwnerLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-state")
	f, e := acquire(root)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if _, e = f.WriteAt([]byte("1234"), 0); e != nil {
		t.Fatal(e)
	}
	owned, e := OwnerPresent(root)
	if e != nil || !owned {
		t.Fatalf("owner detection %v %v", owned, e)
	}
	if e = CheckAvailable(root); !errors.Is(e, ErrLocked) {
		t.Fatalf("competing owner: %v", e)
	}
	f.Close()
	owned, e = OwnerPresent(root)
	if e != nil || owned {
		t.Fatalf("released owner detection %v %v", owned, e)
	}
}
func TestWindowsControlPipe(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pipe-root")
	l, e := listenControl(root)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	done := make(chan error, 1)
	go func() {
		c, e := l.Accept()
		if e != nil {
			done <- e
			return
		}
		defer c.Close()
		_, e = c.Write([]byte("ok"))
		done <- e
	}()
	c, e := dialControl(root, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	b := make([]byte, 2)
	if _, e = c.Read(b); e != nil || string(b) != "ok" {
		t.Fatalf("pipe %q %v", b, e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}
func TestWindowsJobTerminatesChild(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if e := privateDir(root); e != nil {
		t.Fatal(e)
	}
	log, e := openLog(root)
	if e != nil {
		t.Fatal(e)
	}
	defer log.Close()
	child, e := spawn([]string{os.Getenv("ComSpec"), "/d", "/c", "ping -n 100 127.0.0.1 >nul"}, log)
	if e != nil {
		t.Fatal(e)
	}
	if !hostos.Alive(child.cmd.Process.Pid) {
		t.Fatal("child never ran")
	}
	child.kill()
	select {
	case <-child.done:
	case <-time.After(5 * time.Second):
		t.Fatal("job termination did not stop child")
	}
	if hostos.Alive(child.cmd.Process.Pid) {
		t.Fatal("terminated child remains alive")
	}
}
