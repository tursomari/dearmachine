package supervisor

import (
	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
	"net"
	"time"
)

func listenControl(root string) (net.Listener, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return winio.ListenPipe(SocketPath(root), &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;" + user.User.Sid.String() + ")(A;;GA;;;SY)", InputBufferSize: 65536, OutputBufferSize: 65536})
}
func dialControl(root string, timeout time.Duration) (net.Conn, error) {
	return winio.DialPipe(SocketPath(root), &timeout)
}
