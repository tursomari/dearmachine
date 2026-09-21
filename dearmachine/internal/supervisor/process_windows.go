package supervisor

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/dearmachine/dearmachine/internal/hostos"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

var ErrLocked = errors.New("supervisor is already owned; inspect dearmachine status")

func DefaultSocketPath(home func() (string, error)) (string, error) {
	h, err := home()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(h) {
		return "", errors.New("user home directory must be absolute")
	}
	return SocketPath(filepath.Join(h, ".dearmachine")), nil
}
func SocketPath(root string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root))))
	return fmt.Sprintf(`\\.\pipe\dearmachine-%x`, sum[:16])
}
func LogPath(root string) string { return filepath.Join(root, "log", "dearmachine.log") }
func privateDir(path string) error {
	_, err := os.Lstat(path)
	missing := os.IsNotExist(err)
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	if missing {
		if err := hostos.Protect(path, 0700); err != nil {
			return err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || !hostos.Private(path, info, 0077) {
		return fmt.Errorf("state directory must be private and owned by the current user: %s", path)
	}
	return nil
}
func privateFile(path string) (*os.File, error) {
	_, err := os.Lstat(path)
	missing := os.IsNotExist(err)
	f, err := hostos.Open(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	if missing {
		if err = hostos.Protect(path, 0600); err != nil {
			f.Close()
			return nil, err
		}
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	var wininfo windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &wininfo)
	if err != nil || !info.Mode().IsRegular() || wininfo.NumberOfLinks != 1 || !hostos.Private(path, info, 0077) {
		f.Close()
		return nil, errors.New("state file must be a private regular file with one link")
	}
	return f, nil
}
func acquire(root string) (*os.File, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("state directory must be absolute")
	}
	for _, p := range []string{root, filepath.Join(root, "run")} {
		if err := privateDir(p); err != nil {
			return nil, err
		}
	}
	f, err := privateFile(filepath.Join(root, "run", "supervisor.lock"))
	if err != nil {
		return nil, err
	}
	if err = hostos.Flock(int(f.Fd()), hostos.LOCK_EX|hostos.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, hostos.ErrWouldBlock) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return f, nil
}
func openLog(root string) (*os.File, error) {
	if err := privateDir(filepath.Join(root, "log")); err != nil {
		return nil, err
	}
	return privateFile(LogPath(root))
}
func CheckAvailable(root string) error {
	f, err := acquire(root)
	if err != nil {
		return err
	}
	return f.Close()
}
func HasRecord(root string) (bool, error) {
	_, err := os.Lstat(filepath.Join(root, "run", "supervisor.lock"))
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}
func OwnerPresent(root string) (bool, error) {
	path := filepath.Join(root, "run", "supervisor.lock")
	f, err := hostos.Open(path, os.O_RDONLY, 0)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || !hostos.Private(path, info, 0077) {
		return false, errors.New("cannot verify supervisor ownership: invalid lock file")
	}
	err = hostos.Flock(int(f.Fd()), hostos.LOCK_SH|hostos.LOCK_NB)
	if errors.Is(err, hostos.ErrWouldBlock) {
		return true, nil
	}
	return false, err
}

type childProcess struct {
	cmd  *exec.Cmd
	done chan error
}

func spawn(argv []string, log *os.File) (*childProcess, error) {
	return spawnInDirectory(argv, log, "")
}
func spawnInDirectory(argv []string, log *os.File, dir string) (*childProcess, error) {
	if len(argv) == 0 {
		return nil, errors.New("foreground child command is required")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdout = log
	cmd.Stderr = log
	if err := hostos.StartManaged(cmd); err != nil {
		return nil, err
	}
	child := &childProcess{cmd: cmd, done: make(chan error, 1)}
	go func() { err := cmd.Wait(); hostos.ReleaseManaged(cmd); child.done <- err }()
	return child, nil
}
func (c *childProcess) terminate() { _ = hostos.Signal(c.cmd.Process.Pid, syscall.SIGTERM) }
func (c *childProcess) kill()      { _ = hostos.KillManaged(c.cmd) }
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
	cmd.Stdout = log
	cmd.Stderr = log
	// OpenSSH owns session descendants in a kill-on-close job that permits
	// explicit breakaway. Apply the same bounded escape as asynchronous workers
	// so ending the launching SSH session does not stop the supervisor.
	cmd.SysProcAttr = hostos.DetachedWorker()
	if err = cmd.Start(); err != nil {
		return 0, err
	}
	go cmd.Wait()
	return cmd.Process.Pid, nil
}
