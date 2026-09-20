package hostos

import (
	"errors"
	"golang.org/x/sys/windows"
)

const (
	LOCK_SH = 1
	LOCK_EX = 2
	LOCK_NB = 4
	LOCK_UN = 8
)

var ErrWouldBlock = errors.New("file is locked")

// Lock a byte beyond EOF: Windows byte-range locks are mandatory, so locking
// the PID record itself would prevent its readers from observing ownership.
func Flock(fd int, how int) error {
	o := windows.Overlapped{OffsetHigh: 0x7fffffff}
	if how == LOCK_UN {
		return windows.UnlockFileEx(windows.Handle(fd), 0, 1, 0, &o)
	}
	flags := uint32(0)
	if how&LOCK_EX != 0 {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	if how&LOCK_NB != 0 {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	err := windows.LockFileEx(windows.Handle(fd), flags, 0, 1, 0, &o)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrWouldBlock
	}
	return err
}
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == 259
}
