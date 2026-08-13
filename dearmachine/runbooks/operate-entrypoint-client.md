# Operate the DearMachine user stack

The normal installation is the systemd **user** service
`dearmachine-stack.service`. It drives the production Podman Compose overlay
through the pinned `dearmachine-stack` wrapper. Do not launch a direct
`dearmachine` process or transient `dearmachine.service` alongside it.

## Status and health

```bash
systemctl --user show dearmachine-stack.service \
  -p ActiveState -p SubState -p Result -p ExecMainStatus
nix run .#dearmachine-host-lifecycle -- status
nix run .#dearmachine-host-lifecycle -- health
nix run .#dearmachine-host-lifecycle -- logs --tail 100
```

The user unit is healthy only when systemd reports `active/exited` for its
successful oneshot activation, the container health command prints `healthy`,
and recent logs show `DearMachine Client started` followed by verbose `poll:`
lines without configuration, authentication, or subprocess errors. Compose's
`restart: unless-stopped` handles a client-container process death after the
oneshot unit has completed.

Inspect the mounted executable boundary without exposing credentials:

```bash
nix run .#dearmachine-host-lifecycle -- exec dearmachine --help
nix run .#dearmachine-host-lifecycle -- exec sh -c \
  'command -v mct-agent && command -v agent-manager'
```

## Start, stop, and restart

```bash
nix run .#dearmachine-host-lifecycle -- stop
nix run .#dearmachine-host-lifecycle -- start
nix run .#dearmachine-host-lifecycle -- restart
```

Stop through the user service so `dearmachine-stack down` removes the Compose
container cleanly. Never kill by a broad process name. If shutdown fails,
capture `systemctl --user status`, container logs, the exact PID/container ID,
and the current pending row before considering a targeted forced stop.

## Database monitoring

Read the active database only when operational diagnosis is authorized. Never
start a second poller against the same inbox or state:

```bash
sqlite3 -readonly "$HOME/.dearmachine/state/dearmachine.db" '
  SELECT COUNT(*) AS pending FROM pending_messages;
  SELECT COUNT(*) AS processed FROM processed_messages;
  SELECT COUNT(*) AS threads FROM thread_sessions;
'
```

`updated_at` records state transitions, not a heartbeat. During an agent turn,
corroborate an old timestamp with the service, exact container process,
mct-agent session, Agent Manager ticket, and logs.

## Configuration and secret changes

Non-secret project/inbox settings live in the mode-`0600` file
`~/.config/dearmachine/stack.env`. The lifecycle owns storage, archive,
project-name, canonical client-state, `~/.machtiani`, and tools-directory
variables; `stack.env` may not override them. After an approved non-secret
change:

```bash
nix run .#dearmachine-host-lifecycle -- restart
```

Never put the AgentMail API key in that file or an environment variable.
Rotate it through the external Podman secret:

```bash
nix run .#host-secrets -- rotate --file <mode-0600-secret-file>
```

Install, upgrade, restart, and migration do not authorize inbox mutation or
removal of any protected AgentMail allow-list entry.
