# DearMachine container boundary

The install path packages the dearmachine client as an immutable OCI image while keeping
machine-specific state and independently released tools outside that image.
This is the same image-versus-runtime split used by xsrc: Nix builds the image;
the stack wrapper prepares isolated rootless Podman storage and supplies the
runtime mounts.

## In the image

`localhost/dearmachine:nix` contains:

- the CGO-enabled `dearmachine` package and its wrapped `git` dependency;
- BusyBox and CA certificates;
- a small entry point that can load `AGENTMAIL_API_KEY` from a production
  Podman secret; and
- a PID-file health probe.

The image contains no inbox identity, API key, backend credential, mct-agent
session, coding repository, Agent Manager ticket, or mutable database.

## Mounted at runtime

The `dearmachine` Compose service has these host mounts:

| Host input | Container path | Access | Purpose |
| --- | --- | --- | --- |
| `DEARMACHINE_CLIENT_HOME` | `/home/dearmachine` | read/write | Private per-stack home containing backend configuration/credentials and caches. |
| `DEARMACHINE_CLIENT_CONFIG_DIR` | `/home/dearmachine/.dearmachine/config` | read/write | Client and Agent Manager configuration. |
| `DEARMACHINE_CLIENT_STATE_DIR` | `/home/dearmachine/.dearmachine/state` | read/write | SQLite database and durable client state. |
| `DEARMACHINE_CLIENT_RUN_DIR` | `/home/dearmachine/.dearmachine/run` | read/write | PID and other ephemeral run metadata. |
| `DEARMACHINE_CLIENT_LOG_DIR` | `/home/dearmachine/.dearmachine/log` | read/write | Client logs when file logging is configured. |
| `DEARMACHINE_MACHTIANI_DIR` | `/home/dearmachine/.machtiani` | read/write | mct-agent session/project stores. |
| `DEARMACHINE_PROJECT_DIR` | `/workspace` | read/write | The single coding repository in which mct-agent and backend workers operate. |
| `DEARMACHINE_TOOLS_DIR` | `/opt/dearmachine/bin` | read-only | Operator-curated `mct-agent`, `agent-manager`, `codex`, `forge`, and custom-backend executables or symlinks. |
| `/nix/store` | `/nix/store` | read-only | Resolves Nix-store interpreters, libraries, and targets used by mounted Nix-installed tools. |

The explicit client directories default beneath the private client home, while
the client home and tools directory default beneath `DEARMACHINE_STATE_DIR`.
The stack never mounts the ambient host home. Authenticate
backend CLIs into that private client home, or provision only their narrowly
required files there. Do not mount a normal home directory merely to make a
backend discover its credentials.

The selected project is deliberately the only coding tree mounted in v1. It
should be a self-contained checkout: linked worktrees whose Git directory is
outside the project mount need an additional future boundary design. Mounted
tools must be Linux-compatible, and dynamically linked tools must either be
Nix-built (with their store closure already present) or otherwise carry an
interpreter and libraries available in the image.

## Compose modes and secrets

Development mode uses the base `deploy/compose/compose.yaml` and passes an
already-exported `AGENTMAIL_API_KEY` into the container. Production mode adds
`compose.production.yaml`, clears that environment value, and reads the
external Podman secret `dearmachine_agentmail_api_key` through
`/run/secrets/dearmachine_agentmail_api_key`. Test mode adds
`compose.test.yaml`; it runs a credential-free long-lived image/help probe so
the rootless Compose lifecycle can be verified without touching AgentMail.

The wrapper intentionally keeps `podman-compose` even though v1 has one
service. This matches xsrc's operator interface, makes health/restart/secret
semantics declarative, and leaves room for a later relay or diagnostics
sidecar without replacing the lifecycle command.

## User service and release boundary

`dearmachine-stack.service` is a systemd user unit. This differs intentionally
from xsrc's dedicated system-user service: DearMachine agents operate as the
current user and need that user's selected repository, backend credentials,
and `~/.machtiani`. The unit does not elevate privileges, use system systemd,
or broaden the Compose mounts to the ambient home.

`dearmachine-host-lifecycle` copies each realized OCI tarball into the private
user archive at `~/.local/share/dearmachine/image-archive/releases`, and keeps
`current` plus one `rollback` symlink. Upgrade and rollback stop the unit,
unload the currently tagged Podman image, change both archive/runtime links,
and start through the Stage 2 wrapper, which loads the selected archive.
Mutable client and Podman state is never stored in those release directories
and survives upgrades and default uninstall.

Production secret management uses the same isolated Podman configuration as
the wrapper. The lifecycle synchronizes only
`dearmachine_agentmail_api_key`; the production overlay mounts it at the
existing `/run/secrets/dearmachine_agentmail_api_key` path and the image entry
point reads it from there.

## Health and migration limits

The container healthcheck proves that the supervised dearmachine PID is alive;
it does not prove a successful AgentMail poll or backend turn. The migration
lifecycle therefore requires both container health and a new verbose `poll:`
line before marking a cutover clean. Until migration is explicitly performed,
the container stack and a legacy host process are separate state domains and
must not poll the same inbox concurrently.
