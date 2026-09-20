//go:build !windows

package agentmanager

import "os/exec"

func backendLookPath(name string) (string, error) { return exec.LookPath(name) }
