# Install and run DearMachine natively

Native execution is the default DearMachine deployment. Nix builds
and installs the DearMachine Client and its matching Agent Manager together;
`machtiani` and the backend commands approved in `dearmachine.toml` are
resolved from the user's `PATH`. Native deployment does not require an OCI
runtime, Compose, systemd, or another process supervisor.

## Prerequisites

Install Nix, install `machtiani`, and install and authenticate at least one
supported backend. Confirm the commands are available in the environment that
will launch DearMachine:

```bash
command -v machtiani
command -v codex # or another configured backend
```

The `--agent-bin` flag defaults to `machtiani`; pass an explicit path when the
executable is not resolved from the launch environment's `PATH`.

DearMachine packages Agent Manager itself. Do not install a separately built
Agent Manager for normal native operation.

## Install the native package

From the DearMachine source checkout:

```bash
nix flake check
nix profile install .#dearmachine
command -v dearmachine
command -v agent-manager
```

The default package contains both commands. Nix profiles retain the selected
store path across garbage collection. The `nix run .#install` app remains a
development convenience for copying both commands into `~/.local/bin`, but a
profile installation is the supported durable Nix install.

## Configure the backend order

Create `~/.dearmachine/config/dearmachine.toml` interactively:

```bash
dearmachine setup-agents --backend codex
```

Or write the version-1 configuration directly:

```toml
version = 1
backends = ["codex"]
```

The built-in `codex-yolo` backend runs the host `codex` command with approvals
and sandboxing disabled. It can access any host path available to the
DearMachine user and is therefore excluded from the interactive setup default;
select it explicitly only when email-dispatched work should have that authority:

```toml
version = 1
backends = ["codex-yolo", "forge"]
```

DearMachine resolves `agent-manager` from the installed package. Agent Manager
reads this configuration and resolves each selected backend from the inherited
`PATH`. Changing backend installation or authentication does not require
rebuilding DearMachine.

## Provide the AgentMail credential

Prefer a private credential file over a long-lived shell value:

```bash
install -d -m 0700 "$HOME/.config/dearmachine"
install -m 0600 /dev/null "$HOME/.config/dearmachine/agentmail-api-key"
# Write the key into that file without printing it.
export AGENTMAIL_API_KEY_FILE="$HOME/.config/dearmachine/agentmail-api-key"
```

The file must contain exactly one non-empty line. `AGENTMAIL_API_KEY` remains
supported when a caller already supplies the credential through its
environment.

## Create and run

Create the first pair and start DearMachine's native background client. The
same command initializes the default entry-point repository when it is absent:

```bash
project="$HOME/.dearmachine/entrypoint/main"

dearmachine up --create \
  --email <user-email> \
  --new-inbox --transport agentmail \
  --project "$project" \
  --entry-point-repo "$project" \
  --magnifica-humanitas

dearmachine status
```

Existing repositories are left unchanged. To use a custom entry-point path,
initialize it explicitly with `dearmachine init --entry-point-repo <path>`
before selecting it with `up --create`.

Use `dearmachine down` and `dearmachine up` to stop and restart all registered
pairs. To adopt an existing inbox, replace `--new-inbox` with
`--inbox <inbox-id-or-address>`; specifying `--transport agentmail` also allows
an exact provider inbox to be registered. Use `up --foreground` only when a
service manager or test container must supervise the process directly.

Do not start a native process and a container deployment against the same
inbox and database at the same time.

## Optional transient user service (Linux)

For an unattended native client, use the packaged transient-service launcher.
It resolves every backend in the selected device configuration under the
launching shell's `PATH`, then passes a validated, absolute-path snapshot to
systemd. This keeps Node/NVM-style CLI wrappers and custom backend wrappers in
the same runtime environment as their preflight check. The snapshot does not
follow later shell changes; rerun the launcher after changing a backend
installation, its runtime, or your relevant `PATH` entries.

First stop any existing client. Do not replace a live service or start a
second daemon:

```bash
systemctl --user stop dearmachine-native.service
```

Then start the service from a current shell in which all selected backends
resolve. After a profile upgrade, do not reuse `PATH` from the running client's
`/proc/<pid>/environ`: the Nix wrapper may have prepended its old store path,
which can make the launcher select a stale Agent Manager. Pass the matching
profile executable explicitly so the client and Agent Manager advance together:

```bash
project="$HOME/.dearmachine/entrypoint/main"
credential_file="$HOME/.config/dearmachine/agentmail-api-key"
backend_environment_file="$HOME/.config/dearmachine/backends.env"
client="$HOME/.nix-profile/bin/dearmachine"
manager="$HOME/.nix-profile/bin/agent-manager"

nix run .#dearmachine-native-service -- \
  --credential-file "$credential_file" \
  --environment-file "$backend_environment_file" \
  --agent-manager "$manager" \
  --working-directory "$project" \
  -- "$client" \
    --project "$project" \
    --entry-point-repo "$project" \
    --magnifica-humanitas \
    --verbose
```

The launcher reads `~/.dearmachine/config/dearmachine.toml` by default and adds
its matching `--config` and absolute `--agent-manager` arguments to the client.
The explicit `--agent-manager` above prevents an upgrade from mixing package
revisions. When it is omitted, the launcher resolves `agent-manager` from the
current launch shell's normalized `PATH`, never from a previous service
process. Use `--config`, `--agent-manager`, `--home`, or `--unit` before the
separator when the normal paths differ. `--environment-file` is optional, but
use it for non-AgentMail backend credentials such as `DEEPSEEK_API_KEY`; it is
recorded in the unit only as a path. The launcher passes the AgentMail
credential *file path*, not its value, to systemd and force-unsets any
`AGENTMAIL_API_KEY` supplied by that environment file.

Confirm the unit and its runtime lookup environment before accepting work:

```bash
systemctl --user show dearmachine-native.service \
  -p ActiveState -p SubState -p MainPID -p NRestarts

service_pid=$(systemctl --user show dearmachine-native.service -p MainPID --value)
service_path=$(tr '\0' '\n' <"/proc/$service_pid/environ" | sed -n 's/^PATH=//p')
service_manager=$(
  tr '\0' '\n' <"/proc/$service_pid/cmdline" |
    awk 'previous == "--agent-manager" { print; exit } { previous = $0 }'
)
test "$(readlink -f "$service_manager")" = \
  "$(readlink -f "$HOME/.nix-profile/bin/agent-manager")"
test "$(tr '\0' '\n' <"/proc/$service_pid/cmdline" | \
  grep -cx -- '--magnifica-humanitas')" -eq 1
env -i HOME="$HOME" PATH="$service_path" \
  agent-manager backend resolve --config "$HOME/.dearmachine/config/dearmachine.toml"
```

Finally, run `agent-manager backend health <backend>` from that same service
environment and send one controlled AgentMail request. The health command
invokes the selected backend and may incur model-provider cost; an actual mail
round trip is the required end-to-end check.

## Optional container deployment

Use [`host-install.md`](./host-install.md) when the Nix OCI image, explicit
mount boundary, rootless Podman Compose, and current Linux systemd-user helper
are desired. Container deployment is optional for users but is the default
execution path for DearMachine live integration tests.
