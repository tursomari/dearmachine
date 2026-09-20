//go:build !windows

package main

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type launchdTestExit int

func (e launchdTestExit) Error() string { return "launchctl failed" }
func (e launchdTestExit) ExitCode() int { return int(e) }

func launchdFixture(t *testing.T) (serviceManager, *[]string) {
	t.Helper()
	home := socketTestHome(t)
	binary := filepath.Join(home, "bin with spaces & quotes", "dearmachine")
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	m := serviceManager{platform: "darwin", home: home, executable: binary, configDir: filepath.Join(home, "private config")}
	m.run = func(name string, args ...string) (string, error) {
		if name != "/bin/launchctl" {
			t.Fatalf("unexpected manager: %s", name)
		}
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "print" && len(args) > 1 && args[1] == m.launchdTarget() {
			return "", launchdTestExit(113)
		}
		return "disabled services = {\n}\n", nil
	}
	return m, &calls
}
func TestLaunchdSeparateConsentAndLoginStartup(t *testing.T) {
	m, calls := launchdFixture(t)
	if owner, err := selectSupervision(m); err != nil || owner != "supervisor-lite" || len(*calls) != 0 {
		t.Fatalf("implicit service: %s %v %v", owner, err, *calls)
	}
	if err := m.configure("persistence", "on"); err == nil || len(*calls) != 0 {
		t.Fatal("persistence bypassed service choice")
	}
	if err := m.configure("launchd", "on"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.launchdPath(true)); !os.IsNotExist(err) {
		t.Fatal("service choice enabled login startup")
	}
	data, err := m.readLaunchdPlist(false)
	if err != nil {
		t.Fatal(err)
	}
	var document struct{ XMLName xml.Name }
	if err = xml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.XMLName.Local != "plist" {
		t.Fatal("invalid plist")
	}
	for _, part := range []string{"_supervise", "--foreground", "&amp;", "<key>RunAtLoad</key><true/>", "<key>SuccessfulExit</key><false/>", "<key>Umask</key><integer>63</integer>"} {
		if !strings.Contains(string(data), part) {
			t.Fatalf("missing %s", part)
		}
	}
	if strings.Contains(strings.Join(*calls, "\n"), "bootstrap") {
		t.Fatal("service choice started client")
	}
	if err = m.configure("persistence", "on"); err != nil {
		t.Fatal(err)
	}
	if o := m.observeStartup(); o.login != "enabled" || o.reboot != "not configured" {
		t.Fatalf("startup: %+v", o)
	}
	if err = m.configure("launchd", "off"); err == nil {
		t.Fatal("left automatic startup behind")
	}
	*calls = nil
	if err = m.configure("persistence", "off"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(*calls, "\n"), "bootout") {
		t.Fatal("disabling login startup stopped current service")
	}
	if err = m.configure("launchd", "off"); err != nil {
		t.Fatal(err)
	}
	c, err := m.load()
	if err != nil || c.UseLaunchd || c.Persistence {
		t.Fatalf("consent: %+v %v", c, err)
	}
}
func TestLaunchdDefinitionExcludesSecretsAndUsesStableLauncher(t *testing.T) {
	m, _ := launchdFixture(t)
	t.Setenv("PROVIDER_API_KEY", "fixture-only-secret")
	t.Setenv("PATH", "/fixture/ambient/bin")
	stable := filepath.Join(m.home, ".local/bin/dearmachine")
	os.MkdirAll(filepath.Dir(stable), 0700)
	os.WriteFile(stable, []byte("#!/bin/sh\n"), 0700)
	data, err := m.launchdPlist()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), xmlString(stable)) || strings.Contains(string(data), xmlString(m.executable)) {
		t.Fatal("did not use stable launcher")
	}
	for _, bad := range []string{"fixture-only-secret", "PROVIDER_API_KEY", "/fixture/ambient/bin"} {
		if strings.Contains(string(data), bad) {
			t.Fatalf("inherited %s", bad)
		}
	}
	other := m
	other.home = t.TempDir()
	if other.launchdLabel() == m.launchdLabel() {
		t.Fatal("private homes share a service label")
	}
}
func TestLaunchdRefusesUnrelatedAndUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"unmanaged", "symlink", "public", "parent-symlink"} {
		t.Run(kind, func(t *testing.T) {
			m, _ := launchdFixture(t)
			if err := m.configure("launchd", "on"); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "unrelated.plist")
			os.WriteFile(target, []byte("keep me"), 0600)
			path := m.launchdPath(true)
			os.MkdirAll(filepath.Dir(path), 0700)
			switch kind {
			case "unmanaged":
				os.WriteFile(path, []byte("keep me"), 0600)
			case "symlink":
				os.Symlink(target, path)
			case "public":
				data, _ := m.readLaunchdPlist(false)
				os.WriteFile(path, data, 0644)
			case "parent-symlink":
				os.Remove(filepath.Dir(path))
				os.Symlink(filepath.Dir(target), filepath.Dir(path))
			}
			if err := m.configure("persistence", "on"); err == nil {
				t.Fatal("accepted unsafe login path")
			}
			if b, _ := os.ReadFile(target); string(b) != "keep me" {
				t.Fatal("changed unrelated file")
			}
		})
	}
}
func TestLaunchdObservedStateIndependentOfConsent(t *testing.T) {
	for _, scenario := range []string{"enabled", "disabled", "unavailable", "inconclusive", "changed"} {
		t.Run(scenario, func(t *testing.T) {
			m, _ := launchdFixture(t)
			if err := m.configure("launchd", "on"); err != nil {
				t.Fatal(err)
			}
			if err := m.configure("persistence", "on"); err != nil {
				t.Fatal(err)
			}
			os.Remove(m.consentPath())
			m.run = func(_ string, _ ...string) (string, error) {
				switch scenario {
				case "disabled":
					return "disabled services = {\n\"" + m.launchdLabel() + "\" => true\n}\n", nil
				case "unavailable":
					return "", errors.New("unavailable")
				case "inconclusive":
					return "", nil
				default:
					return "disabled services = {\n}\n", nil
				}
			}
			if scenario == "changed" {
				os.WriteFile(m.launchdPath(true), []byte(m.launchdMarker()+"changed"), 0600)
			}
			got := m.observeStartup()
			want := "cannot verify"
			if scenario == "enabled" {
				want = "enabled"
			}
			if scenario == "disabled" {
				want = "not configured"
			}
			if got.login != want || got.reboot != "not configured" {
				t.Fatalf("observation: %+v", got)
			}
			if _, err := os.Stat(m.consentPath()); !os.IsNotExist(err) {
				t.Fatal("observation recreated consent")
			}
		})
	}
}
func TestLaunchdFailuresDoNotGrantConsent(t *testing.T) {
	m, _ := launchdFixture(t)
	m.run = func(string, ...string) (string, error) { return "", errors.New("no GUI session") }
	if err := m.configure("launchd", "on"); err == nil {
		t.Fatal("accepted unavailable GUI")
	}
	c, err := m.load()
	if err != nil || c.UseLaunchd || c.Persistence {
		t.Fatalf("granted consent: %+v %v", c, err)
	}
	if err = m.configure("systemd", "on"); err == nil {
		t.Fatal("accepted systemd on macOS")
	}
	m.platform = "linux"
	if err = m.configure("launchd", "on"); err == nil {
		t.Fatal("accepted launchd on Linux")
	}
}

func TestLaunchdRejectsUnknownAndForeignJobIdentity(t *testing.T) {
	for _, scenario := range []string{"unknown", "foreign"} {
		t.Run(scenario, func(t *testing.T) {
			m, calls := launchdFixture(t)
			original := m.run
			m.run = func(name string, args ...string) (string, error) {
				if len(args) == 2 && args[0] == "print" && args[1] == m.launchdTarget() {
					if scenario == "unknown" {
						return "", launchdTestExit(1)
					}
					return "path = /fixture/unrelated.plist\npid = 123\n", nil
				}
				return original(name, args...)
			}
			if err := m.configure("launchd", "on"); err == nil {
				t.Fatal("accepted unknown or foreign job")
			}
			c, err := m.load()
			if err != nil || c.UseLaunchd {
				t.Fatal("saved consent after identity failure")
			}
			for _, call := range *calls {
				if strings.HasPrefix(call, "bootout") || strings.HasPrefix(call, "enable") || strings.HasPrefix(call, "bootstrap") {
					t.Fatal("mutated another job")
				}
			}
		})
	}
}
func TestLaunchdRefusesMalformedOwnedDefinition(t *testing.T) {
	for _, replacement := range []string{"<key>RunAtLoad</key><false/>", "<key>RunAtLoad</key><string>true</string>"} {
		m, _ := launchdFixture(t)
		if err := m.configure("launchd", "on"); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(m.launchdPath(false))
		data = []byte(strings.Replace(string(data), "<key>RunAtLoad</key><true/>", replacement, 1))
		os.WriteFile(m.launchdPath(false), data, 0600)
		if err := m.configure("persistence", "on"); err == nil {
			t.Fatal("accepted modified startup contract")
		}
	}
}
func TestLaunchdChoicesCannotRaceStartup(t *testing.T) {
	m, _ := launchdFixture(t)
	first, err := m.launchdChoiceLock(true)
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := m.launchdChoiceLock(true)
	if err != nil {
		t.Fatal("concurrent starts cannot join:", err)
	}
	defer second()
	if err := m.configure("launchd", "on"); err == nil {
		t.Fatal("configuration raced startup")
	}
}
func TestLaunchdReadsNativeDisabledValues(t *testing.T) {
	for _, value := range []string{"true", "false", "enabled", "disabled", "unexpected"} {
		t.Run(value, func(t *testing.T) {
			m, _ := launchdFixture(t)
			m.run = func(string, ...string) (string, error) {
				return "disabled services = {\n\"" + m.launchdLabel() + "\" => " + value + "\n}\n", nil
			}
			disabled, err := m.launchdDisabled()
			if value == "unexpected" {
				if err == nil {
					t.Fatal("accepted unknown override")
				}
				return
			}
			if err != nil || disabled != (value == "true" || value == "disabled") {
				t.Fatalf("override: %t %v", disabled, err)
			}
		})
	}
}
func TestLaunchdOffDoesNotRelyOnlyOnSavedConsent(t *testing.T) {
	m, _ := launchdFixture(t)
	if err := m.configure("launchd", "on"); err != nil {
		t.Fatal(err)
	}
	if err := m.configure("persistence", "on"); err != nil {
		t.Fatal(err)
	}
	// Simulate a failed consent write after the real login plist was installed.
	if err := m.save(supervisionConsent{Version: 1, UseLaunchd: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.configure("launchd", "off"); err == nil {
		t.Fatal("removed ownership while login agent remained configured")
	}
}

func TestLaunchdConsentCannotSelectSystemd(t *testing.T) {
	m, _ := launchdFixture(t)
	if err := m.configure("launchd", "on"); err != nil {
		t.Fatal(err)
	}
	m.platform = "linux"
	m.run = func(string, ...string) (string, error) {
		t.Fatal("probed or mutated Linux manager with macOS consent")
		return "", nil
	}
	if _, err := selectSupervision(m); err == nil {
		t.Fatal("accepted foreign service consent")
	}
	if err := m.configure("systemd", "on"); err == nil {
		t.Fatal("overwrote foreign service consent")
	}
}
