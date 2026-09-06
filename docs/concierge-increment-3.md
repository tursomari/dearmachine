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
