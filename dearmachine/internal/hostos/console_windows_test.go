package hostos

import (
	"bytes"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// A detached supervisor must not cause its worker, or that worker's ordinary
// console descendants, to allocate a visible Windows Terminal window.
func TestBackgroundTreeHasNoConsoleWindow(t *testing.T) {
	cmd := consoleHelper("supervisor")
	cmd.SysProcAttr = Detached()
	output, err := cmd.CombinedOutput()
	t.Logf("%s", output)
	if err != nil {
		t.Fatalf("background console tree: %v\n%s", err, output)
	}
	if !bytes.Contains(output, []byte("grandchild complete")) {
		t.Fatalf("background output did not reach its log: %s", output)
	}
}

func consoleHelper(role string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestBackgroundConsoleHelper$", "-test.timeout=20s", "-test.v")
	cmd.Env = append(os.Environ(), "DM_CONSOLE_HELPER="+role)
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

func TestBackgroundConsoleHelper(t *testing.T) {
	role := os.Getenv("DM_CONSOLE_HELPER")
	if role == "" {
		return
	}
	window, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	// IsWindowVisible alone misses the bug under SSH: that session's console
	// window is hidden even though the same launch opens one on the desktop.
	if window != 0 {
		t.Fatalf("%s allocated a console window", role)
	}
	if role == "grandchild" {
		_, _ = os.Stdout.WriteString("grandchild complete\n")
		return
	}
	next := "grandchild"
	if role == "supervisor" {
		next = "worker"
	}
	cmd := consoleHelper(next)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if role == "supervisor" {
		if err := StartManaged(cmd); err != nil {
			t.Fatal(err)
		}
		defer ReleaseManaged(cmd)
		defer KillManaged(cmd)
	} else if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}
