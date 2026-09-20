package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dearmachine/dearmachine/internal/hostos"
	"golang.org/x/sys/windows/registry"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const conciergeUnit = "DearMachine"
const startupKey = `Software\Microsoft\Windows\CurrentVersion\Run`

type supervisionConsent struct {
	Version     int  `json:"version"`
	UseLaunchd  bool `json:"useLaunchd,omitempty"`
	UseSystemd  bool `json:"useSystemd"`
	Persistence bool `json:"enablePersistence"`
}
type serviceManager struct {
	home, executable, configDir string
	platform                    string
	run                         func(string, ...string) (string, error)
}

func nativeServiceManager(home string) serviceManager {
	executable, _ := os.Executable()
	return serviceManager{home: home, executable: executable, platform: "windows", run: func(name string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		b, e := exec.CommandContext(ctx, name, args...).Output()
		return strings.TrimSpace(string(b)), e
	}}
}
func (m serviceManager) root() string        { return filepath.Join(m.home, ".dearmachine") }
func (m serviceManager) consentPath() string { return filepath.Join(m.root(), "supervision.json") }
func (m serviceManager) load() (supervisionConsent, error) {
	c := supervisionConsent{Version: 1}
	p := m.consentPath()
	info, e := os.Lstat(p)
	if os.IsNotExist(e) {
		return c, nil
	}
	if e != nil {
		return c, e
	}
	if !info.Mode().IsRegular() || !hostos.Private(p, info, 0077) {
		return c, errors.New("supervision consent must be a private regular file")
	}
	f, e := hostos.Open(p, os.O_RDONLY, 0)
	if e != nil {
		return c, e
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return c, e
	}
	if c.Version != 1 || c.UseSystemd || c.UseLaunchd {
		return c, errors.New("invalid Windows supervision consent")
	}
	return c, nil
}
func privateServiceDir(path string) error {
	_, e := os.Lstat(path)
	missing := os.IsNotExist(e)
	if e = os.MkdirAll(path, 0700); e != nil {
		return e
	}
	if missing {
		if e = hostos.Protect(path, 0700); e != nil {
			return e
		}
	}
	i, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if !i.IsDir() || !hostos.Private(path, i, 0077) {
		return errors.New("unsafe service state directory")
	}
	return nil
}
func atomicServiceFile(path string, data []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".concierge-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = hostos.Protect(f.Name(), 0600); e != nil {
		f.Close()
		return e
	}
	if _, e = f.Write(data); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}
func (m serviceManager) save(c supervisionConsent) error {
	b, e := json.Marshal(c)
	if e != nil {
		return e
	}
	return atomicServiceFile(m.consentPath(), append(b, '\n'))
}
func (m serviceManager) usable() bool { return true }
func selectSupervision(m serviceManager) (string, error) {
	_, e := m.load()
	return "supervisor-lite", e
}
func (m serviceManager) startupCommand() string {
	executable := m.executable
	if launcher := os.Getenv("DEARMACHINE_LAUNCHER"); filepath.IsAbs(launcher) {
		executable = launcher
	}
	return `"` + executable + `" up`
}
func (m serviceManager) configure(kind, choice string) error {
	if kind != "persistence" {
		return errors.New("systemd is unavailable on Windows; use dearmachine persistence on|off|status")
	}
	if choice != "on" && choice != "off" {
		return errors.New("use dearmachine persistence on|off|status")
	}
	if e := privateServiceDir(m.root()); e != nil {
		return e
	}
	f, e := hostos.Open(filepath.Join(m.root(), "supervision-consent.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = hostos.Flock(int(f.Fd()), hostos.LOCK_EX|hostos.LOCK_NB); e != nil {
		return errors.New("another supervision choice is being applied")
	}
	c, e := m.load()
	if e != nil {
		return e
	}
	k, _, e := registry.CreateKey(registry.CURRENT_USER, startupKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if e != nil {
		return e
	}
	defer k.Close()
	existing, _, e := k.GetStringValue(conciergeUnit)
	if e != nil && e != registry.ErrNotExist {
		return e
	}
	if e == nil && existing != m.startupCommand() {
		return errors.New("existing DearMachine startup entry belongs to another installation")
	}
	c.Persistence = choice == "on"
	if e = m.save(c); e != nil {
		return e
	}
	if c.Persistence {
		return k.SetStringValue(conciergeUnit, m.startupCommand())
	}
	e = k.DeleteValue(conciergeUnit)
	if e == registry.ErrNotExist {
		return nil
	}
	return e
}
func (m serviceManager) observeStartup() startupObservation {
	o := startupObservation{login: "cannot verify", reboot: "not configured", linger: "not applicable", unitState: "not verified", reason: "Windows startup requires signing in; Windows Startup Apps settings can disable the configured entry"}
	k, e := registry.OpenKey(registry.CURRENT_USER, startupKey, registry.QUERY_VALUE)
	if e == registry.ErrNotExist {
		o.login = "not configured"
		o.unitState = "absent"
		return o
	}
	if e != nil {
		o.reason = "cannot read current-user startup configuration"
		return o
	}
	defer k.Close()
	value, _, e := k.GetStringValue(conciergeUnit)
	if e == registry.ErrNotExist {
		o.login = "not configured"
		o.unitState = "absent"
		return o
	}
	if e != nil {
		o.reason = "cannot read Dear Machine startup entry"
		return o
	}
	if value != m.startupCommand() {
		o.reason = "startup entry belongs to another installation"
		return o
	}
	o.login = "enabled"
	o.unitState = "enabled"
	return o
}
func (m serviceManager) persistence() string {
	o := m.observeStartup()
	if o.login == "enabled" {
		return "enabled"
	}
	if o.login == "not configured" {
		return "disabled"
	}
	return "unknown"
}
func runSupervisionChoice(kind string, args []string, deps dependencies) error {
	if len(args) != 1 {
		return errors.New("use dearmachine persistence on|off|status")
	}
	home, e := deps.userHomeDir()
	if e != nil {
		return e
	}
	m := nativeServiceManager(home)
	if args[0] == "status" {
		if e = writeStartupStatus(outputOrDiscard(deps.stdout), m.observeStartup()); e != nil {
			return e
		}
		return m.writeConsentDetails(outputOrDiscard(deps.stdout))
	}
	if e = m.configure(kind, args[0]); e != nil {
		return e
	}
	_, e = fmt.Fprintln(outputOrDiscard(deps.stdout), "Saved Windows startup choice. Persistence starts Dear Machine after you sign in; use dearmachine up to start now.")
	return e
}
func startSelected(executable string, args []string, root string) (int, error) {
	m := nativeServiceManager(filepath.Dir(root))
	m.executable = executable
	return startWithServiceManager(m, args, root)
}
func startWithServiceManager(m serviceManager, args []string, root string) (int, error) {
	if _, e := selectSupervision(m); e != nil {
		return 0, e
	}
	return startSupervised(m.executable, args, root)
}
