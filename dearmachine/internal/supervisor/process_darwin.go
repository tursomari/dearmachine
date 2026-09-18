package supervisor

import "syscall"

// Darwin has no Pdeathsig. Normal shutdown still signals the child's private
// process group; abrupt supervisor death does not automatically kill the child.
func childProcessAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
