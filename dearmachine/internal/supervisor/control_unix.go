//go:build !windows

package supervisor

import (
	"errors"
	"net"
	"os"
	"time"
)

func listenControl(root string) (net.Listener, error) {
	path := SocketPath(root)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("control path is not a socket; inspect state before retrying")
		}
		// Only the flock owner may remove a stale endpoint.
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}
func dialControl(root string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", SocketPath(root), timeout)
}
