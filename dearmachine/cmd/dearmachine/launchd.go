//go:build !windows

package main

// LaunchAgents belong to a logged-in user. They are deliberately not system
// LaunchDaemons: no root, password, or before-login startup is requested.
import (
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dearmachine/dearmachine/internal/supervisor"
)

func (m serviceManager) launchdLabel() string {
	sum := sha256.Sum256([]byte(m.root()))
	return fmt.Sprintf("org.dearmachine.concierge.%x", sum[:8])
}
func (m serviceManager) launchdDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }
func (m serviceManager) launchdTarget() string { return m.launchdDomain() + "/" + m.launchdLabel() }
func (m serviceManager) launchdPath(login bool) string {
	if login {
		return filepath.Join(m.home, "Library", "LaunchAgents", m.launchdLabel()+".plist")
	}
	return filepath.Join(m.root(), "launchd", m.launchdLabel()+".plist")
}
func (m serviceManager) launchdMarker() string {
	return "<!-- Managed by dearmachine concierge v1: " + m.launchdLabel() + " -->\n"
}
func xmlString(value string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(value))
	return "<string>" + b.String() + "</string>\n"
}
func (m serviceManager) launchdExecutable() (string, error) {
	// Stable managed launchers keep both Standard and Nix environments intact,
	// and follow the currently activated release instead of pinning an old one.
	candidates := []string{filepath.Join(m.home, ".local", "bin", "dearmachine"), filepath.Join(m.home, ".nix-profile", "bin", "dearmachine")}
	if strings.HasPrefix(filepath.Base(m.executable), ".dearmachine-wrapped") {
		candidates = append(candidates, filepath.Join(filepath.Dir(m.executable), "dearmachine"))
	}
	candidates = append(candidates, m.executable)
	for _, path := range candidates {
		if !filepath.IsAbs(path) {
			continue
		}
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return path, nil
		}
	}
	return "", errors.New("no installed Dear Machine executable is available for launchd")
}
func (m serviceManager) launchdPlist() ([]byte, error) {
	executable, err := m.launchdExecutable()
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" + m.launchdMarker() + "<plist version=\"1.0\"><dict>\n<key>Label</key>\n" + xmlString(m.launchdLabel()))
	b.WriteString("<key>ProgramArguments</key><array>\n")
	for _, arg := range []string{executable, "_supervise", "--state-dir", m.root(), "--", executable, "up", "--foreground"} {
		b.WriteString(xmlString(arg))
	}
	b.WriteString("</array>\n<key>WorkingDirectory</key>" + xmlString(m.home))
	b.WriteString("<key>EnvironmentVariables</key><dict>\n<key>HOME</key>" + xmlString(m.home))
	b.WriteString("<key>PATH</key>" + xmlString(strings.Join([]string{filepath.Join(m.home, ".local/bin"), filepath.Join(m.home, ".nix-profile/bin"), "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":")))
	if m.configDir != "" {
		b.WriteString("<key>XDG_CONFIG_HOME</key>" + xmlString(m.configDir))
	}
	// Never capture the caller's environment or provider credentials in a plist.
	b.WriteString("</dict>\n<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>\n<key>ThrottleInterval</key><integer>10</integer>\n<key>ExitTimeOut</key><integer>10</integer>\n<key>Umask</key><integer>63</integer>\n</dict></plist>\n")
	return []byte(b.String()), nil
}

type launchdXML struct {
	XMLName  xml.Name
	Text     string       `xml:",chardata"`
	Children []launchdXML `xml:",any"`
}

func launchdDictionary(node launchdXML) (map[string]launchdXML, error) {
	if node.XMLName.Local != "dict" || len(node.Children)%2 != 0 {
		return nil, errors.New("invalid launchd dictionary")
	}
	result := map[string]launchdXML{}
	for i := 0; i < len(node.Children); i += 2 {
		key := node.Children[i]
		if key.XMLName.Local != "key" {
			return nil, errors.New("invalid launchd dictionary key")
		}
		if _, ok := result[key.Text]; ok {
			return nil, errors.New("duplicate launchd dictionary key")
		}
		result[key.Text] = node.Children[i+1]
	}
	return result, nil
}
func (m serviceManager) validateLaunchdPlist(data []byte) error {
	invalid := errors.New("launchd definition differs from the managed service contract")
	var plist launchdXML
	if xml.Unmarshal(data, &plist) != nil || plist.XMLName.Local != "plist" || len(plist.Children) != 1 {
		return invalid
	}
	values, err := launchdDictionary(plist.Children[0])
	if err != nil {
		return invalid
	}
	if values["Label"].XMLName.Local != "string" || values["WorkingDirectory"].XMLName.Local != "string" || values["Label"].Text != m.launchdLabel() || values["RunAtLoad"].XMLName.Local != "true" || values["WorkingDirectory"].Text != m.home {
		return invalid
	}
	args := values["ProgramArguments"]
	if args.XMLName.Local != "array" || len(args.Children) != 8 {
		return invalid
	}
	executable := args.Children[0].Text
	expected := []string{executable, "_supervise", "--state-dir", m.root(), "--", executable, "up", "--foreground"}
	if !filepath.IsAbs(executable) {
		return invalid
	}
	for i, arg := range args.Children {
		if arg.XMLName.Local != "string" || arg.Text != expected[i] {
			return invalid
		}
	}
	environment, err := launchdDictionary(values["EnvironmentVariables"])
	if err != nil || environment["HOME"].Text != m.home {
		return invalid
	}
	for key, value := range environment {
		if (key != "HOME" && key != "PATH" && key != "XDG_CONFIG_HOME") || value.XMLName.Local != "string" {
			return invalid
		}
	}
	keep, err := launchdDictionary(values["KeepAlive"])
	if err != nil || len(keep) != 1 || keep["SuccessfulExit"].XMLName.Local != "false" {
		return invalid
	}
	for _, key := range []string{"ThrottleInterval", "ExitTimeOut", "Umask"} {
		if values[key].XMLName.Local != "integer" {
			return invalid
		}
	}
	if len(values) != 9 || values["ThrottleInterval"].Text != "10" || values["ExitTimeOut"].Text != "10" || values["Umask"].Text != "63" {
		return invalid
	}
	return nil
}

func (m serviceManager) readLaunchdPlist(login bool) ([]byte, error) {
	path := m.launchdPath(login)
	dirs := []string{m.home, m.root(), filepath.Dir(m.launchdPath(false))}
	if login {
		dirs = []string{m.home, filepath.Join(m.home, "Library"), filepath.Dir(path)}
	}
	for _, dir := range dirs {
		info, err := os.Lstat(dir)
		if err != nil {
			return nil, err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || !ok || st.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0022 != 0 {
			return nil, errors.New("unsafe launchd definition directory")
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || st.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("launchd definition must be an owned private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(data, []byte(m.launchdMarker())) {
		return nil, errors.New("existing launchd definition is not managed by this installation")
	}
	if err := m.validateLaunchdPlist(data); err != nil {
		return nil, err
	}
	return data, nil
}
func (m serviceManager) writeLaunchdPlist(login bool, data []byte) error {
	if _, err := m.readLaunchdPlist(login); err != nil && !os.IsNotExist(err) {
		return err
	}
	dirs := []string{m.root(), filepath.Dir(m.launchdPath(false))}
	if login {
		dirs = []string{filepath.Join(m.home, "Library"), filepath.Join(m.home, "Library", "LaunchAgents")}
	}
	for _, dir := range dirs {
		if err := privateServiceDir(dir); err != nil {
			return err
		}
	}
	return atomicServiceFile(m.launchdPath(login), data)
}
func (m serviceManager) launchctl(args ...string) (string, error) {
	return m.run("/bin/launchctl", args...)
}
func (m serviceManager) selectLaunchd() (string, error) {
	c, err := m.load()
	if err != nil {
		return "", err
	}
	if c.UseSystemd {
		return "", errors.New("saved systemd consent cannot select a macOS service")
	}
	if !c.UseLaunchd {
		return "supervisor-lite", nil
	}
	if _, err := m.launchctl("print", m.launchdDomain()); err != nil {
		return "", errors.New("macOS graphical login session is unavailable; inspect dearmachine launchd status")
	}
	return "launchd", nil
}

// print returns 113 only for an absent service. Other failures are unknown,
// never permission to enable, replace, or remove a possibly unrelated job.
func (m serviceManager) inspectLaunchdJob() (string, bool, error) {
	output, err := m.launchctl("print", m.launchdTarget())
	if err != nil {
		var code interface{ ExitCode() int }
		if errors.As(err, &code) && code.ExitCode() == 113 {
			return "", false, nil
		}
		return "", false, errors.New("launchd service identity could not be inspected")
	}
	path := regexp.MustCompile(`(?m)^\s*path = (.+)$`).FindStringSubmatch(output)
	if len(path) != 2 {
		return "", false, errors.New("launchd did not report the service definition path")
	}
	actual := strings.TrimSpace(path[1])
	if actual != m.launchdPath(false) && actual != m.launchdPath(true) {
		return "", false, errors.New("launchd service label belongs to a different definition")
	}
	return output, true, nil
}

var launchdPID = regexp.MustCompile(`(?m)^\s*pid = ([0-9]+)\s*$`)

func (m serviceManager) launchdOwner(pid int) bool {
	output, exists, err := m.inspectLaunchdJob()
	if err != nil || !exists {
		return false
	}
	found := launchdPID.FindStringSubmatch(output)
	return len(found) == 2 && found[1] == strconv.Itoa(pid)
}
func (m serviceManager) stopIdleOwner() error {
	s, err := supervisor.Request(m.root(), "status", time.Second)
	if err != nil {
		return supervisor.CheckAvailable(m.root())
	}
	if err := checkOtherOwner(m.root(), s); err != nil {
		return err
	}
	if s.Supervisor != "stopped" || s.Daemon != "stopped" {
		return errors.New("stop Dear Machine with dearmachine down before changing service ownership")
	}
	// An explicit service choice may retire an idle native supervisor. It must
	// never migrate a running daemon, even when another launchd job owns it.
	_, exists, inspectErr := m.inspectLaunchdJob()
	if inspectErr != nil {
		return inspectErr
	}
	if exists && !m.launchdOwner(s.SupervisorPID) {
		return errors.New("resident supervisor is not the inspected launchd owner")
	}
	if exists {
		if _, err := m.launchctl("bootout", m.launchdTarget()); err != nil {
			return errors.New("launchd shutdown is unconfirmed")
		}
	} else {
		if _, err := supervisor.Request(m.root(), "shutdown", 5*time.Second); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := supervisor.CheckAvailable(m.root()); err == nil {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errors.New("previous supervisor did not release ownership")
}

// Starts may join one another, but cannot race a service ownership change.
func (m serviceManager) launchdChoiceLock(shared bool) (func(), error) {
	if err := privateServiceDir(m.root()); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(filepath.Join(m.root(), "supervision-consent.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	mode := syscall.LOCK_EX
	if shared {
		mode = syscall.LOCK_SH
	}
	if err = syscall.Flock(fd, mode|syscall.LOCK_NB); err != nil {
		syscall.Close(fd)
		return nil, errors.New("another supervision choice or startup is in progress")
	}
	return func() { syscall.Close(fd) }, nil
}

func (m serviceManager) configureLaunchd(kind, choice string) error {
	if (kind != "launchd" && kind != "persistence") || (choice != "on" && choice != "off") {
		return errors.New("on macOS use dearmachine launchd on|off|status or dearmachine persistence on|off|status")
	}
	if !filepath.IsAbs(m.home) {
		return errors.New("launchd requires an absolute HOME")
	}
	unlock, err := m.launchdChoiceLock(false)
	if err != nil {
		return err
	}
	defer unlock()
	c, err := m.load()
	if err != nil {
		return err
	}
	if c.UseSystemd {
		return errors.New("saved systemd consent cannot be used on macOS")
	}
	enabled := choice == "on"
	if kind == "persistence" && !c.UseLaunchd {
		return errors.New("first approve service use with dearmachine launchd on; persistence separately enables startup at login")
	}
	if kind == "launchd" && !enabled && c.Persistence {
		return errors.New("disable login startup first with dearmachine persistence off")
	}
	if _, err = m.launchctl("print", m.launchdDomain()); err != nil {
		return errors.New("macOS graphical login session is unavailable; no service setting was changed")
	}
	if kind == "persistence" {
		if enabled {
			if _, _, err := m.inspectLaunchdJob(); err != nil {
				return err
			}
			data, err := m.readLaunchdPlist(false)
			if err != nil {
				return err
			}
			if _, err := m.readLaunchdPlist(true); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err = m.enableLaunchd(); err != nil {
				return errors.New("launchd enable failed; startup at login is unconfirmed")
			}
			if err = m.writeLaunchdPlist(true, data); err != nil {
				return err
			}
		} else {
			if _, err = m.readLaunchdPlist(true); err == nil {
				if err = os.Remove(m.launchdPath(true)); err != nil {
					return err
				}
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		c.Persistence = enabled
		return m.save(c)
	}
	// Validate files before changing any owner. Never replace another plist.
	for _, login := range []bool{false, true} {
		if _, err = m.readLaunchdPlist(login); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if kind == "launchd" && !enabled {
		if _, err := m.readLaunchdPlist(true); err == nil {
			return errors.New("disable observed login startup first with dearmachine persistence off")
		}
	}
	if _, _, err := m.inspectLaunchdJob(); err != nil {
		return err
	}
	if enabled && c.UseLaunchd {
		_, err := m.readLaunchdPlist(false)
		return err
	}
	if err = m.stopIdleOwner(); err != nil {
		return err
	}
	if enabled {
		data, err := m.launchdPlist()
		if err != nil {
			return err
		}
		if err = m.writeLaunchdPlist(false, data); err != nil {
			return err
		}
	} else {
		// A job can be loaded but have no live supervisor (for example after failure).
		_, exists, inspectErr := m.inspectLaunchdJob()
		if inspectErr != nil {
			return inspectErr
		}
		if exists {
			if _, err = m.readLaunchdPlist(false); err != nil {
				return err
			}
			if _, err = m.launchctl("bootout", m.launchdTarget()); err != nil {
				return errors.New("launchd removal is unconfirmed")
			}
		}
		if _, err = m.readLaunchdPlist(false); err == nil {
			if err = os.Remove(m.launchdPath(false)); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	c.UseLaunchd = enabled
	return m.save(c)
}
func (m serviceManager) launchdDisabled() (bool, error) {
	output, err := m.launchctl("print-disabled", m.launchdDomain())
	if err != nil {
		return false, err
	}
	if !strings.Contains(output, "disabled services = {") || !strings.HasSuffix(strings.TrimSpace(output), "}") {
		return false, errors.New("inconclusive launchd enablement response")
	}
	pattern := regexp.MustCompile(`(?m)^\s*"` + regexp.QuoteMeta(m.launchdLabel()) + `"\s*=>\s*(true|false|enabled|disabled)\s*$`)
	match := pattern.FindStringSubmatch(output)
	if len(match) == 2 {
		return match[1] == "true" || match[1] == "disabled", nil
	}
	if strings.Contains(output, "\""+m.launchdLabel()+"\"") {
		return false, errors.New("unrecognized launchd enablement value")
	}
	return false, nil // No override: launchd's normal enabled default.
}
func (m serviceManager) enableLaunchd() error {
	disabled, err := m.launchdDisabled()
	if err != nil {
		return err
	}
	if disabled {
		_, err = m.launchctl("enable", m.launchdTarget())
	}
	return err
}

func (m serviceManager) observeLaunchd() startupObservation {
	o := startupObservation{login: "cannot verify", reboot: "not configured", unitState: "not verified", linger: "not applicable", reason: "LaunchAgents run only while logged in; no before-login service is configured"}
	data, err := m.readLaunchdPlist(true)
	if os.IsNotExist(err) {
		o.login = "not configured"
		o.unitState = "absent"
		return o
	}
	if err != nil {
		o.reason = "managed login agent could not be verified"
		return o
	}
	reference, err := m.readLaunchdPlist(false)
	if err != nil || !bytes.Equal(data, reference) {
		o.reason = "login agent differs from the managed service definition"
		return o
	}
	disabled, err := m.launchdDisabled()
	if err != nil {
		o.reason = "launchd login enablement could not be verified"
		return o
	}
	if disabled {
		o.login = "not configured"
		o.unitState = "disabled"
		return o
	}
	o.login = "enabled"
	o.unitState = "enabled at login"
	return o
}
func (m serviceManager) runLaunchdChoice(kind, choice string, w io.Writer) error {
	if kind == "systemd" {
		return errors.New("systemd is not available on macOS; use dearmachine launchd on|off|status")
	}
	if choice == "status" {
		_, capabilityErr := m.launchctl("print", m.launchdDomain())
		if _, err := fmt.Fprintf(w, "macOS graphical login manager available: %t\n", capabilityErr == nil); err != nil {
			return err
		}
		if err := writeStartupStatus(w, m.observeLaunchd()); err != nil {
			return err
		}
		if err := m.writeConsentDetails(w); err != nil {
			return err
		}
		_, err := fmt.Fprintf(w, "Service: %s\nInspect: launchctl print %s\nStartup at login includes the next login after reboot. It does not run after logout or before login.\n", m.launchdLabel(), m.launchdTarget())
		return err
	}
	if err := m.configureLaunchd(kind, choice); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w, "Saved explicit supervision choice. Use dearmachine up to start; dearmachine persistence status to inspect startup at login. A LaunchAgent does not run after logout or before login.")
	return err
}
func (m serviceManager) startLaunchd(args []string, root string) (int, error) {
	unlock, err := m.launchdChoiceLock(true)
	if err != nil {
		return 0, err
	}
	defer unlock()
	owner, err := m.selectLaunchd()
	if err != nil {
		return 0, err
	}
	if owner == "supervisor-lite" {
		return startSupervised(m.executable, args, root)
	}
	if strings.Join(args, "\x00") != "up\x00--foreground" {
		return 0, errors.New("launchd uses saved native configuration; explicit launch flags require supervisor-lite")
	}
	if _, err := m.readLaunchdPlist(false); err != nil {
		return 0, err
	}
	if s, err := supervisor.Request(root, "status", time.Second); err == nil {
		if err := checkOtherOwner(root, s); err != nil {
			return 0, err
		}
		if !m.launchdOwner(s.SupervisorPID) {
			return 0, errors.New("resident supervisor is not the consented launchd owner; stop it before switching")
		}
		s, err = supervisor.Request(root, "up", 5*time.Second)
		return s.DaemonPID, err
	}
	if err := supervisor.CheckAvailable(root); err != nil {
		return 0, err
	}
	_, exists, err := m.inspectLaunchdJob()
	if err != nil {
		return 0, err
	}
	if err := m.enableLaunchd(); err != nil {
		return 0, errors.New("launchd enable failed")
	}
	if !exists {
		if _, err := m.launchctl("bootstrap", m.launchdDomain(), m.launchdPath(false)); err != nil {
			return 0, errors.New("launchd bootstrap unconfirmed; inspect dearmachine launchd status")
		}
	} else if _, err := m.launchctl("kickstart", m.launchdTarget()); err != nil {
		return 0, errors.New("launchd start unconfirmed")
	}
	deadline := time.Now().Add(daemonStartupTimeout)
	for time.Now().Before(deadline) {
		if s, err := supervisor.Request(root, "status", 100*time.Millisecond); err == nil && s.Daemon == "running" {
			if err := checkOtherOwner(root, s); err != nil {
				return 0, err
			}
			if !m.launchdOwner(s.SupervisorPID) {
				return 0, errors.New("another supervisor won the launchd startup race")
			}
			return s.DaemonPID, nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return 0, errors.New("launchd startup unconfirmed; inspect dearmachine status")
}
