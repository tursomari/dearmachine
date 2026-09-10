# Rootless container spin-up

Use this runbook to build the Nix OCI image and exercise a clean, per-project
rootless Podman stack. It does not migrate or reuse the normal
`~/.dearmachine` tree. The second exercise passes the same stack through the
Stage 3 systemd user lifecycle with a unique temporary unit.
The container boundary and tool requirements are documented in
[`../../docs/container-boundary.md`](../../docs/container-boundary.md).

## Native or container launch

Use `dearmachine up --foreground --project /absolute/repository` for the native
path after completing native setup. To select the container path, follow the
isolated environment below and use `nix run .#dearmachine-stack -- up`.
`DEARMACHINE_PROJECT_DIR` names the explicit host mount. For a tree containing
several repositories, set `DEARMACHINE_PROJECT_SUBDIR` to the intended entry-point
repository, relative to that mount. The container uses that repository as both
its working directory and its `--project` path. Native and container state and
inboxes must be separate; launch checks reject overlap.

## Credential-free lifecycle check

Run from the DearMachine repository root. Every durable writable location is
placed beneath one private temporary root; the wrapper's `test` mode never
contacts AgentMail or launches machtiani, Agent Manager, or a backend.

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

Confirm `health` prints `healthy`, `exec` prints `Usage: dearmachine <command>`, the
logs contain `DearMachine credential-free Compose test service ready`, and
`down` removes the test container. Before removing the exact `runtime_root`,
verify it is the private directory created above, is owned by the current user,
and is not a symlink. Never point these variables at `~/.dearmachine`.

## Credentialed Live Scenario Evaluations (LSEs)

This runbook owns the Podman mechanics, not live-mail protocol setup. Run every
credentialed LSE through the canonical containerized production path in
[`testing/temporary-instance.md`](testing/temporary-instance.md). That path
defines the scratch HOME/XDG roots, unique Compose project, external Podman
secret, mounted tools and credentials, observation requirements, and exact
cleanup boundary. Do not substitute development mode or the native diagnostic
exception for a protocol that claims container or deployment-path coverage.

Use the stack wrapper—not the installed-service lifecycle helper—to observe an
ephemeral LSE. Run these commands from the same shell with the LSE's complete
isolated environment still exported:

```bash
nix run .#dearmachine-stack -- status
nix run .#dearmachine-stack -- health
nix run .#dearmachine-stack -- logs --tail 100 dearmachine
nix run .#dearmachine-stack -- containers --format '{{.ID}}'
```

`nix run .#dearmachine-container-lifecycle -- status` inspects the separately
installed `dearmachine-stack.service`; it does not report an ephemeral LSE.
Rebuild the current isolated stack with `nix run .#dearmachine-stack --
rebuild`, and confirm its selected image with `nix run .#dearmachine-stack --
image`.

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
  'DEARMACHINE_STACK_MODE=test' \
  >"$XDG_CONFIG_HOME/dearmachine/stack.env"

cd "$repository"
nix run .#container-install
nix run .#dearmachine-container-lifecycle -- health
nix run .#dearmachine-container-lifecycle -- exec dearmachine --help
nix run .#dearmachine-container-lifecycle -- logs --tail 100 |
  grep -F 'DearMachine credential-free Compose test service ready'
nix run .#dearmachine-container-lifecycle -- stop

# Give the same OCI payload a second scratch release name so the isolated
# trial exercises upgrade and rollback links without requiring another commit.
current_archive=$(readlink -f \
  "$XDG_DATA_HOME/dearmachine/image-archive/current")
trial_upgrade="$runtime_root/dearmachine-trial-upgrade.tar.gz"
cp --reflink=auto "$current_archive" "$trial_upgrade"
DEARMACHINE_IMAGE_SOURCE="$trial_upgrade" nix run .#container-upgrade
DEARMACHINE_IMAGE_SOURCE="$trial_upgrade" \
  nix run .#container-upgrade -- --rollback
nix run .#dearmachine-container-lifecycle -- health

nix run .#container-uninstall
test -z "$(nix run .#dearmachine-container-lifecycle -- \
  containers --format '{{.ID}}')"
test ! -e "$DEARMACHINE_SYSTEMD_USER_DIR/$DEARMACHINE_UNIT_NAME"
test ! -e "$XDG_DATA_HOME/dearmachine/image-archive"
test ! -e "$XDG_DATA_HOME/dearmachine/runtime"
test -z "$(systemctl --user list-unit-files "$DEARMACHINE_UNIT_NAME" \
  --no-legend 2>/dev/null)"
```

`health` must print `healthy` from the image's normal PID-file health command,
`exec` must print `Usage: dearmachine <command>`, logs must contain the ready line, and
the final stack status must contain no container. Test mode proves the real
rootless Podman, Compose, systemd-user, mount, PID-health, and cleanup paths; it
does not authenticate AgentMail or execute a backend turn. Use a separately
authorized disposable inbox for that production-only proof.

Before accepting upgrade rollback, also exercise an actually invalid archive,
not only a second filename for the same image. The repository test performs
the complete trial and verifies automatic restoration:

```bash
bash tests/nix/test-host-podman-integration.sh
```

Inspect the exact scratch root before deleting it: it must be the
mode-`0700`, current-user-owned, non-symlink directory created above. If a
command fails, run `nix run .#container-uninstall` with the same exported variables
before leaving the unique unit behind.
