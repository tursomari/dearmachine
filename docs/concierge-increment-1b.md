# Native concierge increment 1b

See [increment 3](concierge-increment-3.md) for current bootstrap, discovery,
systemd consent, and conversational management behavior. The notes below record
the earlier increments.

Ordinary `dearmachine up` now starts a small independent Go supervisor. It holds
an exclusive `flock`, owns the append-only daemon log, starts the existing
`up --foreground` command as a child, and serves local lifecycle requests.
It requires no agent session or service manager. This implementation targets
Linux, including its parent-death signal support.

## Paths and ownership

The native installation state root remains `$HOME/.dearmachine`, resolved through
the existing home-directory dependency. Under that root:

| Path | Purpose |
| --- | --- |
| `run/supervisor.lock` | Lifetime `flock`; never unlinked, including on clean exit |
| `run/supervisor.sock` | Live status/control record; mode 0600 |
| `log/dearmachine.log` | Append-only child stdout/stderr and launch diagnostics |
| `run/dearmachine.pid` | Existing daemon singleton record |
| `run/dearmachine.ready` | Existing foreground daemon readiness record |

The root and its run/log directories must be private directories owned by the
current user. Lock/log files must be private regular files with one hard link;
symlink substitutions are rejected. The supervisor is the only layer allowed to
remove a stale control socket, after acquiring the lock. A PID in a file or
response is diagnostic information, never authority for supervisor signalling.
Only the child process group created by this owner receives stop signals.

The supervisor starts in its own session, without terminal input. Leaving the
calling CLI or a future concierge does not end it. Its child has a separate
process group. SIGTERM/SIGINT to the supervisor stops and reaps the child before
releasing the lock; Linux kills the direct child if the supervisor dies abruptly.
This is not cgroup containment: descendants that create their own sessions need
future service-manager containment. Logs are not rotated in this increment.

All package paths derive from `Config.StateDir`. The internal process entry is:

```text
dearmachine _supervise --state-dir <absolute-native-state> -- <foreground-command> [args]
```

It is a plumbing entry point, not an installer. The foreground command must
publish the PID and readiness records above. Normal `up` supplies the native
executable and preserves its foreground launch arguments. `_supervise` does not
redirect the existing application's configuration paths: normal CLI paths still
come from HOME. Tests use private temporary homes/state roots and disposable
children; they never contact a production daemon or a real systemd manager.

## Lifecycle and retry policy

A successful native start requires both daemon records to identify the owned
child. Starting a process alone is insufficient. The process has 15 seconds to
become ready. A control response may time out earlier; the operation can still
complete, so inspect status before another mutation.

Unexpected exits, including exit 0 and exec/readiness failures, retry after
1, 2, 4, 8, 16, 30, and 30 seconds. The eighth consecutive failure gives up in
`failed` state. At least 60 seconds in the ready/running state resets the failure
count on the next exit. Status exposes the remaining retry delay and last exit.
Explicit `up` from a failed/stopped state and explicit `restart` reset the count.

`down` cancels pending retries and waits for the child to be reaped. It sends
SIGTERM to the owned group, escalating to SIGKILL after two seconds. The owner
remains resident in `stopped` state, holding the lock and serving the socket, so
`up` can work through the same endpoint. `restart` reaps the previous child before
launching its replacement. Concurrent `up` calls join the same startup; a `down`
can supersede startup. Conflicting mutations receive an error instead of silently
reordering. Client disconnection does not undo an accepted operation.

A stale supervisor record makes status/down report an unreachable owner; they do
not fall back to signalling its recorded PID. Explicit native `up` can recreate
an unavailable owner only when the flock is free. A conflicting foreground or
service daemon produces an ownership error. The native daemon's existing lock
remains the final exclusion boundary for foreground callers. Pair creation is
rejected while the supervisor is running, starting, backing off, or failed;
first use `down` to cancel its restart intent.

## Exact socket contract

This implements the TS `SocketDaemonControl` version 1 envelope. Each connection
carries one UTF-8 JSON request terminated by a newline:

```json
{"version":1,"command":"status"}
```

`command` is `status`, `up`, `down`, or `restart`. A successful response is one
newline-terminated JSON object:

```json
{"version":1,"ok":true,"status":{"installation":"installed","supervisor":"running","daemon":"running","persistence":"unknown","supervisorPid":100,"daemonPid":101}}
```

The example PIDs are illustrative. Status fields are:

| Field | Values |
| --- | --- |
| `installation` | `absent`, `installed`, `partial`, `unreadable` |
| `supervisor` | `starting`, `running`, `backing-off`, `stopping`, `stopped`, `failed`, `unreachable` |
| `daemon` | `running`, `stopped`, `unknown` |
| `persistence` | `enabled`, `disabled`, `unknown` |
| `retryInMs` | Optional nonnegative integer, present during backoff |
| `lastExit` | Optional exit/start failure string |
| `externalOwner` | Optional boolean, added by native CLI status when it observes another foreground or service owner; never authority to signal a PID |
| `supervisorPid`, `daemonPid` | Optional Go diagnostic extensions; omitted when zero |

Native installation status is read from the state root/registry, independently
of whether the child is running. Persistence is `unknown`: no service probing or
persistence change occurs. An unreachable socket is a client error, not an
observed stopped daemon. Errors use `{"version":1,"ok":false,"status":...,
"error":"..."}`; malformed requests may have an empty status. The TS client
already ignores arbitrary error text and accepts the optional PID extensions.

Requests/responses are capped at 64 KiB including the newline. The server bounds
a connection to four seconds and waits up to 3.5 seconds for an operation result.
This fits the TS client's total five-second deadline. Timeout, disconnection,
invalid framing/version/command, or `ok:false` is an unconfirmed outcome. Status
is read-only. Mutation success reflects observed state, including an already
running/stopped no-op. A longer native startup can continue after an unconfirmed
response; `status` remains available during readiness waiting and backoff.

Native `up` additionally sends optional `argv` and `directory` fields with command
`up`:

```json
{"version":1,"command":"up","argv":["/absolute/path/to/dearmachine","up","--foreground","--verbose"],"directory":"/absolute/working-directory"}
```

These Go extensions preserve explicit CLI flags and the caller's working
directory, including relative paths. A running child's command/directory cannot
change: first `down`, then `up` with the new launch context. A stopped owner's
launch context can be replaced. TS omits these fields and reuses the existing
command/directory. No credentials are sent. The owner retains its initial
environment; environment refresh and transferring ownership between supervisors
are deferred.

## TS mapping and bare CLI handoff

The installer dispatcher and slash commands send the four commands above.
Increment 2 aligns all TS control clients with the native endpoint contract:
`<dearmachine-state-dir>/run/supervisor.sock`. The default is
`$HOME/.dearmachine/run/supervisor.sock`, regardless of `XDG_STATE_HOME`.
On Linux the native `os.UserHomeDir` dependency requires nonempty HOME and has
no passwd/XDG fallback; the shared default resolver requires an absolute HOME.
TS follows the same rule. Go exports `supervisor.DefaultSocketPath` alongside
`SocketPath` for explicit state roots. Version 1 framing is unchanged.

TS accepts an absolute `DEARMACHINE_SUPERVISOR_SOCKET` override or an explicit
`SocketDaemonControl` constructor path for tests and non-default state roots.
The old `$XDG_STATE_HOME/machtiani-installer/supervisor.sock` location is usable
only by explicitly supplying that path; XDG still controls installer data.
This override does not redirect native state: it must point to the socket of
an owner configured for the intended state root. An absent owner must still be
bootstrapped with native `dearmachine up`; the TS client does not spawn owners.

## Increment 2: foreground concierge handoff

Bare native invocation probes both stdin and stdout. `--help` always prints help;
explicit subcommands retain their scriptable/rescue behavior.

| stdin and stdout | Installation detection | Result |
| --- | --- | --- |
| Either is not a TTY | Not performed | Help, exit 0 |
| Both TTY | Absent | Launch `machtiani-installer --concierge`, adding `--source-root` when configured; TS selects fresh setup |
| Both TTY | Installed, including stopped | Launch `machtiani-installer --concierge` for management |
| Both TTY | Partial, unreadable, invalid, or substituted state | Recovery guidance, exit 1; no launch |

Discovery uses `DEARMACHINE_CONCIERGE_BIN` (one absolute executable path or PATH
name). When unset it resolves the exact name `machtiani-installer` on PATH.
Relative paths, shell command strings, and guessed checkout locations are not
supported. Missing/unexecutable binaries print the honest installer or management
guidance plus discovery instructions and exit 1. The override is not silently
replaced by another binary when discovery fails.

Set `DEARMACHINE_SOURCE_ROOT` to an absolute Machtiani umbrella source checkout
for fresh setup. Native absence routing passes it as `--source-root`; the TS
entry also accepts it for bare/`--concierge` launch. Explicit source arguments
win. This reuses the existing detection, installation consent, model wizard,
and post-install re-detection flow; opening the interface authorizes no product
changes. If no source root is configured, the TS fresh-machine shell retains
its exact setup command and local help. Source discovery/bundling remains a
prerequisite for a zero-configuration IXE launch.

The child inherits stdin, stdout, stderr, and environment and owns the foreground
terminal process group. The native parent waits and restores the original
foreground group. It installs no SIGINT handler and does not forward SIGINT:
terminal interrupts reach TS directly, preserving its two-second second-press
window. There is no daemonization or parent-death kill policy for the concierge;
normal terminal/session semantics apply. SIGTTOU is ignored only during foreground
restoration after the child exits.

Child exit codes propagate unchanged, including nonzero codes without duplicate
native error logging. Signal termination maps to `128 + signal` (e.g. SIGTERM
143). Discovery/launch failures exit 1; successful help and ordinary child exit
are 0. Merely opening or exiting the interface never starts/stops a daemon.
An unavailable owner remains an unconfirmed control outcome; explicitly bootstrap
it with native `dearmachine up`. The TS management interface provides local
commands; agent-backed conversation remains deferred.

CLI exit 0 means successful help/status or confirmed lifecycle operation, including
no-ops. Exit 1 means invalid arguments, a failed/unreachable/unknown observation,
partial/unreadable supervised installation, or an unconfirmed operation. A
stopped daemon alone is not an error. `restart` without a supervisor starts a
stopped native installation, but refuses to take over a running legacy daemon.

## Existing systemd lifecycle

`selectSupervision()` is the function boundary and TODO for usable-manager
probing plus explicit saved consent. It currently selects supervisor-lite only.
There are no new systemctl, unit-writing, enabling, lingering, or consent calls.

The existing native service scripts still run `up --foreground`. They bypass
supervisor-lite and keep systemd's existing `Restart=on-failure` policy, introduced
by the native lifecycle commits: one-minute initial delay, six growth steps, and
a one-hour cap. Their foreground process and provider-failure behavior are
unchanged. `--foreground`, `--once`, pair selection, creation, and other explicit
rescue commands remain available. Legacy status/down seams are retained where
no supervisor record exists.

This increment does not adopt a running systemd daemon or migrate a resident
supervisor into a service. Automatic switching must eventually stop the old
owner, verify ownership transfer, and select exactly one retry policy under the
user's explicit consent. Existing service owners and supervisor-lite must not
be configured concurrently for the same installation. Conflicting ownership is
reported rather than hidden by a stopped supervisor record. Actual persistence
availability/consent, logout/reboot tests, and cgroup ownership remain deferred.

## Verification

Tests were written and observed failing before each implementation increment.
The focused gate is run from `dearmachine/`:

```sh
go test ./cmd/dearmachine ./internal/supervisor ./internal/client
go test -race ./cmd/dearmachine ./internal/supervisor ./internal/client
```

Coverage includes detached subprocess startup, concurrent starters, singleton
lock reuse, log ownership, readiness, serialized restart, backoff limits and
reset, cancelled restart intent, framing errors, stale sockets, changed launch
flags, conflicting owners, TTY routing, and conservative install detection.
One existing permission fixture now explicitly chmods its intended public file,
so a restrictive invoking umask cannot invalidate the fixture.

Increment 2 adds default-path and literal TS-envelope tests, conservative launch
routing/discovery tests, and real-PTY foreground, terminal restoration, signal,
and exit-code tests. The installer repository also has an opt-in
`concierge-native.spec.ts` gate using `DEARMACHINE_TEST_BIN`, a freshly built
native CLI, to exercise the actual Go-to-TS handoff and all slash commands against
a disposable native supervisor and dummy daemon. It uses no real provider.

Increment 2 verification: the scoped Go gate passed 466 tests including subtests;
the same gate passed with `-race -p 1`. The native CLI build passed. The companion
TS native-handoff gate passed against that build using entirely disposable state.
