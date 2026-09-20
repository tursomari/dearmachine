//go:build !windows

package hostos

import "os/exec"

func StartManaged(cmd *exec.Cmd) error { return cmd.Start() }
func ReleaseManaged(cmd *exec.Cmd)     {}
