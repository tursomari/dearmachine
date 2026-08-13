# Install the optional Linux container stack

This is the supported Linux container path: the root Nix flake builds one OCI
image, `dearmachine-stack` runs it with rootless Podman Compose, and
`dearmachine-stack.service` supervises it in the current user's systemd
manager. Native foreground execution is the portable default and is documented
in [`native-install.md`](./native-install.md). The filename is retained for
existing links; the canonical commands below use `container-*` names.

DearMachine must run as the operator, not a dedicated system user. Its agents
need that user's selected repositories, backend credentials, and
`~/.machtiani` state. The Compose boundary still mounts only the declared
project, private client home, canonical `~/.dearmachine` directories,
`~/.machtiani`, and tool directory; it does not grant a container an ambient
host-home mount.

## Prerequisites

The host needs Nix, cgroup v2, a systemd user manager, subordinate UID/GID
ranges for the current user, and capability-enabled `/usr/bin/newuidmap` and
`/usr/bin/newgidmap`. The flake supplies Podman, Podman Compose, and
`fuse-overlayfs`.

```bash
nix flake check
nix run .#dearmachine-stack -- preflight
systemctl --user show-environment >/dev/null
```

Enable user lingering if this personal agent must start before login. That is
an explicit host-administration choice; the lifecycle command never uses
`sudo`, system systemd, or a system account.

## Configure the mounted boundary

Create the non-secret service environment before installation:

```bash
install -d -m 0700 "$HOME/.config/dearmachine"
install -d -m 0700 "$HOME/.local/share/dearmachine/tools"

project_dir="$HOME/projects/<project>"
test -d "$project_dir/.git"

install -m 0600 /dev/null "$HOME/.config/dearmachine/stack.env"
printf '%s\n' \
  "DEARMACHINE_PROJECT_DIR=$project_dir" \
  'DEARMACHINE_INBOX_ID=<inbox-id>' \
  >"$HOME/.config/dearmachine/stack.env"
```

Put Linux-compatible `mct-agent` and configured backend executables or
Nix-store symlinks in
`~/.local/share/dearmachine/tools`. Authenticate backends only in the private
client home at `~/.local/share/dearmachine/client-home`. The service mounts the
selected project and the current user's `~/.machtiani` separately.

Agent Manager is built from the DearMachine source and included in the OCI
image. The flake exposes a separately pinned compatible Codex CLI as an
optional convenience for the external backend boundary:

```bash
nix build .#codex-tool --out-link \
  "$HOME/.local/share/dearmachine/tools/.roots/codex"
ln -sfn .roots/codex/bin/codex \
  "$HOME/.local/share/dearmachine/tools/codex"
```

Keep this as a Nix GC root. A backend executable merely being present is not
readiness proof; run its Agent Manager functional health probe from inside the
production container before accepting the cutover.

Never put the AgentMail key in `stack.env`. Create a mode-`0600`, user-owned
input file, synchronize it into the exact external Podman secret required by
the production Compose overlay, then remove or retain that input according to
the operator's credential policy:

```bash
secret_file="$HOME/.config/dearmachine/agentmail-api-key"
test -s "$secret_file"
test "$(stat -c %a "$secret_file")" = 600
nix run .#container-secrets -- sync --file "$secret_file"
nix run .#container-secrets -- status
```

The secret is named `dearmachine_agentmail_api_key` and appears in the
container only at `/run/secrets/dearmachine_agentmail_api_key`. Rotation
replaces the secret and restarts the user stack:

```bash
nix run .#container-secrets -- rotate --file "$secret_file"
```

## Install and verify

```bash
nix run .#container-install
systemctl --user status --no-pager dearmachine-stack.service
nix run .#dearmachine-container-lifecycle -- health
nix run .#dearmachine-container-lifecycle -- exec dearmachine --help
nix run .#dearmachine-container-lifecycle -- logs --tail 100
```

Installation writes `~/.config/systemd/user/dearmachine-stack.service`,
enables it for `default.target`, and starts it. The selected immutable archive
is copied to
`~/.local/share/dearmachine/image-archive/releases/`; `current` identifies the
active archive. Mutable Podman state stays under
`~/.local/state/dearmachine-stack`, canonical client state stays under
`~/.dearmachine`, and both survive install and upgrade.

## Upgrade and rollback

```bash
nix run .#container-upgrade
nix run .#dearmachine-container-lifecycle -- health
```

Upgrade stops the unit, unloads the old tagged image, changes the runtime and
archive links, loads the replacement through `dearmachine-stack`, and starts
the service. It retains exactly one archive and runtime rollback link. If the
new service does not start, it restores the prior links automatically. An
operator can also swap the two releases explicitly:

```bash
nix run .#container-upgrade -- --rollback
```

Neither path changes the database, project, `~/.machtiani`, backend
credentials, inbox, or AgentMail allow lists.

The hermetic lifecycle test injects a failed `systemctl start`. Before a release
is considered host-validated, also run the real rootless Podman/systemd test,
which attempts to upgrade from an invalid OCI archive and requires the prior
release to be restored and healthy:

```bash
bash tests/nix/test-host-podman-integration.sh
```

This remains credential-free. Complete production validation requires the
disposable-inbox procedure in `testing/temporary-instance.md`, including an
actual poll and configured backend turn.
