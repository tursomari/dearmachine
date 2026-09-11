package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/supervisor"
)

func TestStartupObservationIndependentOfConsent(t *testing.T) {
	cases := []struct {
		name, state, linger, load, login, reboot, reason string
		queryFails, managerFails, lingerFails, loadFails bool
	}{
		{name: "enabled", state: "enabled", linger: "yes", login: "enabled", reboot: "enabled", reason: "user lingering enabled"},
		{name: "login only", state: "enabled", linger: "no", login: "enabled", reboot: "not configured", reason: "user lingering disabled"},
		{name: "disabled", state: "disabled", login: "not configured", reboot: "not configured", reason: "service is disabled"},
		{name: "masked", state: "masked", login: "not configured", reboot: "not configured", reason: "service is masked"},
		{name: "temporary mask", state: "masked-runtime", login: "cannot verify", reboot: "cannot verify", reason: "only for this boot"},
		{name: "runtime enablement", state: "enabled-runtime", login: "not configured for next boot", reboot: "not configured", reason: "runtime-only"},
		{name: "absent", load: "not-found", login: "not configured", reboot: "not configured", reason: "service is absent"},
		{name: "empty", load: "loaded", login: "cannot verify", reboot: "cannot verify", reason: "not conclusively"},
		{name: "load failure", loadFails: true, login: "cannot verify", reboot: "cannot verify", reason: "not conclusively"},
		{name: "static", state: "static", login: "cannot verify", reboot: "cannot verify", reason: "not conclusive"},
		{name: "manager unavailable", queryFails: true, managerFails: true, login: "cannot verify", reboot: "cannot verify", reason: "manager unavailable"},
		{name: "query failure", queryFails: true, login: "cannot verify", reboot: "cannot verify", reason: "query failed"},
		{name: "linger failure", state: "enabled", lingerFails: true, login: "enabled", reboot: "cannot verify", reason: "could not be inspected"},
		{name: "linger empty", state: "enabled", login: "enabled", reboot: "cannot verify", reason: "not conclusively"},
	}
	for _, tc := range cases {
		for _, consent := range []string{"absent", "false", "true", "invalid"} {
			t.Run(tc.name+"/"+consent, func(t *testing.T) {
				m := serviceManager{home: t.TempDir()}
				if err := os.MkdirAll(m.root(), 0700); err != nil {
					t.Fatal(err)
				}
				switch consent {
				case "false", "true":
					if err := m.save(supervisionConsent{Version: 1, UseSystemd: consent == "true", Persistence: consent == "true"}); err != nil {
						t.Fatal(err)
					}
				case "invalid":
					if err := os.WriteFile(m.consentPath(), []byte("invalid"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				before, beforeErr := os.ReadFile(m.consentPath())
				calls := 0
				m.run = func(name string, args ...string) (string, error) {
					calls++
					fail := errors.New("inspection failed")
					switch name + " " + strings.Join(args, " ") {
					case "systemctl --user show " + conciergeUnit + " --property=UnitFileState --value":
						if tc.queryFails {
							return "", fail
						}
						return tc.state, nil
					case "systemctl --user show " + conciergeUnit + " --property=LoadState --value":
						if tc.loadFails {
							return "", fail
						}
						return tc.load, nil
					case "systemctl --user show-environment":
						if tc.managerFails {
							return "", fail
						}
						return "", nil
					case "loginctl show-user " + strconv.Itoa(os.Getuid()) + " --property=Linger --value":
						if tc.lingerFails {
							return "", fail
						}
						return tc.linger, nil
					default:
						t.Fatalf("unexpected or mutating command: %s %v", name, args)
						return "", fail
					}
				}
				o := m.observeStartup()
				if o.login != tc.login || o.reboot != tc.reboot || !strings.Contains(o.reason, tc.reason) {
					t.Fatalf("observation: %+v", o)
				}
				if calls < 1 || calls > 2 {
					t.Fatalf("unexpected probe count: %d", calls)
				}
				after, afterErr := os.ReadFile(m.consentPath())
				if string(before) != string(after) || os.IsNotExist(beforeErr) != os.IsNotExist(afterErr) {
					t.Fatal("inspection changed consent")
				}
				files, err := os.ReadDir(m.root())
				if err != nil {
					t.Fatal(err)
				}
				for _, file := range files {
					if file.Name() != "supervision.json" {
						t.Fatalf("inspection created %s", file.Name())
					}
				}
			})
		}
	}
}

func TestStatusReportRecoveryAndDurabilityAreSeparate(t *testing.T) {
	retry := int64(7501)
	for _, tc := range []struct{ state, daemon, recovery, chat string }{
		{"running", "running", "active — retries", "leaves Dear Machine running"},
		{"starting", "unknown", "waiting for startup readiness", "does not cancel supervised startup"},
		{"backing-off", "stopped", "waiting to retry", "does not cancel crash recovery"},
		{"failed", "stopped", "consecutive-failure limit reached", "does not restart"},
		{"stopped", "stopped", "inactive until started again", "leaves Dear Machine stopped"},
		{"stopping", "running", "stop in progress", "does not cancel the requested stop"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			s := supervisor.Status{Installation: "installed", Supervisor: tc.state, Daemon: tc.daemon, DaemonPID: 42, SupervisorPID: 41, LastExit: "exit status 1", ConsecutiveFailures: 3, FailureLimit: 8}
			if tc.state == "backing-off" {
				s.RetryInMs = &retry
			}
			o := startupObservation{login: "cannot verify", reboot: "cannot verify", reason: "systemd user manager unavailable"}
			var out strings.Builder
			if err := writeStatusReport(&out, s, true, o, false); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{tc.recovery, tc.chat, "After account logout: not verified", "after reboot (before login): cannot verify", o.reason, "other startup mechanisms not inspected"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q: %s", want, out.String())
				}
			}
			for _, hidden := range []string{"PID", "Saved permission", "Failure limit", "Persistence:"} {
				if strings.Contains(out.String(), hidden) {
					t.Fatalf("normal output contains %q", hidden)
				}
			}
			if tc.state == "backing-off" && !strings.Contains(out.String(), "Next retry: 8 seconds\nLast exit: exit status 1") {
				t.Fatal(out.String())
			}
			if tc.state == "stopped" && !strings.Contains(out.String(), "stopped by request") {
				t.Fatal(out.String())
			}
			out.Reset()
			if err := writeStatusReport(&out, s, true, o, true); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"Daemon PID: 42", "Supervisor PID: 41", "Consecutive failures: 3", "Failure limit: 8"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q: %s", want, out.String())
				}
			}
		})
	}
}

func TestUnsupervisedStatusDoesNotPromiseChatSurvivalOrRecovery(t *testing.T) {
	var out strings.Builder
	if err := writeStatusReport(&out, supervisor.Status{Daemon: "running", Supervisor: "not detected"}, false,
		startupObservation{login: "enabled", reboot: "enabled", reason: "service enabled; user lingering enabled"}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Crash recovery: not verified") || !strings.Contains(out.String(), "Closing this chat: cannot verify") {
		t.Fatal(out.String())
	}
}

func TestStatusPairsAreLabelledInBothViews(t *testing.T) {
	registry := client.PairRegistry{Pairs: []client.Pair{{ID: "pair-one", InboxID: "inbox-one", UserEmail: "sender@example.test"}, {ID: "pair-two", InboxID: "inbox-two", UserEmail: "second@example.test"}}, Inboxes: []client.Inbox{{ID: "inbox-one", Address: "receiver@example.test", Transport: "agentmail"}, {ID: "inbox-two", Address: "other@example.test", Transport: "openmail"}}}
	var out strings.Builder
	if err := printStatusPairs(&out, registry); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Inbox: receiver@example.test\nAuthorized sender: sender@example.test", "Inbox: other@example.test\nAuthorized sender: second@example.test"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
	if strings.Contains(out.String(), "pair-one") {
		t.Fatal("pair ID leaked into normal view")
	}
}

func TestInvalidConsentDoesNotHideObservedState(t *testing.T) {
	m := serviceManager{home: t.TempDir()}
	if err := os.MkdirAll(m.root(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.root(), "supervision.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := m.writeConsentDetails(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "cannot read consent record") {
		t.Fatal(out.String())
	}
}
