package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestBareDispatchTTYGuard(t *testing.T) {
	for _, tty := range []struct{ in, out bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		home := t.TempDir()
		var output strings.Builder
		deps := dependencies{stdout: &output, userHomeDir: func() (string, error) { return home, nil }, isInteractive: func(io.Reader) bool { return tty.in }, outputInteractive: func(io.Writer) bool { return tty.out }}
		if err := run(nil, func(string) string { return "" }, deps); (err != nil) != (tty.in && tty.out) {
			t.Fatalf("TTY %v: %v", tty, err)
		}
		want := "Usage:"
		if tty.in && tty.out {
			want = "Installer mode"
		}
		if !strings.Contains(output.String(), want) {
			t.Fatalf("TTY %v: %s", tty, output.String())
		}
		entries, _ := os.ReadDir(home)
		if len(entries) != 0 {
			t.Fatal("bare invocation mutated state")
		}
	}
}

func TestBareInstalledAndPartialRouting(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	deps.outputInteractive = func(io.Writer) bool { return true }
	var output strings.Builder
	deps.stdout = &output
	// Config alone is partial, never a reason to run a fresh installation.
	if err := run(nil, func(string) string { return "" }, deps); err == nil || !strings.Contains(output.String(), "Recovery") {
		t.Fatalf("partial: %s %v", output.String(), err)
	}
	makeUpTestPair(t, deps, "concierge")
	output.Reset()
	if err := run(nil, func(string) string { return "" }, deps); err == nil || !strings.Contains(output.String(), "Concierge mode") {
		t.Fatalf("installed: %s %v", output.String(), err)
	}
	output.Reset()
	if err := run([]string{"--help"}, func(string) string { return "" }, deps); err != nil || !strings.Contains(output.String(), "Usage:") {
		t.Fatalf("help: %s %v", output.String(), err)
	}
}

func TestConservativeInstallDetection(t *testing.T) {
	for _, kind := range []string{"other-install", "symlink", "file", "unreadable", "invalid-registry"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			root := filepath.Join(home, ".dearmachine")
			switch kind {
			case "other-install":
				os.Mkdir(filepath.Join(home, ".machtiani"), 0700)
			case "symlink":
				os.Symlink(t.TempDir(), root)
			case "file":
				os.WriteFile(root, []byte("partial"), 0600)
			case "unreadable":
				os.Mkdir(root, 0000)
				t.Cleanup(func() { os.Chmod(root, 0700) })
			case "invalid-registry":
				os.Mkdir(root, 0700)
				os.WriteFile(filepath.Join(root, "pairs.toml"), []byte("bad toml ["), 0600)
			}
			state := detectInstallation(home)
			if kind == "other-install" && runtime.GOOS == "windows" {
				if state != "absent" {
					t.Fatalf("independent Windows Machtiani store blocked setup: %s", state)
				}
				return
			}
			if state == "absent" || state == "installed" {
				t.Fatalf("unsafe detection: %s", state)
			}
		})
	}
}

func TestDevNullIsNotTTY(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if defaultDependencies().isInteractive(f) {
		t.Fatal("character device mistaken for TTY")
	}
}

func TestBareLaunchRouting(t *testing.T) {
	for _, state := range []string{"absent", "installed", "partial", "invalid", "unreadable"} {
		t.Run(state, func(t *testing.T) {
			home := t.TempDir()
			var output strings.Builder
			deps := dependencies{stdin: strings.NewReader(""), stdout: &output, flagOutput: &output,
				userHomeDir:   func() (string, error) { return home, nil },
				isInteractive: func(io.Reader) bool { return true }, outputInteractive: func(io.Writer) bool { return true }}
			if state == "installed" {
				deps = testDependencies(t, &fakeApplication{})
				makeUpTestPair(t, deps, "concierge")
				deps.stdout, deps.flagOutput = &output, &output
				deps.outputInteractive = func(io.Writer) bool { return true }
			} else if state != "absent" {
				root := filepath.Join(home, ".dearmachine")
				os.Mkdir(root, 0700)
				if state == "invalid" {
					os.WriteFile(filepath.Join(root, "pairs.toml"), []byte("bad toml ["), 0600)
				}
				if state == "unreadable" {
					os.Chmod(root, 0000)
					defer os.Chmod(root, 0700)
				}
			}
			bin := filepath.Join(home, "concierge")
			source := filepath.Join(home, "source with spaces")
			calls := 0
			deps.lookPath = func(name string) (string, error) {
				if name != bin {
					t.Fatalf("guessed binary %q", name)
				}
				return bin, nil
			}
			childExit := errors.New("child result")
			deps.launchConcierge = func(path string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
				calls++
				want := []string{"--concierge"}
				if state == "absent" {
					want = append(want, "--source-root", source)
				}
				if path != bin || !reflect.DeepEqual(args, want) || stdin != deps.stdin || stdout != deps.stdout || stderr != deps.flagOutput {
					t.Fatalf("launch: %q %q", path, args)
				}
				return childExit
			}
			env := func(key string) string {
				if key == "DEARMACHINE_CONCIERGE_BIN" {
					return bin
				}
				if key == "DEARMACHINE_SOURCE_ROOT" {
					return source
				}
				return ""
			}
			err := run(nil, env, deps)
			if state == "absent" || state == "installed" {
				if calls != 1 || !errors.Is(err, childExit) {
					t.Fatalf("calls %d, error %v", calls, err)
				}
			} else if calls != 0 || err == nil || !strings.Contains(output.String(), "Recovery") {
				t.Fatalf("unsafe routing: %d %v %s", calls, err, output.String())
			}
			for _, args := range [][]string{nil, {"--help"}} {
				deps.isInteractive = func(io.Reader) bool { return false }
				deps.userHomeDir = func() (string, error) { t.Fatal("help performed detection"); return "", nil }
				if err := run(args, env, deps); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestBareDiscoveryFallback(t *testing.T) {
	for _, override := range []string{"", "missing-concierge", "relative/path"} {
		t.Run(override, func(t *testing.T) {
			home := t.TempDir()
			var output strings.Builder
			deps := dependencies{stdout: &output, userHomeDir: func() (string, error) { return home, nil }, isInteractive: func(io.Reader) bool { return true }, outputInteractive: func(io.Writer) bool { return true }}
			deps.lookPath = func(name string) (string, error) {
				want := override
				if want == "" {
					want = "machtiani-installer"
				}
				if name != want && !(override == "" && filepath.IsAbs(name)) {
					t.Fatalf("unexpected discovery: %q", name)
				}
				return "", os.ErrNotExist
			}
			deps.launchConcierge = func(string, []string, io.Reader, io.Writer, io.Writer) error {
				t.Fatal("launched missing binary")
				return nil
			}
			err := run(nil, func(key string) string {
				if key == "DEARMACHINE_CONCIERGE_BIN" {
					return override
				}
				return ""
			}, deps)
			if err == nil || !strings.Contains(output.String(), "machtiani-installer --concierge --source-root <absolute-source-root>") || !strings.Contains(output.String(), "DEARMACHINE_CONCIERGE_BIN") {
				t.Fatalf("guidance: %s %v", output.String(), err)
			}
		})
	}
}

func TestConciergeDiscoveryOrder(t *testing.T) {
	home := t.TempDir()
	local := filepath.Join(home, ".local", "bin", "machtiani-installer")
	nix := filepath.Join(home, ".nix-profile", "bin", "machtiani-installer")
	for _, target := range []string{"machtiani-installer", local, nix, "missing"} {
		var calls []string
		_, err := discoverConcierge("", home, func(name string) (string, error) {
			calls = append(calls, name)
			if name == target {
				return name, nil
			}
			return "", os.ErrNotExist
		})
		want := []string{"machtiani-installer"}
		if target != want[0] {
			want = append(want, local)
		}
		if target != want[len(want)-1] {
			want = append(want, nix)
		}
		if !reflect.DeepEqual(calls, want) || (err != nil) != (target == "missing") {
			t.Fatalf("%s: %v %v", target, calls, err)
		}
	}
	calls := 0
	_, err := discoverConcierge("explicit-missing", home, func(string) (string, error) { calls++; return "", os.ErrNotExist })
	if err == nil || calls != 1 {
		t.Fatalf("override fell back: %d %v", calls, err)
	}
}

func TestConciergePinsLaunchingNativeBinary(t *testing.T) {
	env := conciergeEnvironment([]string{"HOME=/isolated", "DEARMACHINE_NATIVE_BIN=/wrong/binary"}, "/fixture/dearmachine")
	count := 0
	for _, value := range env {
		if strings.HasPrefix(value, "DEARMACHINE_NATIVE_BIN=") {
			count++
			if value != "DEARMACHINE_NATIVE_BIN=/fixture/dearmachine" {
				t.Fatal(value)
			}
		}
	}
	if count != 1 {
		t.Fatalf("native discovery: %v", env)
	}
}

func TestConciergeRelaunchesOnlyThroughValidatedManagedLauncher(t *testing.T) {
	t.Setenv("DEARMACHINE_NATIVE_BIN", "/old/native")
	t.Setenv("DEARMACHINE_CONCIERGE_BIN", "/old/concierge")
	t.Setenv("DEARMACHINE_SOURCE_ROOT", "/old/source")
	deps := testDependencies(t, &fakeApplication{})
	home, err := deps.userHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	makeUpTestPair(t, deps, "concierge-relaunch")
	deps.outputInteractive = func(io.Writer) bool { return true }
	deps.isInteractive = func(io.Reader) bool { return true }
	var output strings.Builder
	deps.stdout, deps.flagOutput = &output, &output
	deps.lookPath = func(name string) (string, error) { return name, nil }
	deps.launchConcierge = func(string, []string, io.Reader, io.Writer, io.Writer) error {
		return &conciergeExitError{code: conciergeRelaunchExitCode, cause: errors.New("relaunch")}
	}
	root := filepath.Join(home, ".local", "share", "dearmachine")
	target := filepath.Join(root, "current", "bin", "dearmachine")
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(home, ".local", "bin", "dearmachine")
	if err := os.MkdirAll(filepath.Dir(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, launcher); err != nil {
		t.Fatal(err)
	}
	called := false
	deps.execProcess = func(path string, args, environment []string) error {
		called = true
		if path != launcher || !reflect.DeepEqual(args, []string{launcher}) {
			t.Fatalf("unexpected relaunch: %q %q", path, args)
		}
		for _, value := range environment {
			if strings.HasPrefix(value, "DEARMACHINE_NATIVE_BIN=") || strings.HasPrefix(value, "DEARMACHINE_CONCIERGE_BIN=") || strings.HasPrefix(value, "DEARMACHINE_SOURCE_ROOT=") {
				t.Fatalf("stale release identity leaked into relaunch: %q", value)
			}
		}
		return nil
	}
	env := func(key string) string {
		if key == "DEARMACHINE_CONCIERGE_BIN" {
			return "/old/concierge"
		}
		return ""
	}
	if err := runBare(env, deps); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("updated launcher was not executed")
	}

	if err := os.Remove(launcher); err != nil {
		t.Fatal(err)
	}
	called = false
	output.Reset()
	if err := runBare(env, deps); err != nil {
		t.Fatal(err)
	}
	if called || !strings.Contains(output.String(), "could not be validated") {
		t.Fatalf("unsafe relaunch result: called=%v output=%q", called, output.String())
	}
}
