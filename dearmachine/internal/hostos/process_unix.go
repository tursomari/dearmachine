//go:build !windows

package hostos

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func Signal(pid int, sig syscall.Signal) error   { return syscall.Kill(pid, sig) }
func Detached() *syscall.SysProcAttr             { return &syscall.SysProcAttr{Setsid: true} }
func Exec(path string, args, env []string) error { return syscall.Exec(path, args, env) }
func NotifyContext(ctx context.Context, s ...os.Signal) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, s...)
}
