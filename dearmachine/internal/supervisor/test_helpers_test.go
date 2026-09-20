package supervisor

import (
	"github.com/dearmachine/dearmachine/internal/hostos"
	"os"
	"os/exec"
	"testing"
)

func privateTempDir(t *testing.T) string {
	t.Helper()
	// Keep room for the socket filename under macOS's shorter path limit.
	dir, err := os.MkdirTemp("", "dm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := hostos.Protect(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testExecutable(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
