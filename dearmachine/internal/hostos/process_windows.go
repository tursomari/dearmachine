package hostos

import (
	"context"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"unsafe"
)

func Detached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
}

// DetachedWorker leaves an explicitly breakaway-enabled temporary shell job.
// Windows retains it in the first enclosing job that forbids breakaway, so a
// client/task shutdown still cancels it. Ordinary SSH jobs remain unchanged.
func DetachedWorker() *syscall.SysProcAttr {
	attr := Detached()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	if err := windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)), nil); err == nil && limits.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK != 0 {
		attr.CreationFlags |= windows.CREATE_BREAKAWAY_FROM_JOB
	}
	return attr
}

func Exec(path string, args, env []string) error {
	cmd := exec.Command(path, args[1:]...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err == nil {
		os.Exit(0)
	}
	return err
}
func stopName(pid int) *uint16 {
	return windows.StringToUTF16Ptr(fmt.Sprintf("Local\\DearMachine-stop-%d", pid))
}
func Signal(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return syscall.EINVAL
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		if err == windows.ERROR_INVALID_PARAMETER {
			return syscall.ESRCH
		}
		return err
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err = windows.GetExitCodeProcess(h, &code); err != nil {
		return err
	}
	if code != 259 {
		return syscall.ESRCH
	}
	if sig == 0 {
		return nil
	}
	if sig == syscall.SIGTERM {
		event, e := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, stopName(pid))
		if e == nil {
			defer windows.CloseHandle(event)
			return windows.SetEvent(event)
		}
	}
	return windows.TerminateProcess(h, 1)
}
func NotifyContext(parent context.Context, s ...os.Signal) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt)
	// Each foreground DearMachine process exposes a private graceful-stop event.
	sid, err := currentSID()
	if err != nil {
		return ctx, stop
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + sid.String() + ")(A;;GA;;;SY)")
	if err != nil {
		return ctx, stop
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	event, err := windows.CreateEvent(&sa, 1, 0, stopName(os.Getpid()))
	if err != nil {
		return ctx, stop
	}
	go func() {
		defer windows.CloseHandle(event)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			result, e := windows.WaitForSingleObject(event, 100)
			if e != nil || result == windows.WAIT_OBJECT_0 {
				stop()
				return
			}
		}
	}()
	return ctx, stop
}
