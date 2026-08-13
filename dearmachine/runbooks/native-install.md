# Install and run DearMachine natively

Native foreground execution is the default DearMachine deployment. Nix builds
and installs the DearMachine Client and its matching Agent Manager together;
`mct-agent` and the backend commands approved in `dearmachine.toml` are
resolved from the user's `PATH`. Native deployment does not require an OCI
runtime, Compose, systemd, or another process supervisor.

## Prerequisites

Install Nix, install `mct-agent`, and install and authenticate at least one
supported backend. Confirm the commands are available in the environment that
will launch DearMachine:

```bash
command -v mct-agent
command -v codex # or another configured backend
```

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

## Run in the foreground

Initialize the selected entry point if necessary, then start DearMachine as a
normal foreground process:

```bash
project="$HOME/.dearmachine/entrypoint/main"

dearmachine init \
  --entry-point-repo "$project" \
  --mct-agent mct-agent

dearmachine \
  --inbox-id <inbox-id> \
  --project "$project" \
  --entry-point-repo "$project" \
  --pidfile "$HOME/.dearmachine/run/dearmachine.pid" \
  --verbose
```

DearMachine logs to standard error, handles `SIGINT` and `SIGTERM`, and remains
in the foreground. A user may place that command under a supervisor of their
choice, but DearMachine does not require or install one for native operation.

Do not start a native process and a container deployment against the same
inbox and database at the same time.

## Optional container deployment

Use [`host-install.md`](./host-install.md) when the Nix OCI image, explicit
mount boundary, rootless Podman Compose, and current Linux systemd-user helper
are desired. Container deployment is optional for users but is the default
execution path for DearMachine live integration tests.
