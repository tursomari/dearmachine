//go:build !windows

package main

func runPlatformUpdate(_ []string, _ func(string) string, _ dependencies) (bool, error) {
	return false, nil
}
