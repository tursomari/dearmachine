//go:build !linux && !windows

package main

import "errors"

func runUninstall(_ []string, _ func(string) string, _ dependencies) error {
	return errors.New("confirmed native uninstall is currently supported on Linux only")
}

func refuseDuringUninstall(string) error { return nil }
