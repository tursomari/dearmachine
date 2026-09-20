package hostos

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLockContentionDoesNotBlockFileIO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "owner.lock")
	a, e := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := os.OpenFile(p, os.O_RDWR, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	if e = Flock(int(a.Fd()), LOCK_EX|LOCK_NB); e != nil {
		t.Fatal(e)
	}
	if e = Flock(int(b.Fd()), LOCK_EX|LOCK_NB); !errors.Is(e, ErrWouldBlock) {
		t.Fatalf("competing exclusive lock: %v", e)
	}
	if e = Flock(int(b.Fd()), LOCK_SH|LOCK_NB); !errors.Is(e, ErrWouldBlock) {
		t.Fatalf("competing shared lock: %v", e)
	}
	if _, e = a.WriteAt([]byte("owner"), 0); e != nil {
		t.Fatal(e)
	}
	buf := make([]byte, 5)
	if _, e = b.ReadAt(buf, 0); e != nil || string(buf) != "owner" {
		t.Fatalf("lock must not prevent owner inspection: %q %v", buf, e)
	}
	if e = Flock(int(a.Fd()), LOCK_UN); e != nil {
		t.Fatal(e)
	}
	if e = Flock(int(b.Fd()), LOCK_EX|LOCK_NB); e != nil {
		t.Fatal(e)
	}
	if !Alive(os.Getpid()) {
		t.Fatal("current process not alive")
	}
}
