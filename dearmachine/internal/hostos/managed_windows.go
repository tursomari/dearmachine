package hostos

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

var shellJobs = struct {
	sync.Mutex
	handles map[int]windows.Handle
}{handles: make(map[int]windows.Handle)}

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP}
}
func startProcessGroup(cmd *exec.Cmd) (int, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	if err = cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	fail := func(err error) (int, error) {
		cmd.Process.Kill()
		cmd.Wait()
		windows.CloseHandle(job)
		return 0, err
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fail(err)
	}
	err = windows.AssignProcessToJobObject(job, proc)
	windows.CloseHandle(proc)
	if err != nil {
		return fail(err)
	}
	// The child is suspended until job ownership is established, so it cannot
	// launch escaping descendants between Start and AssignProcessToJobObject.
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fail(err)
	}
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	err = windows.Thread32First(snapshot, &entry)
	resumed := false
	for err == nil {
		if entry.OwnerProcessID == uint32(cmd.Process.Pid) {
			thread, e := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if e != nil {
				err = e
				break
			}
			_, e = windows.ResumeThread(thread)
			windows.CloseHandle(thread)
			err = e
			resumed = e == nil
			break
		}
		err = windows.Thread32Next(snapshot, &entry)
	}
	windows.CloseHandle(snapshot)
	if !resumed {
		return fail(fmt.Errorf("resume supervised child: %w", err))
	}

	shellJobs.Lock()
	shellJobs.handles[cmd.Process.Pid] = job
	shellJobs.Unlock()
	// Watch the process handle separately from exec.Cmd.Wait: descendants can
	// inherit stdout pipes and otherwise keep Wait blocked after their parent exits.
	waiter, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil {
		releaseProcessGroup(cmd, cmd.Process.Pid)
		cmd.Wait()
		return 0, err
	}
	go func() {
		defer windows.CloseHandle(waiter)
		_, _ = windows.WaitForSingleObject(waiter, windows.INFINITE)
		shellJobs.Lock()
		defer shellJobs.Unlock()
		if current, ok := shellJobs.handles[cmd.Process.Pid]; ok && current == job {
			_ = windows.TerminateJobObject(job, 1)
		}
	}()
	return cmd.Process.Pid, nil
}
func processGroupID(cmd *exec.Cmd) int { return cmd.Process.Pid }
func releaseProcessGroup(cmd *exec.Cmd, _ int) {
	shellJobs.Lock()
	defer shellJobs.Unlock()
	if job, ok := shellJobs.handles[cmd.Process.Pid]; ok {
		windows.CloseHandle(job)
		delete(shellJobs.handles, cmd.Process.Pid)
	}
}
func killProcessGroup(cmd *exec.Cmd, _ int) error {
	shellJobs.Lock()
	defer shellJobs.Unlock()
	if job, ok := shellJobs.handles[cmd.Process.Pid]; ok {
		return windows.TerminateJobObject(job, 1)
	}
	return nil
}

func StartManaged(cmd *exec.Cmd) error {
	configureProcessGroup(cmd)
	_, err := startProcessGroup(cmd)
	return err
}
func ReleaseManaged(cmd *exec.Cmd) { releaseProcessGroup(cmd, cmd.Process.Pid) }

func KillManaged(cmd *exec.Cmd) error { return killProcessGroup(cmd, cmd.Process.Pid) }
