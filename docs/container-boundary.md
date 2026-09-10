# DearMachine container boundary

The install path packages the dearmachine client as an immutable OCI image while keeping
machine-specific state and independently released tools outside that image.
This is the same image-versus-runtime split used by xsrc: Nix builds the image;
the stack wrapper prepares isolated rootless Podman storage and supplies the
runtime mounts.

## In the image

`localhost/dearmachine:nix` contains:

- the CGO-enabled `dearmachine` package and its wrapped `git` dependency;
- the matching in-tree `agent-manager` executable, resolved from the same Nix
  package as the client;
- BusyBox and CA certificates;
- a small entry point that can load `AGENTMAIL_API_KEY` from a production
  Podman secret and validated backend variables from the private client home;
  and
- a PID-file health probe.

The image contains no inbox identity, API key, backend credential, machtiani
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
| `DEARMACHINE_CLIENT_AGENT_MANAGER_DIR` | `/home/dearmachine/.dearmachine/agent-manager` | read/write | Canonical Agent Manager tickets and worker state, preserved across container replacement and host rollback. |
| `DEARMACHINE_MACHTIANI_DIR` | `/home/dearmachine/.machtiani` | read/write | machtiani session/project stores. |
| `DEARMACHINE_PROJECT_DIR` | `/workspace` | read/write | Explicit coding tree; `DEARMACHINE_PROJECT_SUBDIR` selects the working repository beneath it. |
| `DEARMACHINE_TOOLS_DIR` | `/opt/dearmachine/bin` | read-only | Operator-curated `machtiani`, `codex`, `forge`, `omp`, and custom-backend executables or symlinks. Agent Manager is packaged in the image. |
| `/nix/store` | `/nix/store` | read-only | Resolves Nix-store interpreters, libraries, and targets used by mounted Nix-installed tools. |

The explicit client directories default beneath the private client home, while
the client home and tools directory default beneath `DEARMACHINE_STATE_DIR`.
The stack never mounts the ambient host home. Authenticate
backend CLIs into that private client home, or provision only their narrowly
required files there. Backend `NAME=value` assignments live at
`.config/dearmachine/backends.env` in that private home, must be mode `0600`,
and are validated and exported by the entry point. Do not mount a normal home directory merely to make a
backend discover its credentials.

The selected project tree is the only coding tree mounted. Each working
repository should be self-contained: linked worktrees whose Git directory is
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
the rootless Compose lifecycle can be verified without touching AgentMail. The
probe writes the normal PID file and uses the same `dearmachine-health` command
as production. Test mode does not claim to prove AgentMail authentication or a
backend turn; those require a separately authorized disposable-inbox exercise.

The wrapper intentionally keeps `podman-compose` even though v1 has one
service. This matches xsrc's operator interface, makes health/restart/secret
semantics declarative, and leaves room for a later relay or diagnostics
sidecar without replacing the lifecycle command.

## User service and release boundary

The OCI image and direct `dearmachine-stack` Compose workflow do not require
systemd. The optional Linux lifecycle helper installs
`dearmachine-stack.service` as a systemd user unit. This differs intentionally
from xsrc's dedicated system-user service: DearMachine agents operate as the
current user and need that user's selected repository, backend credentials,
and `~/.machtiani`. The unit does not elevate privileges, use system systemd,
or broaden the Compose mounts to the ambient home.

`dearmachine-container-lifecycle` copies each realized OCI tarball into the private
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

## Health limits

The container healthcheck proves that the supervised dearmachine PID is alive;
it does not prove a successful AgentMail poll or backend turn. Live acceptance
therefore also requires a successful provider poll and backend reply.

## Selecting a project inside a larger tree

Native foreground execution remains the default. Container execution is an
explicit `dearmachine-stack` choice. Set `DEARMACHINE_PROJECT_DIR` to the exact
host tree to mount, and optionally set `DEARMACHINE_PROJECT_SUBDIR` to the
repository beneath it. For example, selecting a projects directory with
`DEARMACHINE_PROJECT_SUBDIR=my-project` mounts that tree at `/workspace`, sets
the container working directory to `/workspace/my-project`, and passes that
same path to `--project`. Paths with spaces work. Absolute subdirectories,
parent traversal, missing directories, and symlink escapes are rejected.
The default subdirectory remains the mount root. Select only the intended
project scope; a larger mount grants workers access to its other contents.

Before create, up, rebuild, or config, the stack validates its writable state
paths against the launching user's native `.dearmachine` and `.machtiani`
trees, including symlink aliases. It also compares the native and container
pair registries and rejects a shared provider inbox even when their databases
are separate. Creation cannot adopt an inbox already registered natively.
Use a distinct inbox for the container. These are local launch checks, not a
distributed provider lease: never register that container inbox on another host
or add it to the native registry while the container runs. Status and down
remain available to recover from an invalid configuration.
