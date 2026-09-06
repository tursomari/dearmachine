# Concierge increment 3

## Discovery and fresh setup

Native bare-TTY discovery tries, in order: `DEARMACHINE_CONCIERGE_BIN` when
set (authoritative absolute executable path or PATH name, with no fallback on
failure); otherwise `machtiani-installer` on PATH, then
`$HOME/.local/bin/machtiani-installer`, then
`$HOME/.nix-profile/bin/machtiani-installer`. Every candidate must be executable.
No checkout is guessed. Missing binaries produce setup/management guidance and
exit 1. Non-TTY bare invocation and explicit help remain provider-free help.
`DEARMACHINE_SOURCE_ROOT` supplies an absolute umbrella checkout for fresh setup;
explicit TS `--source-root` wins and must also be absolute. Installed management
does not repeat setup. Existing init/setup-agents/inbox/lifecycle commands remain.

## Bootstrap contract

An explicit TS `/up` or scriptable `up` may bootstrap only after ENOENT or
ECONNREFUSED from the supervisor endpoint. Status, down, restart, help, entry,
and exit never bootstrap. Protocol failures and timeouts remain uncertain
outcomes, never reasons to create another owner. Concurrent requests in one
interface join one bootstrap; competing interfaces converge on Go's flock.

TS invokes `dearmachine up --bootstrap` with closed stdin and a 20-second process
deadline, then polls the socket for observed state with a 20-second total deadline.
`DEARMACHINE_NATIVE_BIN` optionally selects one native executable (absolute path
or PATH name). The headless bootstrap checks installation metadata without setup,
prompts, provider work, or credentials. It uses the normal detached native owner
and its singleton lock. Startup success requires an installed, running daemon;
launching a process is not success. A losing native starter may fail while a
competing starter succeeds; the observed endpoint decides. Timeout leaves the
outcome unconfirmed and directs the user to `dearmachine status`.

Bootstrap requires the selected socket to equal
`$HOME/.dearmachine/run/supervisor.sock`. An arbitrary socket override still works
for existing owners, but cannot redirect native installation state. Start custom
owners explicitly. Bootstrap does not transfer an existing foreground/service
owner or signal recorded PIDs. Closing the interface leaves the owner intact.

## Optional systemd and separate consent

`dearmachine systemd status` probes a usable user manager at runtime using
`systemctl --user show-environment`; an executable on PATH alone is insufficient.
No implicit systemd use or persistence changes occur. `systemd on` approves unit
creation and daemon-reload only. `persistence on` requires that prior choice and
separately approves both `loginctl enable-linger` and enabling
`dearmachine-concierge.service` at login/reboot. Neither choice starts the daemon.
The default remains supervisor-lite. A previously consented but unavailable
manager reports an error rather than silently choosing a different owner.

Choices live in private, atomically replaced `$HOME/.dearmachine/supervision.json`
with a separate transaction flock. Failed persistence application retains the
explicit desired choice, but status probes the actual unit and lingering state;
saved approval is never proof of success. Failed or incomplete observation is
`unknown`. Unit files follow `XDG_CONFIG_HOME` or `$HOME/.config`, beneath
`systemd/user/`. An unrelated/substituted unit is not overwritten.

The unit runs the same Go `_supervise` owner, with `Restart=no` and
`KillMode=control-group`. Go alone owns daemon retries. The same socket handles
up/down/restart. A resident owner must be stopped through its existing lifecycle
before changing supervision; this increment deliberately refuses automatic
transfer. For a systemd owner use `systemctl --user stop dearmachine-concierge.service`.
For supervisor-lite, `/down` stops the child but retains the owner; owner migration
requires an explicit administrative owner shutdown. Never signal a guessed PID.
The systemd unit uses saved native configuration; launch flags require lite mode.

`/systemd` and `/persistence` ask separate local consent questions. Answers are
`/systemd on|off|status` and `/persistence on|off|status`, mirrored by the native
CLI. Unknown input grants no consent. `/help` lists exact inspection and recovery
commands. `persistence off` disables the unit but retains account-wide lingering,
which may serve other services. `loginctl disable-linger` is the explicit account
operation when no other service needs it. Disable persistence before systemd off.
`/down` cancels daemon retries even with persistence enabled; login/reboot can
start it again. Real logout/reboot verification requires a disposable systemd
machine and remains outside these mock-only tests.
