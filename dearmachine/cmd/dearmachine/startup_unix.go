//go:build !windows

package main

import (
	"os"
	"strconv"
	"strings"
)

func (m serviceManager) observeStartup() startupObservation {
	if m.platform == "darwin" {
		return m.observeLaunchd()
	}
	o := startupObservation{login: "cannot verify", reboot: "cannot verify", unitState: "not verified", linger: "not inspected"}
	state, err := m.run("systemctl", "--user", "show", conciergeUnit, "--property=UnitFileState", "--value")
	if err != nil {
		o.reason = "service configuration query failed"
		if !m.usable() {
			o.reason = "systemd user manager unavailable"
		}
		return o
	}
	o.unitState = strings.TrimSpace(state)
	switch o.unitState {
	case "":
		load, err := m.run("systemctl", "--user", "show", conciergeUnit, "--property=LoadState", "--value")
		if err == nil && strings.TrimSpace(load) == "not-found" {
			o.unitState = "not-found"
			o.login, o.reboot, o.reason = "not configured", "not configured", "Dear Machine service is absent"
		} else {
			o.reason = "service configuration was not conclusively reported"
		}
	case "disabled", "masked":
		o.login, o.reboot = "not configured", "not configured"
		o.reason = "Dear Machine service is " + o.unitState
	case "masked-runtime":
		o.reason = "service is masked only for this boot; next-boot configuration cannot be inferred"
	case "enabled-runtime":
		o.login, o.reboot = "not configured for next boot", "not configured"
		o.reason = "service enablement is runtime-only and does not survive reboot"
	case "enabled":
		o.login = "enabled"
		// An omitted user can produce empty output outside a login session.
		linger, err := m.run("loginctl", "show-user", strconv.Itoa(os.Getuid()), "--property=Linger", "--value")
		if err != nil {
			o.reason = "service enabled; user lingering could not be inspected"
			return o
		}
		o.linger = strings.TrimSpace(linger)
		switch o.linger {
		case "yes":
			o.reboot, o.reason = "enabled", "service enabled; user lingering enabled"
		case "no":
			o.reboot, o.reason = "not configured", "service enabled at login; user lingering disabled"
		default:
			o.reason = "service enabled; user lingering was not conclusively reported"
		}
	default:
		o.reason = "service enablement is not conclusive for automatic startup"
	}
	return o
}
