package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestSystemdUnitOwnsSupervisorWithoutSecondRestartLoop(t *testing.T) {
	manager := serviceManager{home: filepath.Join(t.TempDir(), "home with spaces%"), executable: "/test/bin/dearmachine"}
	unit := manager.unit()
	for _, want := range []string{"_supervise", "up", "--foreground", "Restart=no", "KillMode=control-group", "%%"} {
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
