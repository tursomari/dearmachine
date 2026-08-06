# DearMachine Device Client and Agent Manager

This module proves the Device Client alpha happy path:

1. Poll an AgentMail inbox through the AgentMail Go SDK.
2. Map AgentMail thread IDs to durable mct-agent session IDs in SQLite.
3. Invoke mct-agent with a preallocated session ID for new threads or
   `--session-id` for existing threads.
4. Inspect `mct-agent session show --json`.
5. Send either the final answer or an AskUser clarification as an AgentMail
   reply.

The Device Client is intentionally single-threaded. It does not implement retries,
preemption, attachments, sandbox policy, or the production transport
abstraction.

## Run

SQLite uses `go-sqlite3`, so source builds require CGO and
a C compiler; the resulting binary has no separate SQLite runtime dependency.

Configure the agent backends first. Configuration is stored at
`~/.dearmachine/config/device-client.toml`. Repeat `--backend` to approve more
than one backend and set their priority order:

```bash
cd device-client
go run ./cmd/device-client setup-agents \
  --backend codex \
  --backend forge
```

The setup command verifies that each requested backend is currently on `PATH`,
asks for confirmation, and writes:

```toml
version = 1
backends = ["codex","forge"]
```

`backends` is an approved list in priority order. Device Client validates and
loads the complete list at startup, then passes that immutable snapshot to each
managed mct-agent run. Restart Device Client after changing the configuration.
Agent Manager currently supports `codex` and `forge`. A version-1 config using
the former `forgecode` identifier is accepted and normalized to `forge` in
memory; rerun `setup-agents` to rewrite it canonically. A config naming any
other unsupported backend is rejected with instructions to rerun setup.

The agent-managed coordinator discovers the approved order with
`agent-manager backend list`, functionally probes candidates in order with
`agent-manager backend health <name>`, and explicitly targets the first healthy
one with `agent-manager ticket send --backend <name> ...`. Health probing asks
the backend to write and then removes a temporary file in the project, so it
tests actual tool execution rather than only checking `PATH`. If every approved
backend fails its probe, the coordinator reports the failure and asks for
direction; it never falls back to modifying project files directly.

Build the standalone manager at its private path:

```bash
cd device-client
mkdir -p ~/.dearmachine/agent-manager
go build -o ~/.dearmachine/agent-manager/agent-manager ./cmd/agent-manager
```

```bash
cd device-client
export AGENTMAIL_API_KEY=am_your_key

go run ./cmd/device-client \
  --inbox-id your-inbox-id \
  --project ~/.dearmachine/entrypoint/main \
  --entry-point-repo ~/.dearmachine/entrypoint/main \
  --pidfile ~/.dearmachine/run/device-client.pid
```

The default SQLite state database is
`~/.dearmachine/state/device-client.db`. Device Client creates its state
directory with mode `0700` and creates or tightens the database to mode `0600`.
Pass `--db /absolute/path/to/device-client.db` only when an isolated instance
needs a separate store.

The daemon runs `mct-agent sync` before polling begins and again immediately
before each `mct-agent run`. Runs use `--mode agent-managed` and receive the
approved order in `DEARMACHINE_BACKENDS` plus the absolute `AGENT_MANAGER_PATH`.
Pass `--model your-model-alias` to override the project's configured default
model; when omitted, no model flag is forwarded.

For each inbound email, Device Client passes the sender/thread metadata and the
newly authored text reported by AgentMail. Follow-ups resume the mapped
`mct-agent` session with `--session-id`; that persisted session owns prior
conversation context, so Device Client does not replay the email thread.

The first poll runs immediately. Later polls start 60 seconds after the prior
poll completes. Override that with `--poll-interval`; use `--once` for a single
poll.

## Locally skip inbox messages

Stop Device Client before changing its local inbox decisions. To suppress the
exact snapshot of messages that are currently unread without changing
AgentMail, run:

```bash
device-client inbox skip \
  --current \
  --inbox-id your-inbox-id \
  --project ~/.dearmachine/entrypoint/main \
  --pidfile ~/.dearmachine/run/device-client.pid \
  --reason "stale before restart"
```

Pass one or more message IDs instead of `--current` to select individual
messages. `--current` records only the IDs returned by that read-only snapshot;
mail arriving afterward remains eligible. Use `--db` when the client uses a
non-default state database.

Inspect and reverse local decisions with:

```bash
device-client inbox skipped
device-client inbox skipped --json
device-client inbox unskip <message-id>
```

These commands never delete messages, change labels, mark messages processed,
or send replies through AgentMail. A skipped unread message therefore remains
visible in each remote poll, but Device Client recognizes its local ID and does
nothing. After `unskip`, the next poll handles the message normally.

Skipping also removes a provisional first-message queue record left by an
interrupted run and asks mct-agent to delete that abandoned session. If session
cleanup fails, the message remains safely skipped and the command reports the
cleanup error. An already-running follow-up in a session with committed history
is rejected because silently removing it could leave that continuing session
with ambiguous partial context.

Every inbox mutation checks the supplied PID file, or the normal
`~/.dearmachine/run/device-client.pid` by default, and refuses to proceed when
that PID is alive. This protects the normal installed invocation and isolated
instances that follow the documented PID-file convention; operators must still
confirm that no client launched without that PID file is polling the database.

Long-running mode logs successful startup and graceful-shutdown counts to
stderr. Pass `--verbose` to also log the unread-message count for every poll;
idle polls remain silent by default.

Pass `--pidfile /path/to/device-client.pid` when a process supervisor needs a
PID file. Device Client creates missing parent directories after the initial
`mct-agent sync`, writes its current PID before polling, and removes the file
on controlled exit. Omit the flag for the previous foreground-without-pidfile
behavior. This flag does not self-daemonize the process; supervisors that
require a returning start command must provide a background wrapper.

Set `AGENTMAIL_BASE_URL` to point the SDK at a non-production endpoint when
needed.

## Task project and entry-point repository

`--project` and `--entry-point-repo` are independent settings:

- `--project` selects the working directory and mct-agent project where email
  sessions are created. Its default is `.`, the directory from which Device
  Client was launched.
- `--entry-point-repo` selects only the repository inspected and maintained by
  entry-point documentation sync. Its default is
  `~/.dearmachine/entrypoint/main`.

Setting `--entry-point-repo` does not make a different `--project` inherit the
entry point's context, documentation, or session history. For normal installed
operation, point both settings at the initialized entry point:

```bash
device-client \
  --inbox-id your-inbox-id \
  --project ~/.dearmachine/entrypoint/main \
  --entry-point-repo ~/.dearmachine/entrypoint/main \
  --pidfile ~/.dearmachine/run/device-client.pid
```

Use different paths only for deliberate development, migration, or isolated
testing arrangements. A disposable test normally supplies its test project
through `--project` and disables real entry-point maintenance with
`--entry-point-repo ""`.

## Initialize the machine entry point

Before using entry-point session maintenance on a new machine, initialize its
user-owned repository:

```bash
device-client init \
  --entry-point-repo ~/.dearmachine/entrypoint/main \
  --mct-agent /absolute/path/to/mct-agent
```

Initialization configures local Git LFS and bootstraps the repository in two
stages. The first commit contains only a neutral machine-entry-point skeleton:
the top-level README, directory READMEs, repository rules, and the generic
maintenance prompt. mct-agent initializes the project identity and syncs that
skeleton before DearMachine-specific material exists. The second commit adds
the DearMachine architecture reference and configuration runbook, followed by
a second documentation-aware sync.

Pass `--snapshot-dir /absolute/path` to preserve the internal README at all
four sync boundaries:

- `01-before-skeleton-sync.md`
- `02-after-skeleton-sync.md`
- `03-before-dearmachine-sync.md`
- `04-after-dearmachine-sync.md`

An existing Git repository is reported as already initialized and is left
unchanged. A non-empty directory that is not a Git repository is rejected.
Failed first-time initialization leaves an explicit marker in `.git` and will
not be mistaken for a complete repository on retry.

## Entry-point session-driven sync

When `~/.dearmachine/entrypoint/main` exists, Device Client evaluates its
mct-agent sessions after every successful poll. Override the paths with
`--entry-point-repo` and `--entry-point-prompt`, or pass an empty
`--entry-point-repo` to disable the trigger.

For email sessions themselves to count toward the trigger, launch Device
Client with `--project` set to the entry-point repository. Two distinct new
threads then create two distinct mct-agent sessions. After two sessions newer
than the effective review boundary, the trigger:

1. forks the most recently updated session;
2. resumes the fork with the entry-point update prompt;
3. deletes the temporary fork;
4. runs `mct-agent sync --include-docs`; and
5. records the newest reviewed source session in
   `state/sync-trigger.json`.

The effective boundary is the newer of that local checkpoint and the project
commit stored by mct-agent's internal-README sync state. It is deliberately not
the repository's latest arbitrary commit. The checkpoint prevents an
immediate repeat when the review correctly decides that no documentation
change is warranted. It is written only after the complete pipeline succeeds,
so failed runs remain eligible for retry. A failed forked run is cleaned up
before the error is reported.

The `state/` checkpoint is host-local and should remain ignored by Git. The
internal-README marker remains owned by mct-agent under the UUID project store
in `~/.machtiani/`.

## Agent Manager help

Run `agent-manager --help` or `agent-manager help` for the top-level command
menu. Help is also available for every command group and subcommand:

```bash
agent-manager backend --help
agent-manager backend health --help
agent-manager help ticket
agent-manager help ticket send
```

`help`, `-h`, and `--help` are accepted at their applicable command level.
Syntax errors name the exact help command for that context. For example, an
incomplete health command reports:

```text
agent-manager: backend health requires <name>
Run "agent-manager backend health --help" for usage.
```

Help is resolved before backend configuration or execution, so asking for
health help never launches a probe.

## Verify

For a recoverable local reset and a rebuild from the current default-branch
HEAD, follow
[`UNINSTALL_REINSTALL.md`](./UNINSTALL_REINSTALL.md). The runbook preserves the
old installation until the fresh instance passes verification and does not
automatically import remote history into a privacy-cleaned repository.

For an opt-in live smoke test that does not share the normal Device Client's
inbox or runtime state, follow
[`DISPOSABLE_INSTANCE.md`](./DISPOSABLE_INSTANCE.md). The ordered Forge,
Codex, and fallback protocol remains in
[`LIVE_BACKEND_TESTING.md`](./LIVE_BACKEND_TESTING.md).

```bash
go test ./...
go build -o /tmp/device-client-spike ./cmd/device-client
go build -o /tmp/agent-manager ./cmd/agent-manager
```
