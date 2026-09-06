// Package supervisor owns a single foreground daemon independently of any UI.
package supervisor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

var ErrLocked = errors.New("supervisor is already owned; inspect dearmachine status")

func SocketPath(root string) string { return filepath.Join(root, "run", "supervisor.sock") }
func LogPath(root string) string    { return filepath.Join(root, "log", "dearmachine.log") }

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 || !ok || stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("state directory must be private and owned by the current user: %s", path)
	}
	return nil
}

func privateFile(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_APPEND|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		file.Close()
		return nil, fmt.Errorf("state file must be a private regular file owned by the current user: %s", path)
	}
	return file, nil
}

func acquire(root string) (*os.File, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("state directory must be absolute")
	}
	if err := privateDir(root); err != nil {
		return nil, err
	}
	if err := privateDir(filepath.Join(root, "run")); err != nil {
		return nil, err
	}
	file, err := privateFile(filepath.Join(root, "run", "supervisor.lock"))
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return file, nil
}

func openLog(root string) (*os.File, error) {
	if err := privateDir(filepath.Join(root, "log")); err != nil {
		return nil, err
	}
	return privateFile(LogPath(root))
}

type childProcess struct {
	cmd  *exec.Cmd
	done chan error
}

func spawn(argv []string, log *os.File) (*childProcess, error) {
	return spawnInDirectory(argv, log, "")
}

func spawnInDirectory(argv []string, log *os.File, directory string) (*childProcess, error) {
	if len(argv) == 0 {
		return nil, errors.New("foreground child command is required")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = directory
	cmd.Stdout, cmd.Stderr = log, log
	// A private process group lets shutdown reach work owned by this child.
	// Linux also kills the direct child if its supervisor dies unexpectedly.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	child := &childProcess{cmd: cmd, done: make(chan error, 1)}
	go func() { child.done <- cmd.Wait() }()
	return child, nil
}

func (c *childProcess) terminate() { _ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM) }
func (c *childProcess) kill()      { _ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL) }

// HasRecord is read-only. A leftover record requires socket-based recovery;
// it must never cause a fallback to signalling a stale PID.
func HasRecord(root string) (bool, error) {
	for _, path := range []string{filepath.Join(root, "run", "supervisor.lock"), SocketPath(root)} {
		if _, err := os.Lstat(path); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	return false, nil
}

// CheckAvailable probes ownership before spawning. The child must still acquire
// the lock itself: this probe is not a reservation against concurrent starts.
func CheckAvailable(root string) error {
	lock, err := acquire(root)
	if err != nil {
		return err
	}
	return lock.Close()
}

// StartDetached starts the non-agent owner in its own session. The caller
// confirms startup through the socket, never through this diagnostic PID.
func StartDetached(root string, argv []string) (int, error) {
	if len(argv) == 0 {
		return 0, errors.New("supervisor command is required")
	}
	if err := privateDir(root); err != nil {
		return 0, err
	}
	log, err := openLog(root)
	if err != nil {
		return 0, err
	}
	defer log.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}
