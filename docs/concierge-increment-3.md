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

## Conversation above the local shell

Management opens the existing DSH session adapter lazily, on the first natural
language message. It uses the saved shared model-host profile at
`$HOME/.config/machtiani/model-profile.json`; only the model host resolves the
credential reference. It does not collect credentials or repeat the model wizard.
Missing/invalid profiles and provider failures leave `/help` and local controls
usable. Each management session has a disposable DSH home under installer state,
uses the shared model-host plugin, and omits installer mutation tools.

Management instructions route lifecycle actions through the native CLI, require
read-only status questions, clarification for ambiguous requests, actual observed
results, and two distinct service/persistence questions with explicit replies.
Natural-language replies are handled by that DSH conversation; deterministic
answers use `/systemd on|off|status` and `/persistence on|off|status`. The launcher
pins `DEARMACHINE_NATIVE_BIN` to its own executable for local calls and instructs
the agent to use it. Direct TS launch may set that variable or resolve native
`dearmachine` on PATH. Credential values are never included in prompts.

Slash commands never initialize DSH, consult a provider, or wait on the model
turn queue. Bootstrap progress is rendered through the TUI. Ctrl+C discards
requests still waiting for lazy setup and interrupts active agent work; it does
not undo committed daemon operations. Exit bounds an unresponsive management
protocol shutdown to 500 ms and restores the terminal independently of provider
health. This is wiring and instruction coverage, not a new agent tool/LLM feature
or proof of live model compliance. Live-provider and real reboot/logout gates
remain deferred; no provider or real service manager is used by these tests.

## Increment 3 verification

Behavior tests were written first and observed failing before the corresponding
implementation gates passed. All test invocations used disposable HOME,
XDG state/config/data/runtime roots and native control endpoints; service tests
used injected runners or temporary mock executables. No live provider, real
service configuration, native instance, main worktree, gitlink, or push was used.

- Fresh `go test -count=1` passed: `./cmd/dearmachine` 120 tests/subtests
  (0.363 s), `./internal/supervisor` 19 (1.160 s), `./internal/client` 354
  (2.798 s): 493 total, zero failures or skips. The native binary built afresh.
- Maintained concierge entry/shell/control/CLI/TUI suites, new bootstrap/consent/
  conversation contracts, DSH adapter suite and native handoff suite passed:
  129 tests in ten files, zero failures/skips, 2.49 s overall. Native handoff
  includes three tests against the freshly built CLI, real PTYs, absent-owner
  bootstrap with a guaranteed local parse failure, and offline-profile recovery.
- Workspace `pnpm build` and `pnpm typecheck` passed. The build retained its
  sourcemap/plugin-timing warnings. Both repository diffs pass `git diff --check`.
- Full Nix runtime closure, live-provider compliance, and actual reboot/logout
  tests were not run. The scoped gates avoid the earlier Nix ENOSPC problem;
  approximately 2 GB remained free in `/tmp` at completion.

After a bootstrap attempt, TS reports the observed running/backoff/failed/stopped
state without issuing a second `up`. A concurrent stop is not undone and failed
startup is not blindly retried. Systemd startup also verifies that its MainPID
matches the observed native supervisor before claiming service ownership.
