package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/hostos"
	"github.com/dearmachine/dearmachine/internal/supervisor"
)

func TestInstallationStatusIndependentOfSupervisor(t *testing.T) {
	for _, kind := range []string{"absent", "partial", "invalid", "installed", "leftover", "held", "orphan-client", "invalid-lock"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			var out strings.Builder
			deps := dependencies{userHomeDir: func() (string, error) { return home, nil }, stdout: &out}
			root := filepath.Join(home, ".dearmachine")
			wantInstall, wantSupervisor, wantDaemon := "installed", "stopped", "stopped"
			switch kind {
			case "absent":
				wantInstall = "absent"
			case "partial", "invalid":
				wantInstall = "partial"
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "invalid" {
					if err := os.WriteFile(filepath.Join(root, "pairs.toml"), []byte("bad toml ["), 0600); err != nil {
						t.Fatal(err)
					}
				}
			default:
				deps = testDependencies(t, &fakeApplication{})
				makeUpTestPair(t, deps, "installation-status")
				deps.stdout = &out
				home, _ = deps.userHomeDir()
				root = filepath.Join(home, ".dearmachine")
			}
			lockPath := filepath.Join(root, "run", "dearmachine.pid")
			if kind == "leftover" || kind == "held" || kind == "orphan-client" || kind == "invalid-lock" {
				if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
					t.Fatal(err)
				}
				lock, err := os.OpenFile(filepath.Join(root, "run", "supervisor.lock"), os.O_CREATE|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				// Windows privacy is an ACL, not the creation mode.
				if kind != "invalid-lock" {
					if err := hostos.Protect(lock.Name(), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "held" {
					if err := hostos.Flock(int(lock.Fd()), hostos.LOCK_EX|hostos.LOCK_NB); err != nil {
						t.Fatal(err)
					}
					wantSupervisor, wantDaemon = "unreachable", "unknown"
				}
				if kind == "orphan-client" {
					unlock, err := client.CreateDaemonLock(lockPath)
					if err != nil {
						t.Fatal(err)
					}
					defer unlock()
					wantSupervisor, wantDaemon = "unreachable", "unknown"
				}
				if kind == "invalid-lock" {
					if err := lock.Chmod(0644); err != nil {
						t.Fatal(err)
					}
					wantSupervisor, wantDaemon = "unreachable", "unknown"
				}
			}
			if err := runStatus([]string{"--json"}, deps); err != nil {
				t.Fatal(err)
			}
			var response supervisor.Response
			if err := json.Unmarshal([]byte(out.String()), &response); err != nil {
				t.Fatal(err)
			}
			s := response.Status
			if response.Version != 1 || !response.OK || s.Installation != wantInstall || s.Supervisor != wantSupervisor || s.Daemon != wantDaemon || s.Persistence != "unknown" {
				t.Fatalf("status = %+v", response)
			}
			if s.ExternalOwner != (kind == "orphan-client") {
				t.Fatalf("incorrect external ownership observation: %+v", s)
			}
			if strings.Contains(out.String(), "@") {
				t.Fatal("status exposed pair identities")
			}
			if kind == "absent" || kind == "partial" || kind == "invalid" || kind == "installed" {
				if _, err := os.Lstat(filepath.Join(root, "run", "supervisor.lock")); !os.IsNotExist(err) {
					t.Fatalf("status created supervisor state: %v", err)
				}
			}
			observed, managed, err := observeStatus(lockPath, nil)
			if managed || (err != nil) != (wantSupervisor == "unreachable") {
				t.Fatalf("observation: %+v managed=%v err=%v", observed, managed, err)
			}
			out.Reset()
			if err := writeStatusReport(&out, observed, managed, startupObservation{}, false); err != nil {
				t.Fatal(err)
			}
			if kind == "leftover" || kind == "installed" {
				if !strings.Contains(out.String(), "Dear Machine: stopped\nSupervisor: stopped") || !strings.Contains(out.String(), "Crash recovery: inactive until started again") || strings.Contains(out.String(), "Installation: partial") {
					t.Fatal(out.String())
				}
			}
		})
	}
}

// Ownership is a safe observation, not permission to signal that process.
func TestStatusExplainsExternalOwner(t *testing.T) {
	for _, kind := range []string{"foreground", "stale-record", "idle-supervisor"} {
		t.Run(kind, func(t *testing.T) {
			deps := testDependencies(t, &fakeApplication{})
			makeUpTestPair(t, deps, "external-status")
			home, _ := deps.userHomeDir()
			root := filepath.Join(home, ".dearmachine")
			if kind == "idle-supervisor" {
				deps, root = supervisedDeps(t)
				if _, err := supervisor.Request(root, "down", time.Second); err != nil {
					t.Fatal(err)
				}
			}
			lockPath := filepath.Join(root, "run", "dearmachine.pid")
			release, err := client.CreateDaemonLock(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if kind == "stale-record" {
				if err := os.WriteFile(filepath.Join(root, "run", "supervisor.lock"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var output strings.Builder
			deps.stdout = &output
			if err := runStatus([]string{"--json"}, deps); err != nil {
				t.Fatal(err)
			}
			var response struct {
				Status struct {
					ExternalOwner bool `json:"externalOwner"`
				} `json:"status"`
			}
			if err := json.Unmarshal([]byte(output.String()), &response); err != nil {
				t.Fatal(err)
			}
			if !response.Status.ExternalOwner {
				t.Fatalf("external ownership missing: %s", output.String())
			}
			output.Reset()
			_ = runStatus(nil, deps) // Nonzero status may still describe an unmanaged client.
			if !strings.Contains(output.String(), "Ownership: another foreground session or service") ||
				!strings.Contains(output.String(), "Inspect the existing process or service") {
				t.Fatal(output.String())
			}
			pid, running, err := client.DaemonStatus(lockPath)
			if err != nil || !running || pid != os.Getpid() {
				t.Fatalf("owner changed: %d %v %v", pid, running, err)
			}
		})
	}
}
