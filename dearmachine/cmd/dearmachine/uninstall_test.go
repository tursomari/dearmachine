//go:build linux

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func uninstallFixture(t *testing.T) (string, dependencies, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, ".dearmachine", "pairs", "fixture", "state", "dearmachine.db")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture database"), 0600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	deps := defaultDependencies()
	deps.userHomeDir = func() (string, error) { return home, nil }
	deps.stdout, deps.flagOutput = out, out
	deps.isInteractive = func(_ io.Reader) bool { return true }
	return home, deps, out
}

func TestUninstallCancellationDoesNotMutate(t *testing.T) {
	for _, answer := range []string{"", "no\n", "yes\n"} {
		t.Run(answer, func(t *testing.T) {
			home, deps, out := uninstallFixture(t)
			deps.stdin = strings.NewReader(answer)
			if err := run([]string{"uninstall"}, func(string) string { return "" }, deps); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "UNINSTALL") || !strings.Contains(out.String(), "cancelled") {
				t.Fatal(out.String())
			}
			if _, err := os.Stat(filepath.Join(home, ".dearmachine/pairs/fixture/state/dearmachine.db")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUninstallRequiresTerminalAndRejectsBypass(t *testing.T) {
	_, deps, _ := uninstallFixture(t)
	deps.stdin = strings.NewReader("UNINSTALL\n")
	deps.isInteractive = func(_ io.Reader) bool { return false }
	for _, args := range [][]string{{"uninstall"}, {"uninstall", "--yes"}} {
		if err := run(args, func(string) string { return "" }, deps); err == nil {
			t.Fatal("expected rejection", args)
		}
	}
}

func TestUninstallPlanRejectsSymlinkedRootsAndBusyInstallation(t *testing.T) {
	for _, kind := range []string{"symlink", "update", "bootstrap"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			root := filepath.Join(home, ".local/share/dearmachine")
			if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				if err := os.Symlink(t.TempDir(), root); err != nil {
					t.Fatal(err)
				}
			} else {
				lock := "update.lock"
				if kind == "bootstrap" {
					lock = ".bootstrap-lock"
				}
				if err := os.MkdirAll(filepath.Join(root, lock), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := buildUninstallPlan(home, func(string) string { return "" }); err == nil {
				t.Fatal("unsafe plan accepted")
			}
		})
	}
}

func TestUninstallPlanPreservesIndependentMachtiani(t *testing.T) {
	home := t.TempDir()
	plan, err := buildUninstallPlan(home, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range plan.paths {
		if path == filepath.Join(home, ".machtiani") || path == filepath.Join(home, ".config/machtiani") {
			t.Fatal("independent configuration selected", path)
		}
	}
}

func TestUninstallServiceShutdownMustBeConfirmed(t *testing.T) {
	for _, fail := range []string{"", "disable", "active", "enabled"} {
		t.Run(fail, func(t *testing.T) {
			home := t.TempDir()
			p := uninstallPlan{home: home, units: []string{conciergeUnit}}
			var calls []string
			manager := serviceManager{home: home, run: func(name string, args ...string) (string, error) {
				call := name + " " + strings.Join(args, " ")
				calls = append(calls, call)
				if strings.Contains(call, "disable --now") {
					if fail == "disable" {
						return "", errors.New("unavailable")
					}
					return "", nil
				}
				if strings.Contains(call, "ActiveState") {
					if fail == "active" {
						return "active", nil
					}
					return "inactive", nil
				}
				if fail == "enabled" {
					return "enabled", nil
				}
				return "disabled", nil
			}}
			err := p.stop(manager)
			if (err != nil) != (fail != "") {
				t.Fatalf("shutdown=%v, failure=%s", err, fail)
			}
			if len(calls) == 0 || !strings.Contains(calls[0], "disable --now") {
				t.Fatal(calls)
			}
			if _, err := os.Stat(filepath.Join(home, ".dearmachine")); !os.IsNotExist(err) {
				t.Fatal("shutdown recreated private state")
			}
		})
	}
}
