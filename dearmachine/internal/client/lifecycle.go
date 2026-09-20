package client

import (
	"errors"
	"fmt"
	"github.com/dearmachine/dearmachine/internal/hostos"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var ErrDaemonStopped = errors.New("DearMachine is not running")

func DefaultDaemonLogPath(userHomeDir func() (string, error)) (string, error) {
	home, err := resolveDeviceHome(userHomeDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".dearmachine", "log", "dearmachine.log"), nil
}

func DefaultDaemonReadyPath(userHomeDir func() (string, error)) (string, error) {
	home, err := resolveDeviceHome(userHomeDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".dearmachine", "run", "dearmachine.ready"), nil
}

func daemonReadyPath(lockPath string) string {
	return filepath.Join(filepath.Dir(lockPath), "dearmachine.ready")
}

func DaemonStatus(lockPath string) (int, bool, error) {
	content, err := readDaemonFile(lockPath)
	if os.IsNotExist(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read DearMachine daemon lock: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || pid <= 0 {
		return 0, false, fmt.Errorf("DearMachine daemon lock %s is invalid; inspect it before retrying", lockPath)
	}
	err = hostos.Signal(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return pid, true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return pid, false, nil
	}
	return 0, false, fmt.Errorf("check DearMachine daemon PID %d: %w", pid, err)
}

func WaitDaemonReady(lockPath string, expectedPID int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		pid, running, err := DaemonStatus(lockPath)
		if err == nil && running {
			if pid != expectedPID {
				return fmt.Errorf("daemon lock is owned by PID %d, not started PID %d", pid, expectedPID)
			}
			ready, readErr := readDaemonFile(daemonReadyPath(lockPath))
			if readErr == nil && strings.TrimSpace(string(ready)) == strconv.Itoa(expectedPID) {
				return nil
			}
		}
		if signalErr := hostos.Signal(expectedPID, 0); errors.Is(signalErr, syscall.ESRCH) {
			return fmt.Errorf("started PID %d exited before readiness", expectedPID)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for PID %d", expectedPID)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func StopDaemon(lockPath string, timeout time.Duration) error {
	pid, running, err := DaemonStatus(lockPath)
	if err != nil {
		return err
	}
	if !running {
		if pid > 0 {
			_ = os.Remove(lockPath)
		}
		_ = os.Remove(daemonReadyPath(lockPath))
		return ErrDaemonStopped
	}
	if err := hostos.Signal(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stop DearMachine daemon PID %d: %w", pid, err)
	}
	deadline := time.Now().Add(timeout)
	for {
		owner, alive, err := DaemonStatus(lockPath)
		if err == nil && (!alive || owner != pid) {
			if owner == pid {
				_ = os.Remove(lockPath)
			}
			_ = os.Remove(daemonReadyPath(lockPath))
			return nil
		}
		if os.IsNotExist(err) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for DearMachine daemon PID %d to stop", pid)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
