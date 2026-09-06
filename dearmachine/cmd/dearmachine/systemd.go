package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dearmachine/dearmachine/internal/supervisor"
)

const conciergeUnit = "dearmachine-concierge.service"
const unitMarker = "# Managed by dearmachine concierge v1\n"

type supervisionConsent struct {
	Version     int  `json:"version"`
	UseSystemd  bool `json:"useSystemd"`
	Persistence bool `json:"enablePersistence"`
}
type serviceManager struct {
	home, executable, configDir string
	run                         func(string, ...string) (string, error)
}

func nativeServiceManager(home string) serviceManager {
	executable, _ := os.Executable()
	return serviceManager{home: home, executable: executable, configDir: os.Getenv("XDG_CONFIG_HOME"), run: func(name string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, name, args...).Output()
		return strings.TrimSpace(string(output)), err
	}}
}
func (m serviceManager) root() string        { return filepath.Join(m.home, ".dearmachine") }
func (m serviceManager) consentPath() string { return filepath.Join(m.root(), "supervision.json") }
func (m serviceManager) load() (supervisionConsent, error) {
	var consent supervisionConsent
	info, err := os.Lstat(m.consentPath())
	if os.IsNotExist(err) {
		return supervisionConsent{Version: 1}, nil
	}
	if err != nil {
		return consent, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return consent, errors.New("supervision consent must be a private regular file")
	}
	fd, err := syscall.Open(m.consentPath(), syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return consent, err
	}
	file := os.NewFile(uintptr(fd), m.consentPath())
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&consent); err != nil {
		return consent, err
	}
	if consent.Version != 1 || consent.Persistence && !consent.UseSystemd {
		return consent, errors.New("invalid supervision consent")
	}
	return consent, nil
}
func privateServiceDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0022 != 0 {
		return errors.New("unsafe service state directory")
	}
	return nil
}
func atomicServiceFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".concierge-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
func (m serviceManager) save(consent supervisionConsent) error {
	data, err := json.Marshal(consent)
	if err != nil {
		return err
	}
	return atomicServiceFile(m.consentPath(), append(data, '\n'))
}
func (m serviceManager) usable() bool {
	_, err := m.run("systemctl", "--user", "show-environment")
	return err == nil
}

// Capability is probed at runtime only after an explicit saved choice.
func selectSupervision(m serviceManager) (string, error) {
	consent, err := m.load()
	if err != nil {
		return "", err
	}
	if !consent.UseSystemd {
		return "supervisor-lite", nil
	}
	if !m.usable() {
		return "", errors.New("consented systemd user manager is unavailable; inspect dearmachine systemd status")
	}
	return "systemd", nil
}
func systemdQuote(value string) string {
	return strconv.Quote(strings.ReplaceAll(strings.ReplaceAll(value, "%", "%%"), "$", "$$"))
}
func (m serviceManager) unit() string {
	return unitMarker + "[Unit]\nDescription=Dear Machine native supervisor\n[Service]\nType=simple\nExecStart=" + systemdQuote(m.executable) + " _supervise --state-dir " + systemdQuote(m.root()) + " -- " + systemdQuote(m.executable) + " up --foreground\nEnvironment=" + systemdQuote("HOME="+m.home) + "\nRestart=no\nKillMode=control-group\nTimeoutStopSec=5\n[Install]\nWantedBy=default.target\n"
}
func (m serviceManager) unitPath() string {
	config := m.configDir
	if config == "" {
		config = filepath.Join(m.home, ".config")
	}
	return filepath.Join(config, "systemd", "user", conciergeUnit)
}
func (m serviceManager) configure(kind, choice string) error {
	if (kind != "systemd" && kind != "persistence") || (choice != "on" && choice != "off") {
		return errors.New("use dearmachine systemd on|off|status or dearmachine persistence on|off|status")
	}
	if err := privateServiceDir(m.root()); err != nil {
		return err
	}
	// Serialize saved choices and side effects, separately from the lifetime owner lock.
	fd, err := syscall.Open(filepath.Join(m.root(), "supervision-consent.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another supervision choice is being applied")
	}
	consent, err := m.load()
	if err != nil {
		return err
	}
	enabled := choice == "on"
	if kind == "persistence" && !consent.UseSystemd {
		return errors.New("first approve systemd separately with dearmachine systemd on; persistence on also authorizes loginctl enable-linger")
	}
	if kind == "systemd" {
		if !enabled && consent.Persistence {
			return errors.New("disable reboot persistence first with dearmachine persistence off")
		}
		if err := supervisor.CheckAvailable(m.root()); err != nil {
			return errors.New("resident supervisor owns this installation; stop it through its existing owner before changing supervision")
		}
		if !enabled {
			consent.UseSystemd = false
			return m.save(consent)
		}
	}
	if !m.usable() {
		return errors.New("systemd user manager is unavailable; no service or lingering change was applied")
	}
	if kind == "systemd" {
		path := m.unitPath()
		// Never overwrite an unrelated or substituted unit.
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() {
				return errors.New("service unit is not a regular file")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !strings.HasPrefix(string(data), unitMarker) {
				return errors.New("existing service unit is not managed by concierge")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		config := filepath.Dir(filepath.Dir(filepath.Dir(path)))
		for _, dir := range []string{config, filepath.Join(config, "systemd"), filepath.Dir(path)} {
			if err := privateServiceDir(dir); err != nil {
				return err
			}
		}
		if err := atomicServiceFile(path, []byte(m.unit())); err != nil {
			return err
		}
		if _, err := m.run("systemctl", "--user", "daemon-reload"); err != nil {
			return errors.New("service reload failed; consent was not saved")
		}
		consent.UseSystemd = true
		return m.save(consent)
	}
	// Save the explicit desired choice before effects: failures remain visible and retryable.
	consent.Persistence = enabled
	if err := m.save(consent); err != nil {
		return err
	}
	if enabled {
		if _, err := m.run("loginctl", "enable-linger"); err != nil {
			return errors.New("lingering setup failed; persistence is unconfirmed; inspect dearmachine persistence status")
		}
		if _, err := m.run("systemctl", "--user", "enable", conciergeUnit); err != nil {
			return errors.New("service enable failed; lingering may be enabled; inspect dearmachine persistence status")
		}
	} else {
		if _, err := m.run("systemctl", "--user", "disable", conciergeUnit); err != nil {
			return errors.New("service disable failed; persistence is unconfirmed")
		}
	}
	return nil
}
func (m serviceManager) persistence() string {
	consent, err := m.load()
	if err != nil || !consent.UseSystemd {
		return "unknown"
	}
	enabled, err := m.run("systemctl", "--user", "show", conciergeUnit, "--property=UnitFileState", "--value")
	if err != nil {
		return "unknown"
	}
	if enabled == "disabled" {
		return "disabled"
	}
	if enabled != "enabled" {
		return "unknown"
	}
	linger, err := m.run("loginctl", "show-user", "--property=Linger", "--value")
	if err != nil || linger != "yes" {
		return "unknown"
	}
	return "enabled"
}
func runSupervisionChoice(kind string, args []string, deps dependencies) error {
	if len(args) != 1 {
		return errors.New("systemd on approves service use only; persistence on separately approves reboot startup AND loginctl enable-linger; use on|off|status")
	}
	home, err := deps.userHomeDir()
	if err != nil {
		return err
	}
	m := nativeServiceManager(home)
	if args[0] == "status" {
		consent, err := m.load()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(outputOrDiscard(deps.stdout), "Systemd user manager available: %t. Saved systemd consent: %t. Saved reboot/linger consent: %t. Observed persistence: %s.\nInspect: systemctl --user status %s; systemctl --user is-enabled %s; loginctl show-user --property=Linger\nDisable: dearmachine persistence off (retains account-wide lingering); loginctl disable-linger removes account-wide lingering if no other services need it.\n", m.usable(), consent.UseSystemd, consent.Persistence, m.persistence(), conciergeUnit, conciergeUnit)
		return err
	}
	if err := m.configure(kind, args[0]); err != nil {
		return err
	}
	_, err = fmt.Fprintln(outputOrDiscard(deps.stdout), "Saved explicit supervision choice. Use dearmachine up to start; dearmachine persistence status to inspect observed persistence. Disabling persistence retains account-wide lingering.")
	return err
}
func startSelected(executable string, args []string, root string) (int, error) {
	m := nativeServiceManager(filepath.Dir(root))
	owner, err := selectSupervision(m)
	if err != nil {
		return 0, err
	}
	if owner == "supervisor-lite" {
		return startSupervised(executable, args, root)
	}
	if strings.Join(args, "\x00") != "up\x00--foreground" {
		return 0, errors.New("systemd service uses saved native configuration; explicit launch flags require supervisor-lite")
	}
	// An existing socket still owns mutations. Never migrate or stop it implicitly.
	if s, err := supervisor.Request(root, "status", time.Second); err == nil {
		if err := checkOtherOwner(root, s); err != nil {
			return 0, err
		}
		mainPID, queryErr := m.run("systemctl", "--user", "show", conciergeUnit, "--property=MainPID", "--value")
		if queryErr != nil || mainPID != strconv.Itoa(s.SupervisorPID) {
			return 0, errors.New("resident supervisor is not the consented systemd owner; inspect existing supervision before switching")
		}
		s, err = supervisor.Request(root, "up", 5*time.Second)
		return s.DaemonPID, err
	}
	if err := supervisor.CheckAvailable(root); err != nil {
		return 0, err
	}
	if _, err := m.run("systemctl", "--user", "start", conciergeUnit); err != nil {
		return 0, errors.New("systemd start unconfirmed; inspect dearmachine status")
	}
	deadline := time.Now().Add(daemonStartupTimeout)
	for time.Now().Before(deadline) {
		if s, err := supervisor.Request(root, "status", 100*time.Millisecond); err == nil && s.Daemon == "running" {
			return s.DaemonPID, nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return 0, errors.New("systemd startup unconfirmed; inspect dearmachine status")
}
