package main

import (
	"fmt"
	"io"
	"math"
	"runtime"
	"strings"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/supervisor"
)

// These observations concern only our named user service. Permission to change
// that service is deliberately not consulted by any read-only probe.
type startupObservation struct {
	login, reboot, reason string
	unitState, linger     string
}

func writeStartupStatus(w io.Writer, o startupObservation) error {
	_, err := fmt.Fprintf(w, "Managed startup at login: %s\nManaged startup after reboot (before login): %s\n  Reason: %s\n  Scope: Dear Machine's managed service; other startup mechanisms not inspected\n", o.login, o.reboot, o.reason)
	return err
}

func writeStatusReport(w io.Writer, s supervisor.Status, managed bool, o startupObservation, details bool) error {
	daemon, owner, recovery := s.Daemon, s.Supervisor, "not verified — no responding native supervisor"
	chat := "cannot verify — native supervisor ownership not established"
	if managed {
		switch s.Supervisor {
		case "running":
			recovery = "active — retries if Dear Machine exits unexpectedly"
			if s.Daemon == "running" {
				chat = "leaves Dear Machine running"
			}
		case "starting":
			recovery = "active — waiting for startup readiness"
			chat = "does not cancel supervised startup"
		case "backing-off":
			daemon, owner = "stopped unexpectedly", "waiting to retry"
			recovery = "active — waiting to retry"
			chat = "does not cancel crash recovery"
		case "failed":
			owner = "recovery paused"
			recovery = "paused — consecutive-failure limit reached"
			chat = "does not restart Dear Machine"
		case "stopped":
			owner = "idle"
			daemon = "stopped by request"
			recovery = "inactive until started again"
			chat = "leaves Dear Machine stopped"
		case "stopping":
			recovery = "inactive — stop in progress"
			chat = "does not cancel the requested stop"
		}
	}
	if !managed && s.Supervisor == "stopped" && s.Daemon == "stopped" {
		recovery = "inactive until started again"
		chat = "leaves Dear Machine stopped"
	}
	var text strings.Builder
	if s.Installation != "" && s.Installation != "installed" {
		fmt.Fprintf(&text, "Installation: %s\n", s.Installation)
	}
	fmt.Fprintf(&text, "Dear Machine: %s\nSupervisor: %s\nCrash recovery: %s\n", daemon, owner, recovery)
	if s.RetryInMs != nil {
		fmt.Fprintf(&text, "Next retry: %.0f seconds\n", math.Ceil(float64(*s.RetryInMs)/1000))
	}
	if s.LastExit != "" && (s.Supervisor == "backing-off" || s.Supervisor == "failed") {
		fmt.Fprintf(&text, "Last exit: %s\n", s.LastExit)
	}
	fmt.Fprintf(&text, "\nClosing this chat: %s\nAfter account logout: not verified — session/service lifetime not assessed\n", chat)
	writeStartupStatus(&text, o)
	if details {
		fmt.Fprintf(&text, "\nInstallation: %s\nSupervisor state: %s\nDaemon PID: %d\nSupervisor PID: %d\nService: %s\nService unit state: %s\nUser lingering: %s\n", s.Installation, s.Supervisor, s.DaemonPID, s.SupervisorPID, conciergeUnit, o.unitState, o.linger)
		if managed && s.FailureLimit > 0 {
			fmt.Fprintf(&text, "Consecutive failures: %d\nFailure limit: %d\n", s.ConsecutiveFailures, s.FailureLimit)
		}
		if s.LastExit != "" {
			fmt.Fprintf(&text, "Last recorded exit: %s\n", s.LastExit)
		}
	}
	_, err := io.WriteString(w, text.String())
	return err
}

func (m serviceManager) writeConsentDetails(w io.Writer) error {
	consent, err := m.load()
	if err != nil {
		_, err = fmt.Fprintln(w, "Saved permission: cannot read consent record (does not determine observed startup state)")
		return err
	}
	if m.platform == "darwin" {
		_, err = fmt.Fprintf(w, "Saved permission (not observed state): launchd service use=%t; startup at login=%t\n", consent.UseLaunchd, consent.Persistence)
		return err
	}
	if runtime.GOOS == "windows" {
		_, err = fmt.Fprintf(w, "Saved permission (not observed state): startup at sign-in=%t\n", consent.Persistence)
		return err
	}
	_, err = fmt.Fprintf(w, "Saved permission (not observed state): service use=%t; reboot/linger=%t\n", consent.UseSystemd, consent.Persistence)
	return err
}

func printStatusPairs(w io.Writer, registry client.PairRegistry) error {
	if len(registry.Pairs) == 0 {
		_, err := fmt.Fprintln(w, "\nNo pairs are registered.")
		return err
	}
	inboxes := make(map[string]client.Inbox, len(registry.Inboxes))
	for _, inbox := range registry.Inboxes {
		inboxes[inbox.ID] = inbox
	}
	for _, pair := range registry.Pairs {
		inbox := inboxes[pair.InboxID]
		if _, err := fmt.Fprintf(w, "\nInbox: %s\nAuthorized sender: %s\nTransport: %s\n", inbox.Address, pair.UserEmail, inbox.Transport); err != nil {
			return err
		}
	}
	return nil
}
