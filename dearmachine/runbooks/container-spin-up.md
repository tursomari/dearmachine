# Rootless container spin-up

Use this runbook to build the Nix OCI image and exercise a clean, per-project
rootless Podman stack. It does not migrate or reuse the normal
`~/.dearmachine` tree. The second exercise passes the same stack through the
Stage 3 systemd user lifecycle with a unique temporary unit.
The container boundary and tool requirements are documented in
[`../../docs/container-boundary.md`](../../docs/container-boundary.md).

## Credential-free lifecycle check

Run from the DearMachine repository root. Every durable writable location is
placed beneath one private temporary root; the wrapper's `test` mode never
contacts AgentMail or launches mct-agent, Agent Manager, or a backend.

```bash
runtime_root=$(mktemp -d -t dearmachine-container.XXXXXXXX)
chmod 0700 "$runtime_root"

export DEARMACHINE_STATE_DIR="$runtime_root/state"
export DEARMACHINE_CACHE_DIR="$runtime_root/cache"
export DEARMACHINE_CLIENT_HOME="$runtime_root/client-home"
export DEARMACHINE_PROJECT_DIR="$runtime_root/project"
export DEARMACHINE_TOOLS_DIR="$runtime_root/tools"
export DEARMACHINE_PROJECT_NAME="dearmachine-test-$$"
export DEARMACHINE_STACK_MODE=test
mkdir -p "$DEARMACHINE_CLIENT_HOME" "$DEARMACHINE_PROJECT_DIR" \
  "$DEARMACHINE_TOOLS_DIR"
chmod 0700 "$DEARMACHINE_CLIENT_HOME" "$DEARMACHINE_PROJECT_DIR" \
  "$DEARMACHINE_TOOLS_DIR"

nix flake check
nix build .#dearmachine-image --out-link "$runtime_root/dearmachine-image"
nix run .#dearmachine-stack -- run --help
nix run .#dearmachine-stack -- config
nix run .#dearmachine-stack -- up
nix run .#dearmachine-stack -- health
nix run .#dearmachine-stack -- exec dearmachine --help
nix run .#dearmachine-stack -- logs dearmachine
nix run .#dearmachine-stack -- down
```

The wrapper uses the real per-user runtime directory by default, and the
session naturally inherits its bus address so rootless Podman networking can
start. All durable client state remains isolated under `DEARMACHINE_STATE_DIR`
and the private scratch root.

Confirm `health` prints `healthy`, `exec` prints `Usage of dearmachine`, the
logs contain `DearMachine credential-free Compose test service ready`, and
`down` removes the test container. Before removing the exact `runtime_root`,
verify it is the private directory created above, is owned by the current user,
and is not a symlink. Never point these variables at `~/.dearmachine`.

## Functional disposable client

For a real temporary inbox exercise, repeat the isolation setup with
`DEARMACHINE_STACK_MODE=development`. Create a self-contained Git checkout at
`DEARMACHINE_PROJECT_DIR`. Put Linux-compatible `mct-agent`, `agent-manager`,
and every configured backend executable or symlink in
`DEARMACHINE_TOOLS_DIR`; Nix-store symlinks work because `/nix/store` is
mounted read-only. Configure and authenticate those tools only under
`DEARMACHINE_CLIENT_HOME`.

Rebuild and replace a running disposable stack from the current source with:

```bash
nix run .#dearmachine-stack -- rebuild
```

`nix run` rebuilds the wrapper and its image archive when the source changes;
the wrapper loads `localhost/dearmachine:nix` and force-recreates the Compose
service. For rollback, retain a previously built image archive, set both
`DEARMACHINE_IMAGE_ARCHIVE` and `DEARMACHINE_IMAGE` to that archive and its
embedded tag, then run `dearmachine-stack rebuild`. Confirm the selected image
with `nix run .#dearmachine-stack -- image` before resuming a disposable inbox.
The host lifecycle below provides the supported archive retention and
rollback behavior.

Write the backend selection to
`$DEARMACHINE_CLIENT_HOME/.dearmachine/config/dearmachine.toml`, for example:

```toml
version = 1
backends = ["forge", "codex"]
```

Then export, without printing, the API key for a dedicated temporary inbox and
set its exact ID:

```bash
export AGENTMAIL_API_KEY
export DEARMACHINE_INBOX_ID=<temporary-inbox-id>
export DEARMACHINE_STACK_MODE=development
nix run .#dearmachine-stack -- up
nix run .#dearmachine-stack -- health
nix run .#dearmachine-stack -- logs --follow dearmachine
```

Apply the provisioning, allow-list, observation, and teardown rules in
[`testing/temporary-instance.md`](testing/temporary-instance.md). In
particular, never use the normal inbox, never run a competing poller, and never
remove the protected pre-existing Gmail allow-list entry. Stop with
`nix run .#dearmachine-stack -- down` before deleting only the verified
disposable inbox, mct store, and runtime root.

For production mode, create the external Podman secret in the same isolated
Podman context and set `DEARMACHINE_STACK_MODE=production`. The wrapper refuses
to start if `dearmachine_agentmail_api_key` is absent. Provision and rotate it
with `nix run .#host-secrets`; plaintext never enters Compose or the Nix store.

## Isolated systemd user lifecycle

This exercise uses a unique unit name and scratch HOME/XDG/state roots. The
only live user-manager mutation is that uniquely named unit; uninstall removes
it. Never use `dearmachine-stack.service` for this trial, and never point any
scratch variable at the normal `~/.dearmachine` tree.

```bash
repository=$PWD
operator_unit_dir="$(systemd-path user-configuration)/systemd/user"
runtime_root=$(mktemp -d -t dearmachine-host-trial.XXXXXXXX)
chmod 0700 "$runtime_root"

export HOME="$runtime_root/home"
export XDG_DATA_HOME="$runtime_root/xdg-data"
export XDG_CONFIG_HOME="$runtime_root/xdg-config"
export XDG_STATE_HOME="$runtime_root/xdg-state"
export XDG_CACHE_HOME="$runtime_root/xdg-cache"
export DEARMACHINE_SYSTEMD_USER_DIR="$operator_unit_dir"
export DEARMACHINE_UNIT_NAME="dearmachine-test-$RANDOM.service"
export DEARMACHINE_PROJECT_DIR="$runtime_root/project"
export DEARMACHINE_PROJECT_NAME=dearmachine
export DEARMACHINE_STACK_MODE=test
export NIX_CONFIG='experimental-features = nix-command flakes'

install -d -m 0700 \
  "$HOME" "$XDG_CONFIG_HOME/dearmachine" "$DEARMACHINE_PROJECT_DIR"
install -m 0600 /dev/null "$XDG_CONFIG_HOME/dearmachine/stack.env"
printf '%s\n' \
  "DEARMACHINE_PROJECT_DIR=$DEARMACHINE_PROJECT_DIR" \
  'DEARMACHINE_INBOX_ID=credential-free-test' \
  'DEARMACHINE_STACK_MODE=test' \
  >"$XDG_CONFIG_HOME/dearmachine/stack.env"

cd "$repository"
nix run .#host-install
nix run .#dearmachine-host-lifecycle -- health
nix run .#dearmachine-host-lifecycle -- exec dearmachine --help
nix run .#dearmachine-host-lifecycle -- logs --tail 100 |
  grep -F 'DearMachine credential-free Compose test service ready'
nix run .#dearmachine-host-lifecycle -- stop

# Give the same OCI payload a second scratch release name so the isolated
# trial exercises upgrade and rollback links without requiring another commit.
current_archive=$(readlink -f \
  "$XDG_DATA_HOME/dearmachine/image-archive/current")
trial_upgrade="$runtime_root/dearmachine-trial-upgrade.tar.gz"
cp --reflink=auto "$current_archive" "$trial_upgrade"
DEARMACHINE_IMAGE_SOURCE="$trial_upgrade" nix run .#host-upgrade
DEARMACHINE_IMAGE_SOURCE="$trial_upgrade" \
  nix run .#host-upgrade -- --rollback
nix run .#dearmachine-host-lifecycle -- health

nix run .#host-uninstall
test -z "$(nix run .#dearmachine-host-lifecycle -- \
  containers --format '{{.ID}}')"
test ! -e "$DEARMACHINE_SYSTEMD_USER_DIR/$DEARMACHINE_UNIT_NAME"
test ! -e "$XDG_DATA_HOME/dearmachine/image-archive"
test ! -e "$XDG_DATA_HOME/dearmachine/runtime"
test -z "$(systemctl --user list-unit-files "$DEARMACHINE_UNIT_NAME" \
  --no-legend 2>/dev/null)"
```

`health` must print `healthy`, `exec` must print `Usage of dearmachine`, logs
must contain the ready line, and the final stack status must contain no
container. Inspect the exact scratch root before deleting it: it must be the
mode-`0700`, current-user-owned, non-symlink directory created above. If a
command fails, run `nix run .#host-uninstall` with the same exported variables
before leaving the unique unit behind.
