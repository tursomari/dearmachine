//go:build windows

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func runUninstall(args []string, getenv func(string) string, deps dependencies) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(outputOrDiscard(deps.stdout), "Usage: dearmachine uninstall\nPermanently remove this Windows installation and DearMachine private data after terminal confirmation. No --yes bypass.")
		return err
	}
	if len(args) != 0 {
		return errors.New("usage: dearmachine uninstall (no confirmation bypass)")
	}
	if !inputIsInteractive(deps) {
		return errors.New("uninstall requires an interactive terminal and explicit confirmation")
	}
	root := getenv("DEARMACHINE_INSTALL_ROOT")
	if root == "" || !filepath.IsAbs(root) {
		return errors.New("run uninstall through the installed Windows launcher")
	}
	script := filepath.Join(getenv("DEARMACHINE_SOURCE_ROOT"), "scripts", "uninstall-windows.ps1")
	// Validate ownership and all removal paths before asking for confirmation.
	parameters := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, "-Destination", root}
	check := exec.Command(windowsPowerShell(getenv), append(append([]string{}, parameters...), "-Check")...)
	check.Stdout, check.Stderr = deps.stdout, deps.flagOutput
	if err := check.Run(); err != nil {
		return err
	}
	fmt.Fprintln(outputOrDiscard(deps.stdout), "External projects, independent backend installations, and remote accounts will be preserved. No backup is retained. Close other DearMachine terminals first.")
	fmt.Fprint(outputOrDiscard(deps.stdout), "Type UNINSTALL to confirm permanent deletion (anything else cancels): ")
	answer, err := bufio.NewReader(deps.stdin).ReadString('\n')
	if err != nil || strings.TrimSpace(answer) != "UNINSTALL" {
		fmt.Fprintln(outputOrDiscard(deps.stdout), "Uninstall cancelled; nothing changed.")
		return nil
	}
	bootstrap := getenv("DEARMACHINE_BOOTSTRAP_PID")
	if _, err := strconv.Atoi(bootstrap); err != nil {
		bootstrap = "0"
	}
	command := exec.Command(windowsPowerShell(getenv), append(parameters, "-ParentProcess", strconv.Itoa(os.Getpid()), "-LauncherProcess", strconv.Itoa(os.Getppid()), "-BootstrapProcess", bootstrap)...)
	command.Stdout, command.Stderr = deps.stdout, deps.flagOutput
	return command.Run()
}

func refuseDuringUninstall(_ string) error {
	root := os.Getenv("DEARMACHINE_INSTALL_ROOT")
	if root != "" {
		if _, err := os.Stat(filepath.Join(root, ".uninstalling")); err == nil {
			return errors.New("this Windows installation is being removed; inspect the uninstall log in LocalAppData")
		}
	}
	return nil
}
