//go:build linux

package main

// Uninstall stays in the native process: Linux keeps an unlinked executable and
// its mapped libraries alive until exit. No script, runtime or helper is needed
// after the release is removed.
import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/supervisor"
	"golang.org/x/sys/unix"
)

type uninstallPlan struct {
	home                                 string
	ownedSoftware                        bool
	paths, releases, processRoots, units []string
	identities                           map[string]os.FileInfo
}

func insideUninstallRoot(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// Never follow a symlink in a deletion root or its parents. Interior symlinks
// are unlinked by RemoveAll, never traversed.
func validateUninstallPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return fmt.Errorf("unsafe uninstall path: %s", path)
	}
	for at := path; at != "/"; at = filepath.Dir(at) {
		info, err := os.Lstat(at)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlinked uninstall path: %s", at)
		}
		if at == path {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || stat.Uid != uint32(os.Getuid()) {
				return fmt.Errorf("uninstall target is not owned by you: %s", at)
			}
		}
	}
	return nil
}

func buildUninstallPlan(home string, getenv func(string) string) (uninstallPlan, error) {
	p := uninstallPlan{home: home, identities: make(map[string]os.FileInfo)}
	if err := validateUninstallPath(home); err != nil {
		return p, err
	}
	xdg := func(key, fallback string) (string, error) {
		value := getenv(key)
		if value == "" {
			value = filepath.Join(home, fallback)
		}
		if !filepath.IsAbs(value) || filepath.Clean(value) != value || value == "/" || value == home {
			return "", fmt.Errorf("unsafe %s", key)
		}
		return value, nil
	}
	data, err := xdg("XDG_DATA_HOME", ".local/share")
	if err != nil {
		return p, err
	}
	config, err := xdg("XDG_CONFIG_HOME", ".config")
	if err != nil {
		return p, err
	}
	state, err := xdg("XDG_STATE_HOME", ".local/state")
	if err != nil {
		return p, err
	}
	cache, err := xdg("XDG_CACHE_HOME", ".cache")
	if err != nil {
		return p, err
	}
	add := func(path string) error {
		for _, existing := range p.paths {
			if existing == path {
				return nil
			}
		}
		if err := validateUninstallPath(path); err != nil {
			return err
		}
		if info, err := os.Lstat(path); err == nil {
			p.identities[path] = info
		} else if !os.IsNotExist(err) {
			return err
		}
		p.paths = append(p.paths, path)
		return nil
	}
	// The standalone Machtiani profile and backend homes are not DearMachine's.
	for _, path := range []string{
		filepath.Join(home, ".dearmachine"), filepath.Join(home, ".config/dearmachine"), filepath.Join(config, "dearmachine"),
		filepath.Join(state, "dearmachine"), filepath.Join(state, "machtiani-installer"),
		filepath.Join(cache, "dearmachine"), filepath.Join(cache, "machtiani-installer"),
		filepath.Join(data, "machtiani-installer"),
		filepath.Join(home, ".local/state/dearmachine"), filepath.Join(home, ".local/state/machtiani-installer"),
		filepath.Join(home, ".cache/dearmachine"), filepath.Join(home, ".cache/machtiani-installer"),
		filepath.Join(home, ".local/share/machtiani-installer"),
	} {
		if err := add(path); err != nil {
			return p, err
		}
	}
	// Machtiani keeps per-project sessions outside the repository. Remove only
	// UUID stores referenced by a DearMachine-owned workspace, not its whole home.
	for _, workspace := range []string{filepath.Join(home, ".dearmachine/entrypoint"), filepath.Join(data, "machtiani-installer/workspace")} {
		if err := validateUninstallPath(workspace); err != nil {
			return p, err
		}
		err := filepath.WalkDir(workspace, func(path string, entry fs.DirEntry, walkErr error) error {
			if os.IsNotExist(walkErr) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() && entry.Name() == ".git" {
				return filepath.SkipDir
			}
			if entry.Name() != "project.uuid" || filepath.Base(filepath.Dir(path)) != ".machtiani" {
				return nil
			}
			if err := validateUninstallPath(path); err != nil {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			id, err := uuid.Parse(strings.TrimSpace(string(b)))
			if err != nil || id == uuid.Nil {
				return fmt.Errorf("invalid project identity: %s", path)
			}
			return add(filepath.Join(home, ".machtiani", id.String()))
		})
		if err != nil {
			return p, err
		}
	}
	if custom := getenv("DEARMACHINE_HOME"); custom != "" && custom != filepath.Join(home, ".dearmachine") {
		return p, errors.New("custom DEARMACHINE_HOME needs explicit path review before uninstall; no changes made")
	}
	p.releases = []string{filepath.Join(data, "dearmachine"), filepath.Join(home, ".local/share/machtiani/bootstrap")}
	if data != filepath.Join(home, ".local/share") {
		p.releases = append(p.releases, filepath.Join(home, ".local/share/dearmachine"))
	}
	if executable, err := os.Executable(); err == nil {
		p.processRoots = append(p.processRoots, executable)
	}
	if managed := getenv("DEARMACHINE_MANAGED_DATA_HOME"); managed != "" {
		if !filepath.IsAbs(managed) || filepath.Clean(managed) != managed || managed == "/" || managed == home {
			return p, errors.New("unsafe managed data home")
		}
		root := filepath.Join(managed, "dearmachine")
		if root != p.releases[0] {
			p.releases = append(p.releases, root)
		}
	}
	for _, root := range p.releases {
		if executable, err := os.Executable(); err == nil && insideUninstallRoot(executable, root) {
			p.ownedSoftware = true
		}
		if err := validateUninstallPath(root); err != nil {
			return p, err
		}
		for _, lock := range []string{"update.lock", ".bootstrap-lock", "transaction.json"} {
			if _, err := os.Lstat(filepath.Join(root, lock)); !os.IsNotExist(err) {
				return p, fmt.Errorf("installation is busy or needs recovery: %s", filepath.Join(root, lock))
			}
		}
		p.processRoots = append(p.processRoots, root)
		// Receipts only identify processes; they never add arbitrary deletion paths.
		entries, err := os.ReadDir(filepath.Join(root, "releases"))
		if err != nil && !os.IsNotExist(err) {
			return p, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			var receipt struct {
				Binaries map[string]string `json:"binaries"`
			}
			receiptPath := filepath.Join(root, "releases", entry.Name(), "release.json")
			if err := validateUninstallPath(receiptPath); err != nil {
				return p, err
			}
			b, err := os.ReadFile(receiptPath)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return p, err
			}
			if err := json.Unmarshal(b, &receipt); err != nil {
				return p, fmt.Errorf("invalid release receipt: %w", err)
			}
			for name, binary := range receipt.Binaries {
				if name != "dearmachine" && name != "machtiani-installer" {
					continue
				}
				if !regexp.MustCompile(`^/nix/store/[a-z0-9]{32}-[^\s/]+/bin/` + name + `$`).MatchString(binary) {
					return p, fmt.Errorf("invalid managed executable in %s", receiptPath)
				}
				p.processRoots = append(p.processRoots, filepath.Dir(filepath.Dir(binary)))
			}
		}
	}
	for _, name := range []string{"dearmachine", "agent-manager", "machtiani", "machtiani-installer", "machtiani-model-host"} {
		path := filepath.Join(home, ".local/bin", name)
		if err := validateUninstallPath(filepath.Dir(path)); err != nil {
			return p, err
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return p, err
		}
		target, err := os.Readlink(path)
		if err != nil {
			return p, fmt.Errorf("unmanaged command requires removal through its owner: %s", path)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		owned := false
		for _, root := range p.releases {
			owned = owned || insideUninstallRoot(filepath.Clean(target), root)
		}
		if !owned {
			if name == "machtiani" {
				continue
			}
			return p, fmt.Errorf("unmanaged command requires removal through its owner: %s", path)
		}
		p.paths = append(p.paths, path)
		p.identities[path] = info
		p.ownedSoftware = true
	}
	if _, err := os.Stat(filepath.Join(home, ".nix-profile/bin/dearmachine")); err == nil {
		return p, errors.New("a separate Nix profile exposes dearmachine; remove or migrate that exact profile entry through its owner before uninstall")
	} else if !os.IsNotExist(err) {
		return p, err
	}
	for _, root := range p.releases {
		if err := add(root); err != nil {
			return p, err
		}
	}
	for _, name := range []string{conciergeUnit, "dearmachine-native.service", "dearmachine-stack.service"} {
		path := filepath.Join(config, "systemd/user", name)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return p, err
		}
		if name == "dearmachine-stack.service" {
			return p, fmt.Errorf("container service needs its owning lifecycle removal first: %s", path)
		}
		if err := validateUninstallPath(path); err != nil {
			return p, err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return p, err
		}
		owned := strings.HasPrefix(string(b), unitMarker)
		if name == "dearmachine-native.service" {
			wrapper := filepath.Join(data, "dearmachine/native-service/dearmachine-native-launcher")
			owned = strings.HasPrefix(string(b), "[Unit]\nDescription=Dear Machine native client\n") && strings.Contains(string(b), "\nExecStart="+systemdQuote(wrapper)+"\n")
		}
		if !owned {
			return p, fmt.Errorf("unmanaged service: %s", path)
		}
		p.units = append(p.units, name)
		if err := add(path); err != nil {
			return p, err
		}
		for _, suffix := range []string{".d", ".wants", ".requires"} {
			if err := add(path + suffix); err != nil {
				return p, err
			}
		}
	}
	// A bind mount is not a symlink: RemoveAll would cross it. Refuse mounted
	// descendants instead of turning product cleanup into another volume's purge.
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return p, err
	}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(string(mounts), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mount := unescape.Replace(fields[4])
		for _, path := range p.paths {
			if insideUninstallRoot(mount, path) {
				return p, fmt.Errorf("unmount before uninstall: %s", mount)
			}
		}
	}
	// Delete stores before the markers that identify them, so a partial failure
	// or interruption cannot strand an undiscoverable private session store.
	sort.SliceStable(p.paths, func(i, j int) bool {
		return insideUninstallRoot(p.paths[i], filepath.Join(home, ".machtiani")) && !insideUninstallRoot(p.paths[j], filepath.Join(home, ".machtiani"))
	})
	return p, nil
}

func (p uninstallPlan) unchanged() error {
	for _, path := range p.paths {
		if err := validateUninstallPath(filepath.Dir(path)); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		before, existed := p.identities[path]
		if os.IsNotExist(err) && !existed {
			continue
		}
		if err != nil || !existed || !os.SameFile(before, info) {
			return fmt.Errorf("uninstall target changed; inspect and retry: %s", path)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if err := validateUninstallPath(path); err != nil {
				return err
			}
		}
	}
	return nil
}

type uninstallProcess struct {
	pid, parent int
	selected    bool
}

func (p uninstallPlan) processes() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	processes := map[int]uninstallProcess{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		root := filepath.Join("/proc", entry.Name())
		info, err := os.Stat(root)
		if err != nil {
			continue
		}
		if info.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
			continue
		}
		stat, err := os.ReadFile(filepath.Join(root, "stat"))
		if err != nil {
			continue
		}
		end := strings.LastIndex(string(stat), ") ")
		if end < 0 {
			return nil, errors.New("invalid process status")
		}
		fields := strings.Fields(string(stat)[end+2:])
		if len(fields) < 2 || fields[0] == "Z" {
			continue
		}
		parent, _ := strconv.Atoi(fields[1])
		cmd, _ := os.ReadFile(filepath.Join(root, "cmdline"))
		env, _ := os.ReadFile(filepath.Join(root, "environ"))
		selected := false
		if strings.Contains("\x00"+string(env), "\x00HOME="+p.home+"\x00") {
			for _, arg := range strings.Split(string(cmd), "\x00") {
				for _, owned := range p.processRoots {
					if insideUninstallRoot(arg, owned) {
						selected = true
					}
				}
			}
		}
		processes[pid] = uninstallProcess{pid: pid, parent: parent, selected: selected}
	}
	// Include backend descendants before their supervisor can exit and orphan them.
	for changed := true; changed; {
		changed = false
		for pid, process := range processes {
			if !process.selected && processes[process.parent].selected {
				process.selected = true
				processes[pid] = process
				changed = true
			}
		}
	}
	var result []int
	for pid, process := range processes {
		if process.selected {
			result = append(result, pid)
		}
	}
	sort.Ints(result)
	return result, nil
}

func (p uninstallPlan) stop(m serviceManager) error {
	pids, err := p.processes()
	if err != nil {
		return err
	}
	var handles []int
	defer func() {
		for _, fd := range handles {
			unix.Close(fd)
		}
	}()
	for _, pid := range pids {
		fd, err := unix.PidfdOpen(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot safely track process %d: %w", pid, err)
		}
		handles = append(handles, fd)
	}
	for _, unit := range p.units {
		if _, err := m.run("systemctl", "--user", "disable", "--now", unit); err != nil {
			return fmt.Errorf("cannot disable and stop %s: %w", unit, err)
		}
		state, err := m.run("systemctl", "--user", "show", unit, "--property=ActiveState", "--value")
		if err != nil || (state != "inactive" && state != "failed") {
			return fmt.Errorf("service shutdown unconfirmed: %s", unit)
		}
		enabled, err := m.run("systemctl", "--user", "show", unit, "--property=UnitFileState", "--value")
		if err != nil || (enabled != "disabled" && enabled != "static") {
			return fmt.Errorf("service startup removal unconfirmed: %s", unit)
		}
	}
	root := filepath.Join(p.home, ".dearmachine")
	pid, live, err := client.DaemonStatus(filepath.Join(root, "run/dearmachine.pid"))
	if err != nil {
		return err
	}
	if live {
		found := false
		for _, tracked := range pids {
			if tracked == pid {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("cannot verify ownership of daemon PID %d; no data was deleted", pid)
		}
	}
	present, err := supervisor.HasRecord(root)
	if err != nil {
		return err
	}
	if present && supervisor.CheckAvailable(root) != nil {
		if _, err := supervisor.Request(root, "shutdown", 10*time.Second); err != nil {
			return fmt.Errorf("supervisor shutdown unconfirmed: %w", err)
		}
	}
	for _, fd := range handles {
		if err := unix.PidfdSendSignal(fd, syscall.SIGTERM, nil, 0); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		stopped := true
		for _, fd := range handles {
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			if _, err := unix.Poll(fds, 0); err != nil {
				return err
			}
			if fds[0].Revents == 0 {
				stopped = false
			}
		}
		if stopped && (!present || supervisor.CheckAvailable(root) == nil) {
			left, err := p.processes()
			if err != nil {
				return err
			}
			if len(left) == 0 {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("DearMachine processes did not stop; no data was deleted")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (p uninstallPlan) verifyNoOpenData() error {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		root := filepath.Join("/proc", entry.Name())
		info, err := os.Stat(root)
		if err != nil {
			continue
		}
		if info.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
			continue
		}
		if uninstallProcessExited(root) {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(root, "fd"))
		if os.IsNotExist(err) || (err != nil && uninstallProcessExited(root)) {
			continue
		}
		if errors.Is(err, os.ErrPermission) {
			// systemd --user, ssh-agent and other nondumpable OS processes hide
			// their descriptors even from their owner. They are not our workers.
			// An inaccessible process naming an owned path remains a blocker.
			cmd, readErr := os.ReadFile(filepath.Join(root, "cmdline"))
			if readErr != nil {
				return fmt.Errorf("cannot identify protected process %d: %w", pid, readErr)
			}
			for _, arg := range strings.Split(string(cmd), "\x00") {
				for _, path := range append(append([]string{}, p.paths...), p.processRoots...) {
					if insideUninstallRoot(arg, path) {
						return fmt.Errorf("cannot verify protected product process %d; no data was deleted", pid)
					}
				}
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot verify open files for PID %d: %w", pid, err)
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(root, "fd", fd.Name()))
			if err != nil {
				continue
			}
			for _, path := range p.paths {
				if insideUninstallRoot(target, path) {
					return fmt.Errorf("PID %d still has uninstall data open at %s; no data was deleted", pid, path)
				}
			}
		}
	}
	return nil
}

func uninstallProcessExited(root string) bool {
	stat, err := os.ReadFile(filepath.Join(root, "stat"))
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		return false
	}
	end := strings.LastIndex(string(stat), ") ")
	if end < 0 {
		return false
	}
	fields := strings.Fields(string(stat)[end+2:])
	return len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X")
}

func runUninstall(args []string, getenv func(string) string, deps dependencies) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(outputOrDiscard(deps.stdout), "Usage: dearmachine uninstall\nPermanently remove the local DearMachine installation and private data. Requires terminal confirmation; no --yes bypass.")
		return err
	}
	if len(args) != 0 {
		return errors.New("usage: dearmachine uninstall (no confirmation bypass)")
	}
	if !inputIsInteractive(deps) {
		return errors.New("uninstall requires an interactive terminal and explicit confirmation")
	}
	home, err := deps.userHomeDir()
	if err != nil {
		return err
	}
	p, err := buildUninstallPlan(home, getenv)
	if err != nil {
		return err
	}
	out := outputOrDiscard(deps.stdout)
	fmt.Fprintln(out, "Permanently remove DearMachine: stop its daemon, supervisor and concierge; delete its software, credentials, configuration, databases, memory, logs and caches.")
	for _, path := range p.paths {
		if _, exists := p.identities[path]; exists {
			fmt.Fprintln(out, "  "+path)
		}
	}
	fmt.Fprintln(out, "Independent Machtiani/backend installations, external projects, remote accounts, shared Nix store contents and account-wide lingering are preserved. No backup is retained.")
	fmt.Fprint(out, "Type UNINSTALL to confirm permanent deletion (anything else cancels): ")
	answer, err := bufio.NewReader(deps.stdin).ReadString('\n')
	if err != nil || strings.TrimSpace(answer) != "UNINSTALL" {
		fmt.Fprintln(out, "Uninstall cancelled; nothing changed.")
		return nil
	}
	if !p.ownedSoftware {
		return errors.New("no owned managed executable was found; remove this installation through its package owner; no changes made")
	}
	if err := p.unchanged(); err != nil {
		return err
	}
	// Recheck competing acquisition immediately before taking its own locks.
	if _, err := buildUninstallPlan(home, getenv); err != nil {
		return err
	}
	stateRoot := filepath.Join(home, ".dearmachine")
	if _, existed := p.identities[stateRoot]; existed {
		marker := filepath.Join(stateRoot, "uninstalling")
		file, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("another uninstall may be active; inspect %s: %w", marker, err)
		}
		defer os.Remove(marker)
		_, writeErr := fmt.Fprintln(file, os.Getpid())
		if err := errors.Join(writeErr, file.Close()); err != nil {
			return err
		}
	}
	var locks []string
	defer func() {
		for _, lock := range locks {
			os.Remove(lock)
		}
	}()
	for _, root := range p.releases {
		if _, exists := p.identities[root]; !exists {
			continue
		}
		for _, name := range []string{"update.lock", ".bootstrap-lock"} {
			lock := filepath.Join(root, name)
			if err := os.Mkdir(lock, 0700); err != nil {
				return fmt.Errorf("installation lock: %w", err)
			}
			locks = append(locks, lock)
		}
	}
	fmt.Fprintln(out, "Stopping DearMachine processes...")
	if err := p.stop(nativeServiceManager(home)); err != nil {
		return err
	}
	if err := p.verifyNoOpenData(); err != nil {
		return err
	}
	// Keep the supervisor's lifetime lock through deletion so concurrent native
	// starters cannot acquire it between shutdown verification and removal.
	if _, existed := p.identities[stateRoot]; existed {
		lock, err := os.OpenFile(filepath.Join(stateRoot, "run/supervisor.lock"), os.O_RDWR|syscall.O_NOFOLLOW, 0)
		if err == nil {
			defer lock.Close()
			if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				return errors.New("supervisor restarted; uninstall aborted before deletion")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if err := p.unchanged(); err != nil {
		return err
	}
	for _, path := range p.paths {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("uninstall incomplete; could not remove %s: %w", path, err)
		}
	}
	if len(p.units) != 0 {
		if _, err := nativeServiceManager(home).run("systemctl", "--user", "daemon-reload"); err != nil {
			return fmt.Errorf("files removed but service reload failed: %w", err)
		}
	}
	for _, path := range p.paths {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return fmt.Errorf("uninstall incomplete; path remains: %s", path)
		}
	}
	fmt.Fprintln(out, "DearMachine uninstalled. Its owned commands and private data are removed. No cleanup helper remains. Nix store contents, if any, are reclaimed by normal Nix garbage collection.")
	return nil
}

func refuseDuringUninstall(home string) error {
	path := filepath.Join(home, ".dearmachine/uninstalling")
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return fmt.Errorf("uninstall is in progress; inspect %s before starting DearMachine", path)
	}
	return nil
}
