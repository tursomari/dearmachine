# Runbook: Operate the DearMachine Device Client

## Purpose

Launch, monitor, stop, and restart the normal DearMachine Device Client on the
current development machine. The default installed service is entry-point-only:
both inbound email sessions and entry-point maintenance run in
`/home/david/.dearmachine/entrypoint/main`.

This project-owned runbook operates the normal inbox and state. Use the
[`testing/disposable-instance.md`](./testing/disposable-instance.md) protocol
for live testing; never start a second client against the normal inbox.
Whether this runbook should later be seeded into an initialized entry point is
a separate decision.

## Normal installation

- Inbox: `david-8699@agentmail.to`
- Device Client: `/home/david/.local/bin/device-client`
- mct-agent: `/home/david/.local/bin/mct-agent`
- Agent Manager: `/home/david/.dearmachine/agent-manager/agent-manager`
- Entry point: `/home/david/.dearmachine/entrypoint/main`
- Configuration: `/home/david/.dearmachine/config/device-client.toml`
- State database: `/home/david/.dearmachine/state/device-client.db`
- PID file: `/home/david/.dearmachine/run/device-client.pid`
- Log: `/home/david/.dearmachine/log/device-client.log`
- Credential file:
  `/home/david/projects/DearMachine/.secrets/agentmail-api-key.txt`
- User service: `dearmachine-device-client.service`

The service is currently transient: it survives terminal closure and restarts
after failure, but it is not enabled across logout or reboot. Because it uses
`--collect`, systemd may remove the unit after it stops.

## Guardrails

- Never print, log, or put the AgentMail API key directly in a command line.
- Never run two clients against the same inbox or database.
- Do not delete a PID file until its recorded PID has been proved absent.
- Do not skip, mark read, or otherwise change inbox messages during ordinary
  launch, monitoring, stop, or restart.
- Stop through systemd so the client can cancel children, close state, remove
  its PID file, and log a graceful shutdown.
- Preserve the complete service `PATH`. The Device Client, mct-agent,
  shell-agent, and managed workers inherit it.
- Treat a quiet log during an active mct-agent or maintenance run as expected;
  the current client processes globally and does not poll concurrently.

## Preflight

Run these checks from an ordinary user shell:

```bash
dm_home=/home/david/.dearmachine
dm_entrypoint=$dm_home/entrypoint/main
dm_config=$dm_home/config/device-client.toml
dm_db=$dm_home/state/device-client.db
dm_pidfile=$dm_home/run/device-client.pid
dm_log=$dm_home/log/device-client.log
dm_key_file=/home/david/projects/DearMachine/.secrets/agentmail-api-key.txt
dm_node_bin=$(dirname "$(command -v node)")
dm_path=/home/david/.local/bin:/home/david/.nix-profile/bin:$dm_node_bin:/home/david/go/bin:/usr/local/bin:/usr/bin:/bin
dm_backends='["codex","forge"]'

test -d "$dm_entrypoint"
test -r "$dm_config"
test -r "$dm_key_file"
test "$(stat -c %a "$dm_key_file")" = 600
test -x /home/david/.local/bin/device-client
test -x /home/david/.local/bin/mct-agent
test -x /home/david/.dearmachine/agent-manager/agent-manager

env -i HOME=/home/david PATH="$dm_path" /bin/sh -c '
  for tool in device-client mct-agent rg go node codex forge sqlite3 jq; do
    command -v "$tool" || exit 1
  done
'

PATH="$dm_path" DEARMACHINE_HOME="$dm_home" \
DEARMACHINE_BACKENDS="$dm_backends" \
  /home/david/.dearmachine/agent-manager/agent-manager backend list
```

If any command fails, fix the installation or `PATH` before launch. Do not
silently omit a missing directory. In particular,
`/home/david/.nix-profile/bin` supplies both `rg` and `go`.

Check for an existing client:

```bash
systemctl --user show dearmachine-device-client.service \
  -p ActiveState -p SubState -p MainPID 2>/dev/null || true

if test -f /home/david/.dearmachine/run/device-client.pid; then
  dm_recorded_pid=$(sed -n '1p' /home/david/.dearmachine/run/device-client.pid)
  ps -o pid,ppid,lstart,etime,stat,comm -p "$dm_recorded_pid"
fi
```

If the unit or recorded PID is live, monitor or stop that instance; do not
launch another. If the PID file is stale, verify the exact PID is absent before
removing only that PID file.

## Launch

The launch command deliberately sets a stable service `PATH`. It loads the API
key inside the service without displaying it or placing its value in systemd's
unit properties.

```bash
dm_home=/home/david/.dearmachine
dm_entrypoint=$dm_home/entrypoint/main
dm_log=$dm_home/log/device-client.log
dm_node_bin=$(dirname "$(command -v node)")
dm_path=/home/david/.local/bin:/home/david/.nix-profile/bin:$dm_node_bin:/home/david/go/bin:/usr/local/bin:/usr/bin:/bin

install -d -m 0700 \
  /home/david/.dearmachine/log \
  /home/david/.dearmachine/run \
  /home/david/.dearmachine/state
touch "$dm_log"
chmod 0600 "$dm_log"

systemd-run --user \
  --unit=dearmachine-device-client \
  --collect \
  --property=Restart=on-failure \
  --property=RestartSec=5s \
  --property=KillMode=control-group \
  --property=WorkingDirectory="$dm_entrypoint" \
  --property=StandardOutput=append:"$dm_log" \
  --property=StandardError=append:"$dm_log" \
  --setenv=PATH="$dm_path" \
  --setenv=DEARMACHINE_HOME="$dm_home" \
  /bin/bash -c '
    set -euo pipefail
    dm_agentmail_key=$(tr -d "\r\n" < /home/david/projects/DearMachine/.secrets/agentmail-api-key.txt)
    test -n "$dm_agentmail_key"
    export AGENTMAIL_API_KEY="$dm_agentmail_key"
    unset dm_agentmail_key
    exec /home/david/.local/bin/device-client \
      --inbox-id david-8699@agentmail.to \
      --project /home/david/.dearmachine/entrypoint/main \
      --entry-point-repo /home/david/.dearmachine/entrypoint/main \
      --entry-point-prompt /home/david/.dearmachine/entrypoint/main/documentation/update-prompt-template.md \
      --config /home/david/.dearmachine/config/device-client.toml \
      --agent-manager /home/david/.dearmachine/agent-manager/agent-manager \
      --mct-agent /home/david/.local/bin/mct-agent \
      --db /home/david/.dearmachine/state/device-client.db \
      --pidfile /home/david/.dearmachine/run/device-client.pid \
      --poll-interval 60s \
      --verbose
  '
```

Both `--project` and `--entry-point-repo` intentionally name the entry point.
Do not change `--project` to the DearMachine source repository for normal
operation.

## Verify startup

Startup performs `mct-agent sync` before creating the PID file, so allow that
bounded startup work to finish. Do not infer failure merely because the PID
file is not immediate.

```bash
systemctl --user show dearmachine-device-client.service \
  -p ActiveState -p SubState -p MainPID -p NRestarts \
  -p ExecMainStartTimestamp

dm_main_pid=$(systemctl --user show dearmachine-device-client.service \
  -p MainPID --value)
dm_recorded_pid=$(sed -n '1p' /home/david/.dearmachine/run/device-client.pid)
test "$dm_main_pid" = "$dm_recorded_pid"
kill -0 "$dm_main_pid"

dm_service_path=$(tr '\0' '\n' < "/proc/$dm_main_pid/environ" |
  sed -n 's/^PATH=//p')
env -i HOME=/home/david PATH="$dm_service_path" /bin/sh -c '
  for tool in device-client mct-agent rg go node codex forge; do
    command -v "$tool" || exit 1
  done
'

tail -n 30 /home/david/.dearmachine/log/device-client.log
```

Startup is verified only when:

- systemd reports `active/running` with a nonzero main PID;
- the PID file matches that main PID;
- the service environment resolves all required tools, including `rg` and
  `go`;
- the log contains `Device Client started`; and
- the first poll or active email-processing state has no configuration,
  authentication, or subprocess error.

## Monitor every two minutes

While work is active, run the following snapshot approximately every two
minutes and report state changes. Back off to five minutes when idle. Use a
bounded deadline for expected work; do not monitor silently forever.

```bash
date '+%Y-%m-%d %H:%M:%S %Z'

systemctl --user show dearmachine-device-client.service \
  -p ActiveState -p SubState -p MainPID -p NRestarts \
  -p ExecMainStartTimestamp 2>/dev/null || true

dm_main_pid=$(systemctl --user show dearmachine-device-client.service \
  -p MainPID --value 2>/dev/null || true)
if test -n "$dm_main_pid" && test "$dm_main_pid" != 0 && kill -0 "$dm_main_pid" 2>/dev/null; then
  ps -o pid,ppid,lstart,etime,stat,%cpu,%mem,comm -p "$dm_main_pid"
  ps --ppid "$dm_main_pid" -o pid,ppid,lstart,etime,stat,%cpu,%mem,comm
fi

sqlite3 -header -column /home/david/.dearmachine/state/device-client.db '
  SELECT p.message_id,
         p.thread_id,
         t.session_id,
         p.sequence,
         p.state,
         p.result_kind,
         p.created_at,
         p.updated_at
    FROM pending_messages AS p
    JOIN thread_sessions AS t USING (thread_id)
   ORDER BY p.created_at;

  SELECT COUNT(*) AS processed_total,
         MAX(processed_at) AS last_processed_at
    FROM processed_messages;
'

for dm_backend in codex forge; do
  printf '[%s]\n' "$dm_backend"
  PATH=/home/david/.local/bin:/home/david/.nix-profile/bin:/usr/local/bin:/usr/bin:/bin \
  DEARMACHINE_HOME=/home/david/.dearmachine \
  DEARMACHINE_BACKENDS='["codex","forge"]' \
    /home/david/.dearmachine/agent-manager/agent-manager \
      worker "$dm_backend" status
done

tail -n 40 /home/david/.dearmachine/log/device-client.log
```

When a pending row supplies a session ID, inspect that session read-only from
the entry point:

```bash
cd /home/david/.dearmachine/entrypoint/main
/home/david/.local/bin/mct-agent session show <session-id> --json
```

Interpret the snapshot as follows:

- **Healthy and idle:** service and PID agree, no pending row exists, no child
  mct-agent is running, and verbose logs show polls at roughly the configured
  interval.
- **Healthy and processing email:** a pending row is `running`, an mct-agent
  child is alive, and its session or trajectory continues to progress. Polling
  pauses until the current message finishes.
- **Healthy and maintaining the entry point:** an mct-agent child is running
  the update prompt after email processing. Polling also pauses during this
  orchestration.
- **Needs investigation:** the unit is failed, the PID does not match, restarts
  increase, a pending row has no corresponding live child, a ticket is
  `crashed`, errors repeat, or more than two polling intervals pass while there
  is neither pending work nor a maintenance child.

Database `updated_at` records state transitions, not a heartbeat. A long-running
agent can be healthy even when that value is old; corroborate it with the
process, mct session, trajectory, and ticket state.

## Stop gracefully

```bash
dm_main_pid=$(systemctl --user show dearmachine-device-client.service \
  -p MainPID --value 2>/dev/null || true)

systemctl --user stop dearmachine-device-client.service

for dm_attempt in $(seq 1 30); do
  if ! systemctl --user is-active --quiet dearmachine-device-client.service; then
    break
  fi
  sleep 1
done

if test -n "$dm_main_pid" && test "$dm_main_pid" != 0; then
  ! kill -0 "$dm_main_pid" 2>/dev/null
fi
test ! -e /home/david/.dearmachine/run/device-client.pid
tail -n 20 /home/david/.dearmachine/log/device-client.log
```

A normal stop logs `Device Client shutting down`, removes the PID file, and
leaves no child process in the unit's control group. With a collected transient
unit, `systemctl status` may subsequently report that the unit is not found;
that is expected.

If graceful stop does not finish, capture service status, the process tree,
pending database rows, ticket state, and recent logs before considering a
forced kill. Do not kill an individual child first, because doing so can leave
the durable message state ambiguous.

## Abandon one stuck follow-up

Use this recovery only when the User explicitly intends to suppress one
already-running follow-up. Restarting alone is not enough: a `running` pending
row is durable and will be recovered on startup. Ordinary `inbox skip` also
rejects an in-progress follow-up with committed history.

1. Capture the full monitoring snapshot, including the exact message, thread,
   session, sequence, child processes, and recent trajectory state.
2. Stop the normal client gracefully and verify its PID file is absent. If the
   stuck agent launched a detached disposable process outside the unit's
   control group, identify it by its recorded temporary runtime and stop that
   isolated process separately; never match or kill by a broad name.
3. Back up the state database before the mutation.
4. Run `inbox abandon` with the exact message ID, normal database, normal PID
   file, entry-point project, and mct-agent path:

```bash
dm_message_id='<exact-message-id>'

cp --preserve=mode,timestamps \
  /home/david/.dearmachine/state/device-client.db \
  "/home/david/.dearmachine/state/device-client.db.before-abandon.$(date -u +%Y%m%dT%H%M%SZ)"

/home/david/.local/bin/device-client inbox abandon \
  --db /home/david/.dearmachine/state/device-client.db \
  --pidfile /home/david/.dearmachine/run/device-client.pid \
  --project /home/david/.dearmachine/entrypoint/main \
  --mct-agent /home/david/.local/bin/mct-agent \
  --reason "operator abandoned stuck follow-up" \
  "$dm_message_id"
```

The command requires an inactive mct session and a clean checkpoint recorded
before that follow-up started. It atomically remaps the email thread to the
checkpoint, records only the selected message as locally skipped, removes its
pending row, and deletes the partial source session. It does not query or modify
AgentMail and does not require the API key. A legacy running row created before
checkpoint support is rejected instead of guessing at a rollback boundary.

Before relaunching, verify:

```bash
/home/david/.local/bin/device-client inbox skipped \
  --db /home/david/.dearmachine/state/device-client.db

sqlite3 -header -column /home/david/.dearmachine/state/device-client.db '
  SELECT p.message_id, p.thread_id, t.session_id, p.sequence, p.state
    FROM pending_messages AS p
    JOIN thread_sessions AS t USING (thread_id)
   ORDER BY p.created_at;
'
```

Confirm the exact message appears in the local skip list, its pending row is
absent, and the thread retains its prior committed sequence under a replacement
session ID. Then relaunch and verify that later unread mail can progress. Keep
the database backup until the next follow-up in that thread completes.

## Restart

1. Record the pre-restart monitoring snapshot.
2. Stop gracefully and verify shutdown as above.
3. Repeat preflight, including the complete `PATH` check.
4. Launch and verify startup as above.
5. Confirm recovery of any pending row and exactly one eventual reply.

Do not locally skip unread mail merely because the service is restarting.
Skipping is a separate inbox decision that requires explicit intent.

## Update this runbook when

- the service becomes a persistent installed unit;
- binary, credential, config, state, entry-point, or log paths change;
- the backend list or required toolchain changes;
- polling becomes concurrent or gains a durable scheduler; or
- the client gains a first-class health/status command.
