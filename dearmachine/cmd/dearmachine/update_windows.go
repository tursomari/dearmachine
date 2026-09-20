//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func runPlatformUpdate(args []string, getenv func(string) string, deps dependencies) (bool, error) {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(outputOrDiscard(deps.flagOutput))
	bundle := flags.String("bundle", "", "activate a prepared Windows release bundle")
	recover := flags.Bool("recover", false, "restore the previous Windows release")
	check := flags.Bool("check", false, "show the current Windows release")
	machine := flags.Bool("json", false, "write a versioned machine-readable result")
	if err := flags.Parse(args); err != nil {
		return true, err
	}
	if flags.NArg() != 0 || (*recover && (*check || *bundle != "")) || (*check && *bundle != "") || (*machine && (*recover || *bundle != "")) {
		return true, errors.New("usage: dearmachine update [--check [--json] | --bundle <directory> | --recover]")
	}
	root := getenv("DEARMACHINE_INSTALL_ROOT")
	if root == "" || !filepath.IsAbs(root) {
		return true, errors.New("run update through the installed Windows launcher")
	}
	if *check || (*bundle == "" && !*recover) {
		data, err := os.ReadFile(filepath.Join(root, "current.json"))
		if err != nil {
			return true, err
		}
		var active struct {
			Release string `json:"release"`
		}
		if err := json.Unmarshal(data, &active); err != nil {
			return true, err
		}
		if *machine {
			operation := "install"
			if *check {
				operation = "check"
			}
			return true, json.NewEncoder(outputOrDiscard(deps.stdout)).Encode(map[string]any{"version": 1, "operation": operation, "state": "unsupported"})
		}
		fmt.Fprintf(outputOrDiscard(deps.stdout), "Installed Windows release: %s\nTo update, download and extract a prepared Windows bundle, then run dearmachine update --bundle <directory>. Use --recover to restore the previous release.\n", active.Release)
		return true, nil
	}
	script := filepath.Join(getenv("DEARMACHINE_SOURCE_ROOT"), "scripts", "install-windows.ps1")
	commandArgs := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, "-Destination", root, "-NoPath"}
	if *recover {
		commandArgs = append(commandArgs, "-Rollback")
	} else {
		absolute, err := filepath.Abs(*bundle)
		if err != nil {
			return true, err
		}
		commandArgs = append(commandArgs, "-Update", "-Bundle", absolute)
	}
	command := exec.Command(windowsPowerShell(getenv), commandArgs...)
	command.Stdin, command.Stdout, command.Stderr = deps.stdin, deps.stdout, deps.flagOutput
	return true, command.Run()
}

func windowsPowerShell(getenv func(string) string) string {
	root := getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
}
