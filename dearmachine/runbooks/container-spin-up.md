# Rootless container spin-up

Use this runbook to build the Nix OCI image and exercise a clean, per-project
rootless Podman stack. It does not migrate or reuse the normal
`~/.dearmachine` tree, and it does not replace the Stage 3 host service work.
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
Stage 3 must turn this manual archive retention into a host upgrade/rollback
lifecycle.

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
to start if `dearmachine_agentmail_api_key` is absent. Secret provisioning and
rotation automation remain Stage 3 host-lifecycle work.
