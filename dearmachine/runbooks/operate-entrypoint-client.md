# Operate the optional DearMachine container stack

The optional Linux container installation uses the systemd **user** service
`dearmachine-stack.service`. It drives the production Podman Compose overlay
through the pinned `dearmachine-stack` wrapper. Do not launch a direct
`dearmachine` process or transient `dearmachine.service` alongside it.

## Status and health

```bash
systemctl --user show dearmachine-stack.service \
  -p ActiveState -p SubState -p Result -p ExecMainStatus
nix run .#dearmachine-container-lifecycle -- status
nix run .#dearmachine-container-lifecycle -- health
nix run .#dearmachine-container-lifecycle -- logs --tail 100
```

The user unit is healthy only when systemd reports `active/exited` for its
successful oneshot activation, the container health command prints `healthy`,
and recent logs show `DearMachine Client started` followed by verbose `poll:`
lines without configuration, authentication, or subprocess errors. Compose's
`restart: unless-stopped` handles a client-container process death after the
oneshot unit has completed.

Inspect the mounted executable boundary without exposing credentials:

```bash
nix run .#dearmachine-container-lifecycle -- exec dearmachine --help
nix run .#dearmachine-container-lifecycle -- exec sh -c \
  'command -v machtiani && command -v agent-manager'
```

## Start, stop, and restart

```bash
nix run .#dearmachine-container-lifecycle -- stop
nix run .#dearmachine-container-lifecycle -- start
nix run .#dearmachine-container-lifecycle -- restart
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
machtiani session, Agent Manager ticket, and logs.

## Monitor active email sessions

While work is active, take this snapshot about every two minutes. Back off to
five minutes when idle, and use a bounded deadline rather than monitoring
silently forever:

```bash
date '+%Y-%m-%d %H:%M:%S %Z'
systemctl --user show dearmachine-stack.service \
  -p ActiveState -p SubState -p Result -p NRestarts
nix run .#dearmachine-container-lifecycle -- containers \
  --format 'table {{.ID}}\t{{.Status}}\t{{.Names}}'

sqlite3 -readonly "$HOME/.dearmachine/state/dearmachine.db" '
  SELECT p.message_id, p.thread_id, t.session_id, p.sequence, p.state,
         p.result_kind, p.created_at, p.updated_at
    FROM pending_messages AS p
    JOIN thread_sessions AS t USING (thread_id)
   ORDER BY p.created_at;
  SELECT COUNT(*) AS processed_total,
         MAX(processed_at) AS last_processed_at
    FROM processed_messages;
'

for backend in codex forge; do
  nix run .#dearmachine-container-lifecycle -- exec \
    agent-manager worker "$backend" status || true
done
nix run .#dearmachine-container-lifecycle -- logs --tail 80
```

For a pending row with a session ID, inspect the exact session from the mounted
project rather than inferring progress from the database timestamp:

```bash
nix run .#dearmachine-container-lifecycle -- exec \
  machtiani session show <session-id> --json
```

- Healthy processing has one pending row, a corresponding machtiani session,
  and advancing trajectory or ticket activity.
- Healthy idle has no pending row and logs polls at approximately the configured
  interval.
- Investigate when the unit/container is unhealthy, restarts increase, a
  pending row has no live session, a worker ticket crashes, or trajectory and
  process evidence both stop advancing.

## Stop gracefully and verify shutdown

Capture the monitoring snapshot first, then stop through the user service:

```bash
nix run .#dearmachine-container-lifecycle -- stop
test -z "$(nix run .#dearmachine-container-lifecycle -- \
  containers --format '{{.ID}}')"
test ! -e "$HOME/.dearmachine/run/dearmachine.pid"
```

If shutdown fails, capture the exact unit, container, PID, pending row, session,
ticket, and logs before considering a targeted forced stop. Never kill by a
broad process name or kill an individual worker first; either action can leave
durable message state ambiguous.

## Abandon one stuck follow-up

Use this recovery only when the operator explicitly intends to suppress one
specific already-running follow-up. Restarting is insufficient because a
`running` pending row is durable; ordinary `inbox skip` also rejects committed
in-progress history.

1. Record the exact message, thread, session, sequence, processes, trajectory,
   ticket, and recent logs using the monitoring snapshot above.
2. Stop the stack and prove the container and PID file are absent.
3. Checkpoint and make a private SQLite backup. Do not copy a live WAL database
   with a plain file copy.
4. Run `inbox abandon` against the exact message and host paths:

```bash
database="$HOME/.dearmachine/state/dearmachine.db"
backup="$HOME/.local/state/dearmachine-abandon-backups/$(date -u +%Y%m%dT%H%M%SZ).db"
message_id='<exact-message-id>'

install -d -m 0700 "$(dirname "$backup")"
sqlite3 "$database" 'PRAGMA wal_checkpoint(TRUNCATE);'
sqlite3 "$database" ".backup '$backup'"
chmod 0600 "$backup"

nix run .#dearmachine -- inbox abandon \
  --db "$database" \
  --pidfile "$HOME/.dearmachine/run/dearmachine.pid" \
  --project <exact-host-project-path> \
  --agent-bin "$HOME/.local/share/dearmachine/tools/machtiani" \
  --reason 'operator abandoned stuck follow-up' \
  "$message_id"
```

The command must reject a live session or a row without a clean checkpoint.
Before restarting, verify the exact message is locally skipped, its pending row
is gone, and the thread retains its prior committed sequence under the expected
replacement session. Keep the backup until a later follow-up in that thread
completes successfully.

## Restart and recovery verification

After an ordinary restart, confirm the previous pending row is recovered under
one session and produces at most one eventual reply:

```bash
nix run .#dearmachine-container-lifecycle -- start
nix run .#dearmachine-container-lifecycle -- health
nix run .#dearmachine-container-lifecycle -- logs --tail 100
```

Do not locally skip unread mail merely because the service is restarting.
Skipping and abandonment are separate inbox decisions requiring explicit
operator intent.

## Configuration and secret changes

Non-secret project/inbox settings live in the mode-`0600` file
`~/.config/dearmachine/stack.env`. The lifecycle owns storage, archive,
project-name, canonical client-state, `~/.machtiani`, and tools-directory
variables; `stack.env` may not override them. After an approved non-secret
change:

```bash
nix run .#dearmachine-container-lifecycle -- restart
```

Never put the AgentMail API key in that file or an environment variable.
Rotate it through the external Podman secret:

```bash
nix run .#container-secrets -- rotate --file <mode-0600-secret-file>
```

Install, upgrade, restart, and migration do not authorize inbox mutation or
removal of any protected paired address from `DEARMACHINE_ALLOW`.
