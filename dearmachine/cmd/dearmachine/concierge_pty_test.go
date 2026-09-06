package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Re-enter the real main path in a private controlling terminal. The substitute
// concierge makes no provider requests and asserts foreground ownership itself.
func TestConciergeProcess(t *testing.T) {
	if os.Getenv("CONCIERGE_TEST_CHILD") == "1" {
		if !defaultDependencies().isInteractive(os.Stdin) || !defaultDependencies().outputInteractive(os.Stdout) {
			os.Exit(91)
		}
		group, err := unix.IoctlGetInt(0, unix.TIOCGPGRP)
		if err != nil || group != syscall.Getpgrp() {
			os.Exit(92)
		}
		switch os.Getenv("CONCIERGE_TEST_EXIT") {
		case "zero":
			os.Exit(0)
		case "nonzero":
			os.Exit(37)
		case "signal":
			syscall.Kill(os.Getpid(), syscall.SIGTERM)
			select {}
		}
		interrupts := make(chan os.Signal, 2)
		signal.Notify(interrupts, os.Interrupt)
		fmt.Println("concierge ready")
		<-interrupts
		fmt.Println("first interrupt received")
		<-interrupts
		os.Exit(37)
	}
	if os.Getenv("CONCIERGE_TEST_PARENT") == "1" {
		os.Args = []string{"dearmachine"}
		main()
		group, err := unix.IoctlGetInt(0, unix.TIOCGPGRP)
		if err != nil || group != syscall.Getpgrp() {
			os.Exit(93)
		}
		os.Exit(0)
	}
}

func TestConciergeForegroundPTY(t *testing.T) {
	for _, mode := range []struct {
		name string
		code int
	}{{"zero", 0}, {"nonzero", 37}, {"signal", 143}, {"interrupts", 37}} {
		t.Run(mode.name, func(t *testing.T) {
			home := t.TempDir()
			binary := filepath.Join(home, "concierge with spaces")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\nCONCIERGE_TEST_CHILD=1 exec \"$CONCIERGE_TEST_EXE\" -test.run=^TestConciergeProcess$ -- \"$@\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
				t.Fatal(err)
			}
			number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
			if err != nil {
				t.Fatal(err)
			}
			slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer slave.Close()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(exe, "-test.run=^TestConciergeProcess$")
			cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "DEARMACHINE_CONCIERGE_BIN=" + binary, "DEARMACHINE_SOURCE_ROOT=" + home,
				"CONCIERGE_TEST_PARENT=1", "CONCIERGE_TEST_EXE=" + exe, "CONCIERGE_TEST_EXIT=" + mode.name}
			cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			slave.Close()
			defer cmd.Process.Kill()
			output := make(chan string, 1)
			go func() {
				var text strings.Builder
				scanner := bufio.NewScanner(master)
				for scanner.Scan() {
					line := strings.TrimSuffix(scanner.Text(), "\r")
					text.WriteString(line + "\n")
					if strings.Contains(line, "concierge ready") || strings.Contains(line, "first interrupt received") {
						master.Write([]byte{3})
					}
				}
				output <- text.String()
			}()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("foreground handoff timed out")
			}
			text := <-output
			if cmd.ProcessState.ExitCode() != mode.code {
				t.Fatalf("exit %d, want %d: %s", cmd.ProcessState.ExitCode(), mode.code, text)
			}
			if mode.name == "interrupts" && !strings.Contains(text, "first interrupt received") {
				t.Fatalf("parent broke first Ctrl+C: %s", text)
			}
		})
	}
}
