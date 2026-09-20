//go:build !windows

package hostos

import (
	"os"
	"syscall"
)

func Owned(path string, info os.FileInfo) bool {
	s, ok := info.Sys().(*syscall.Stat_t)
	return ok && s.Uid == uint32(os.Getuid())
}
func Private(path string, info os.FileInfo, mask os.FileMode) bool {
	return Owned(path, info) && info.Mode().Perm()&mask == 0
}
func Open(path string, flags int, mode os.FileMode) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, uint32(mode))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
func Protect(path string, mode os.FileMode) error { return os.Chmod(path, mode) }
