package agentmanager

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// npm shims are shell programs. Resolve official native payloads directly so
// prompts and file paths never pass through cmd.exe expansion or quoting.
func backendLookPath(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
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
