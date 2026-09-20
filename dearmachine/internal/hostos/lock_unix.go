//go:build !windows

package hostos

import "syscall"

const (
	LOCK_EX = syscall.LOCK_EX
	LOCK_SH = syscall.LOCK_SH
	LOCK_NB = syscall.LOCK_NB
	LOCK_UN = syscall.LOCK_UN
)

var ErrWouldBlock = syscall.EWOULDBLOCK

func Flock(fd int, how int) error { return syscall.Flock(fd, how) }
func Alive(pid int) bool          { return syscall.Kill(pid, 0) == nil }
