package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
	"unsafe"

	"github.com/dearmachine/dearmachine/internal/hostos"
	"golang.org/x/sys/windows"
)

func TestWindowsSupervisorSurvivesSessionJob(t *testing.T) {
	directory := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsSupervisorSessionHelper$")
	cmd.Env = append(os.Environ(), "DM_SESSION_HELPER="+directory)
	cmd.SysProcAttr = hostos.DetachedWorker()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("launcher: %v: %s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(directory, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Release()
	defer process.Kill()
	// The launcher has exited and its only job handle has closed. The detached
	// supervisor must remain alive after Windows has processed job termination.
	time.Sleep(300 * time.Millisecond)
	if !hostos.Alive(pid) {
		t.Fatal("supervisor died when the launching session job closed")
	}
}

func TestWindowsSupervisorSessionHelper(t *testing.T) {
	directory := os.Getenv("DM_SESSION_HELPER")
	if directory == "" {
		return
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		t.Fatal(err)
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		t.Fatal(err)
	}
	pid, err := StartDetached(filepath.Join(directory, "state"), []string{os.Args[0], "-test.run=^TestWindowsSupervisorDetachedHelper$"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "pid"), []byte(strconv.Itoa(pid)), 0600); err != nil {
		t.Fatal(err)
	}
	// Process exit closes the sole job handle, just as an SSH session ending.
	os.Exit(0)
}

func TestWindowsSupervisorDetachedHelper(t *testing.T) {
	if os.Getenv("DM_SESSION_HELPER") != "" {
		time.Sleep(time.Minute)
	}
}
