package hostos

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Exercise three nested ownership levels: client, temporary shell, and the
// asynchronous worker. Only the temporary shell permits explicit breakaway.
func TestDetachedWorkerSurvivesShellAndRemainsOwned(t *testing.T) {
	dir := t.TempDir()
	outer := workerHelper("outer", dir)
	if err := StartManaged(outer); err != nil {
		t.Fatal(err)
	}
	defer ReleaseManaged(outer)
	defer outer.Wait()
	defer KillManaged(outer)
	if err := waitWorkerFile(filepath.Join(dir, "survived")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "worker"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := Signal(pid, 0); err != nil {
		t.Fatalf("worker died with shell: %v", err)
	}
	if err := KillManaged(outer); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if Signal(pid, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("worker escaped enclosing client job")
}

func workerHelper(mode, dir string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestDetachedWorkerHelper$")
	cmd.Env = append(os.Environ(), "DM_WORKER_HELPER="+mode, "DM_WORKER_DIR="+dir)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd
}
func waitWorkerFile(path string) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", path)
}
func TestDetachedWorkerHelper(t *testing.T) {
	mode := os.Getenv("DM_WORKER_HELPER")
	if mode == "" {
		return
	}
	dir := os.Getenv("DM_WORKER_DIR")
	switch mode {
	case "outer":
		shell := workerHelper("shell", dir)
		if err := StartManaged(shell); err != nil {
			t.Fatal(err)
		}
		defer ReleaseManaged(shell)
		shellJobs.Lock()
		job := shellJobs.handles[shell.Process.Pid]
		limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
		_, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
		shellJobs.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "launch"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := shell.Wait(); err != nil {
			t.Fatal(err)
		}
		ReleaseManaged(shell)
		if err := os.WriteFile(filepath.Join(dir, "handoff"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
	case "shell":
		if err := waitWorkerFile(filepath.Join(dir, "launch")); err != nil {
			t.Fatal(err)
		}
		child := workerHelper("worker", dir)
		child.SysProcAttr = DetachedWorker()
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		if err := waitWorkerFile(filepath.Join(dir, "worker")); err != nil {
			t.Fatal(err)
		}
		child.Process.Release()
	case "worker":
		if err := os.WriteFile(filepath.Join(dir, "worker"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			t.Fatal(err)
		}
		if err := waitWorkerFile(filepath.Join(dir, "handoff")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
		if err := os.WriteFile(filepath.Join(dir, "survived"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
	default:
		t.Fatalf("unknown helper: %s", mode)
	}
}
