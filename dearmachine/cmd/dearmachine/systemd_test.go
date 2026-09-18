package main

import (
	"context"
	"errors"
	"github.com/dearmachine/dearmachine/internal/supervisor"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSystemdSeparateConsent(t *testing.T) {
	for _, scenario := range []string{"default", "unavailable", "use", "persist-without-use", "persist", "partial", "disable"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			var calls []string
			manager := serviceManager{home: home, executable: "/test/bin/dearmachine", run: func(name string, args ...string) (string, error) {
				call := name + " " + strings.Join(args, " ")
				calls = append(calls, call)
				if scenario == "unavailable" || (scenario == "partial" && strings.Contains(call, "enable-linger")) {
					return "", errors.New("unavailable")
				}
				if strings.Contains(call, "show-user") {
					return "yes", nil
				}
				return "", nil
			}}
			if scenario == "default" {
				owner, err := selectSupervision(manager)
				if err != nil || owner != "supervisor-lite" || len(calls) != 0 {
					t.Fatalf("implicit consent: %s %v %v", owner, err, calls)
				}
				return
			}
			if scenario == "persist-without-use" {
				if err := manager.configure("persistence", "on"); err == nil || len(calls) != 0 {
					t.Fatalf("persistence bypass: %v %v", err, calls)
				}
				return
			}
			err := manager.configure("systemd", "on")
			if scenario == "unavailable" {
				if err == nil {
					t.Fatal("unusable manager accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.Join(calls, "\n"), "enable-linger") || strings.Contains(strings.Join(calls, "\n"), "--user enable") {
				t.Fatal("use implied persistence")
			}
			consent, err := manager.load()
			if err != nil || !consent.UseSystemd || consent.Persistence {
				t.Fatalf("consent: %+v %v", consent, err)
			}
			if scenario == "persist" || scenario == "partial" || scenario == "disable" {
				err = manager.configure("persistence", "on")
				if scenario == "partial" {
					if err == nil {
						t.Fatal("partial setup reported success")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(strings.Join(calls, "\n"), "loginctl enable-linger") || !strings.Contains(strings.Join(calls, "\n"), "--user enable") {
					t.Fatalf("missing persistence: %v", calls)
				}
			}
			if scenario == "disable" {
				if err := manager.configure("persistence", "off"); err != nil {
					t.Fatal(err)
				}
				if err := manager.configure("systemd", "off"); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSystemdRefusesResidentOwner(t *testing.T) {
	deps, root := supervisedDeps(t)
	home, _ := deps.userHomeDir()
	manager := serviceManager{home: home, executable: "/test/bin/dearmachine", run: func(string, ...string) (string, error) {
		t.Fatal("service mutation with resident owner")
		return "", nil
	}}
	if err := manager.configure("systemd", "on"); err == nil {
		t.Fatal("transferred owner")
	}
	if _, err := os.Stat(filepath.Join(root, "run", "supervisor.sock")); err != nil {
		t.Fatal(err)
	}
}

func TestSystemdUnitKeepsSupervisorAliveAfterWorkerOOM(t *testing.T) {
	manager := serviceManager{home: filepath.Join(t.TempDir(), "home with spaces%"), executable: "/test/bin/dearmachine"}
	unit := manager.unit()
	for _, want := range []string{"_supervise", "up", "--foreground", "OOMPolicy=continue", "Restart=on-failure", "RestartSec=5s", "KillMode=control-group", "%%"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("missing %s: %s", want, unit)
		}
	}
}

func TestObservedPersistenceIsNotSavedConsent(t *testing.T) {
	for _, state := range []string{"enabled", "disabled", "failed", "no-linger"} {
		t.Run(state, func(t *testing.T) {
			m := serviceManager{home: t.TempDir(), run: func(name string, args ...string) (string, error) {
				if state == "failed" {
					return "", errors.New("offline")
				}
				if name == "loginctl" {
					if state == "no-linger" {
						return "no", nil
					}
					return "yes", nil
				}
				if state == "disabled" {
					return "disabled", nil
				}
				return "enabled", nil
			}}
			os.MkdirAll(m.root(), 0700)
			if err := m.save(supervisionConsent{Version: 1, UseSystemd: true, Persistence: true}); err != nil {
				t.Fatal(err)
			}
			want := state
			if state == "failed" || state == "no-linger" {
				want = "unknown"
			}
			if got := m.persistence(); got != want {
				t.Fatalf("observed %s, want %s", got, want)
			}
		})
	}
}

func TestSocketPersistenceProbeDoesNotRequireMutationConsent(t *testing.T) {
	for _, saved := range []bool{false, true} {
		t.Run(strconv.FormatBool(saved), func(t *testing.T) {
			m := serviceManager{home: t.TempDir(), run: func(name string, args ...string) (string, error) {
				if name == "systemctl" && strings.Join(args, " ") == "--user show "+conciergeUnit+" --property=UnitFileState --value" {
					return "enabled", nil
				}
				if name == "loginctl" && strings.Join(args, " ") == "show-user --property=Linger --value" {
					return "yes", nil
				}
				t.Fatalf("unexpected command: %s %v", name, args)
				return "", errors.New("unexpected command")
			}}
			if saved {
				if err := os.MkdirAll(m.root(), 0700); err != nil {
					t.Fatal(err)
				}
				if err := m.save(supervisionConsent{Version: 1}); err != nil {
					t.Fatal(err)
				}
			}
			if got := m.persistence(); got != "enabled" {
				t.Fatalf("observation gated on consent: %s", got)
			}
		})
	}
}

func TestSystemdCLIWithMockExecutables(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	home, _ := deps.userHomeDir()
	bin := filepath.Join(home, "bin")
	os.MkdirAll(bin, 0700)
	log := filepath.Join(home, "calls")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("PATH", bin)
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\ncase \"$*\" in *UnitFileState*) echo enabled;; *Linger*) echo yes;; esac\n"
	for _, name := range []string{"systemctl", "loginctl"} {
		os.WriteFile(filepath.Join(bin, name), []byte(script), 0700)
	}
	for _, args := range [][]string{{"systemd", "on"}, {"persistence", "on"}, {"persistence", "status"}, {"persistence", "off"}, {"systemd", "off"}} {
		if err := run(args, os.Getenv, deps); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	data, _ := os.ReadFile(log)
	if !strings.Contains(string(data), "enable-linger") || !strings.Contains(string(data), "--user disable") {
		t.Fatalf("calls: %s", data)
	}
}

func TestSystemdStatusExplainsUnknownWithoutChangingConsent(t *testing.T) {
	for _, saved := range []bool{false, true} {
		t.Run(strconv.FormatBool(saved), func(t *testing.T) {
			deps := testDependencies(t, &fakeApplication{})
			home, _ := deps.userHomeDir()
			t.Setenv("PATH", t.TempDir()) // No usable systemd manager.
			m := serviceManager{home: home}
			if saved {
				if err := os.MkdirAll(m.root(), 0700); err != nil {
					t.Fatal(err)
				}
				if err := m.save(supervisionConsent{Version: 1}); err != nil {
					t.Fatal(err)
				}
			}
			before, beforeErr := os.ReadFile(m.consentPath())
			for _, command := range []string{"systemd", "persistence"} {
				var output strings.Builder
				deps.stdout = &output
				if err := run([]string{command, "status"}, os.Getenv, deps); err != nil {
					t.Fatal(err)
				}
				for _, want := range []string{"Saved permission (not observed state): service use=false; reboot/linger=false", "after reboot (before login): cannot verify", "systemd user manager unavailable"} {
					if !strings.Contains(output.String(), want) {
						t.Fatalf("missing %q: %s", want, output.String())
					}
				}
			}
			after, afterErr := os.ReadFile(m.consentPath())
			if string(before) != string(after) || os.IsNotExist(beforeErr) != os.IsNotExist(afterErr) {
				t.Fatal("status changed saved consent")
			}
		})
	}
}

func TestSystemdStartUsesOneNativeOwner(t *testing.T) {
	home := socketTestHome(t)
	root := filepath.Join(home, ".dearmachine")
	os.MkdirAll(root, 0700)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	started := false
	defer func() {
		if started {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
	}()
	starts := 0
	m := serviceManager{home: home, executable: "/test/bin/dearmachine", run: func(_ string, args ...string) (string, error) {
		command := strings.Join(args, " ")
		if strings.Contains(command, "--user start") {
			starts++
			started = true
			go func() {
				done <- supervisor.Run(ctx, supervisor.Config{StateDir: root, Command: []string{testExecutable(t, "sleep"), "60"}})
			}()
		}
		if strings.Contains(command, "MainPID") {
			return strconv.Itoa(os.Getpid()), nil
		}
		return "", nil
	}}
	if err := m.save(supervisionConsent{Version: 1, UseSystemd: true}); err != nil {
		t.Fatal(err)
	}
	first, err := startWithServiceManager(m, []string{"up", "--foreground"}, root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := startWithServiceManager(m, []string{"up", "--foreground"}, root)
	if err != nil || first != second || starts != 1 {
		t.Fatalf("duplicated owner: %d %d %d %v", first, second, starts, err)
	}
	if _, err := startWithServiceManager(m, []string{"up", "--foreground", "--verbose"}, root); err == nil {
		t.Fatal("silently ignored explicit flags")
	}
	if _, err := supervisor.Request(root, "down", time.Second); err != nil {
		t.Fatal(err)
	}
	s, err := supervisor.Request(root, "status", time.Second)
	if err != nil || s.Supervisor != "stopped" {
		t.Fatalf("down: %+v %v", s, err)
	}
}

func TestSystemdDoesNotAdoptForeignSupervisor(t *testing.T) {
	deps, root := supervisedDeps(t)
	home, _ := deps.userHomeDir()
	m := serviceManager{home: home, executable: "/test/bin/dearmachine", run: func(string, ...string) (string, error) { return "0", nil }}
	if err := m.save(supervisionConsent{Version: 1, UseSystemd: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := startWithServiceManager(m, []string{"up", "--foreground"}, root); err == nil {
		t.Fatal("foreign owner adopted")
	}
}

func TestSystemdStartupRaceDoesNotAdoptForeignOwner(t *testing.T) {
	home := socketTestHome(t)
	root := filepath.Join(home, ".dearmachine")
	os.MkdirAll(root, 0700)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	started := false
	defer func() {
		if started {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
	}()
	m := serviceManager{home: home, executable: "/test/bin/dearmachine", run: func(_ string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "--user start") {
			started = true
			go func() {
				done <- supervisor.Run(ctx, supervisor.Config{StateDir: root, Command: []string{testExecutable(t, "sleep"), "60"}})
			}()
		}
		return "0", nil
	}}
	if err := m.save(supervisionConsent{Version: 1, UseSystemd: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := startWithServiceManager(m, []string{"up", "--foreground"}, root); err == nil {
		t.Fatal("adopted a foreign owner racing service startup")
	}
}

func TestSystemdUnitPreservesLiteralEnvironmentPaths(t *testing.T) {
	m := serviceManager{home: "/fixture/home$with%spaces", executable: "/fixture/bin$native"}
	unit := m.unit()
	if !strings.Contains(unit, `Environment="HOME=/fixture/home$with%%spaces"`) {
		t.Fatalf("environment value changed: %s", unit)
	}
	if !strings.Contains(unit, `ExecStart="/fixture/bin$$native"`) {
		t.Fatalf("executable expanded environment: %s", unit)
	}
}
