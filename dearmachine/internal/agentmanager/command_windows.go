package agentmanager

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	machineEnvironmentKey = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
	userEnvironmentKey    = `Environment`
)

// Agent Manager inherits the environment captured at logon, so a backend
// installed later is missing from its PATH. Fall back to the PATH currently
// stored in the registry, which installers update.
func backendLookPath(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		registryPath, ok := registryLookPath(name)
		if !ok {
			return "", err
		}
		path = registryPath
	}
	return nativeBackendPath(path)
}

func registryLookPath(name string) (string, bool) {
	if strings.ContainsAny(name, `:\/`) {
		return "", false
	}
	return lookPathInDirs(name, registryPathDirs())
}

func lookPathInDirs(name string, dirs []string) (string, bool) {
	for _, dir := range dirs {
		if !filepath.IsAbs(dir) {
			continue
		}
		// A name with a directory makes LookPath try each PATHEXT extension
		// in that directory only.
		if path, err := exec.LookPath(filepath.Join(dir, name)); err == nil {
			return path, true
		}
	}
	return "", false
}

// registryPathDirs returns the machine PATH followed by the user PATH, as
// Windows composes them for a new logon. Variables such as %NVM_SYMLINK% are
// resolved from the stored environment first, since installers often define
// them in the same update that adds them to PATH.
func registryPathDirs() []string {
	machine := readRegistryEnvironment(registry.LOCAL_MACHINE, machineEnvironmentKey)
	user := readRegistryEnvironment(registry.CURRENT_USER, userEnvironmentKey)
	lookup := func(name string) (string, bool) {
		key := strings.ToUpper(name)
		for _, environment := range []map[string]string{user, machine} {
			if value, ok := environment[key]; ok {
				return expandWindowsEnvironment(value, os.LookupEnv), true
			}
		}
		return os.LookupEnv(name)
	}
	var dirs []string
	for _, environment := range []map[string]string{machine, user} {
		dirs = append(dirs, filepath.SplitList(expandWindowsEnvironment(environment["PATH"], lookup))...)
	}
	return dirs
}

func readRegistryEnvironment(root registry.Key, path string) map[string]string {
	environment := map[string]string{}
	key, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return environment
	}
	defer key.Close()
	names, err := key.ReadValueNames(0)
	if err != nil {
		return environment
	}
	for _, name := range names {
		if value, _, err := key.GetStringValue(name); err == nil {
			environment[strings.ToUpper(name)] = value
		}
	}
	return environment
}

// expandWindowsEnvironment replaces %NAME% references like
// ExpandEnvironmentStrings, leaving unknown references unchanged.
func expandWindowsEnvironment(value string, lookup func(string) (string, bool)) string {
	var expanded strings.Builder
	for {
		start := strings.IndexByte(value, '%')
		if start < 0 {
			break
		}
		end := strings.IndexByte(value[start+1:], '%')
		if end < 0 {
			break
		}
		end += start + 1
		if replacement, ok := lookup(value[start+1 : end]); ok && end > start+1 {
			expanded.WriteString(value[:start])
			expanded.WriteString(replacement)
			value = value[end+1:]
			continue
		}
		// The closing % may open the next reference.
		expanded.WriteString(value[:end])
		value = value[end:]
	}
	expanded.WriteString(value)
	return expanded.String()
}

// npm shims are shell programs. Resolve official native payloads directly so
// prompts and file paths never pass through cmd.exe expansion or quoting.
func nativeBackendPath(path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".cmd" && ext != ".bat" && ext != ".ps1" {
		return path, nil
	}
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	var packages []string
	switch base {
	case "codex":
		packages = []string{
			"@openai/codex-win32-x64/vendor/x86_64-pc-windows-msvc/bin/codex.exe",
			"@openai/codex/node_modules/@openai/codex-win32-x64/vendor/x86_64-pc-windows-msvc/bin/codex.exe",
			"@openai/codex/vendor/x86_64-pc-windows-msvc/bin/codex.exe",
		}
	case "claude":
		packages = []string{
			"@anthropic-ai/claude-code-win32-x64/claude.exe",
			"@anthropic-ai/claude-code/node_modules/@anthropic-ai/claude-code-win32-x64/claude.exe",
		}
	}
	for _, relative := range packages {
		candidate := filepath.Join(filepath.Dir(path), "node_modules", filepath.FromSlash(relative))
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s is a Windows shell shim without a supported native payload; install the backend's native Windows executable", path)
}
