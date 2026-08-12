# DearMachine Client and Agent Manager

This module proves the DearMachine Client alpha happy path:

1. Poll an AgentMail inbox through the AgentMail Go SDK.
2. Map AgentMail thread IDs to durable mct-agent session IDs in SQLite.
3. Invoke mct-agent with a preallocated session ID for new threads or
   `--session-id` for existing threads.
4. Inspect `mct-agent session show --json`.
5. Send either the final answer or an AskUser clarification as an AgentMail
   reply.

The DearMachine Client processes independent email threads concurrently while
preserving FIFO order within each thread. It does not implement retries,
preemption, attachments, sandbox policy, or the production transport
abstraction.

## Run

SQLite uses `go-sqlite3`, so source builds require CGO and
a C compiler; the resulting binary has no separate SQLite runtime dependency.

Configure the agent backends first. Configuration is stored at
`~/.dearmachine/config/dearmachine.toml`. Repeat `--backend` to approve more
than one backend and set their priority order:

```bash
cd dearmachine
go run ./cmd/dearmachine setup-agents \
  --backend codex \
  --backend forge
```

The setup command verifies that each requested backend is currently on `PATH`,
asks for confirmation, and writes:

```toml
version = 1
backends = ["codex","forge"]
```

`backends` is an approved list in priority order. DearMachine Client validates and
loads the complete list at startup, then passes that immutable snapshot to each
managed mct-agent run. Restart DearMachine Client after changing the configuration.
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
cd dearmachine
mkdir -p ~/.dearmachine/agent-manager
go build -o ~/.dearmachine/agent-manager/agent-manager ./cmd/agent-manager
```

```bash
cd dearmachine
export AGENTMAIL_API_KEY=am_your_key

go run ./cmd/dearmachine \
  --inbox-id your-inbox-id \
  --project ~/.dearmachine/entrypoint/main \
  --entry-point-repo ~/.dearmachine/entrypoint/main \
  --maintenance-min-turns 20 \
  --pidfile ~/.dearmachine/run/dearmachine.pid
```

The default SQLite state database is
`~/.dearmachine/state/dearmachine.db`. DearMachine Client creates its state
directory with mode `0700` and creates or tightens the database to mode `0600`.
Pass `--db /absolute/path/to/dearmachine.db` only when an isolated instance
needs a separate store.

The daemon runs `mct-agent sync` before polling begins and again immediately
before each `mct-agent run`. Runs use `--mode agent-managed` and receive the
approved order in `DEARMACHINE_BACKENDS` plus the absolute `AGENT_MANAGER_PATH`.
Pass `--model your-model-alias` to override the project's configured default
model; when omitted, no model flag is forwarded.

For each inbound email, DearMachine Client passes the sender/thread metadata and the
newly authored text reported by AgentMail. Follow-ups resume the mapped
`mct-agent` session with `--session-id`; that persisted session owns prior
conversation context, so DearMachine Client does not replay the email thread.

The first poll runs immediately. Later polls start 60 seconds after the prior
poll completes. Override that with `--poll-interval`; use `--once` for a single
poll. Each poll claims messages before dispatching them to a worker pool. The
`--concurrency` flag limits the pool to three active email threads by default;
set `--concurrency 1` for sequential processing. The value must be at least one.
Messages from one thread always run one at a time and in sequence, so one
mct-agent session never has multiple active children.

## Locally skip inbox messages

Stop DearMachine Client before changing its local inbox decisions. To suppress the
exact snapshot of messages that are currently unread without changing
AgentMail, run:

```bash
dearmachine inbox skip \
  --current \
  --inbox-id your-inbox-id \
  --project ~/.dearmachine/entrypoint/main \
  --pidfile ~/.dearmachine/run/dearmachine.pid \
  --reason "stale before restart"
```

Pass one or more message IDs instead of `--current` to select individual
messages. `--current` records only the IDs returned by that read-only snapshot;
mail arriving afterward remains eligible. Use `--db` when the client uses a
non-default state database.

Inspect and reverse local decisions with:

```bash
dearmachine inbox skipped
dearmachine inbox skipped --json
dearmachine inbox unskip <message-id>
```

These commands never delete messages, change labels, mark messages processed,
or send replies through AgentMail. A skipped unread message therefore remains
visible in each remote poll, but DearMachine Client recognizes its local ID and does
nothing. After `unskip`, the next poll handles the message normally.

Skipping also removes a provisional first-message queue record left by an
interrupted run and asks mct-agent to delete that abandoned session. If session
cleanup fails, the message remains safely skipped and the command reports the
cleanup error. An already-running follow-up in a session with committed history
is rejected because silently removing it could leave that continuing session
with ambiguous partial context.

To deliberately abandon exactly one already-running follow-up, stop DearMachine Client and use the explicit recovery command instead:

```bash
dearmachine inbox abandon \
  --project ~/.dearmachine/entrypoint/main \
  --pidfile ~/.dearmachine/run/dearmachine.pid \
  --reason "stalled disposable test" \
  '<message-id>'
```

`inbox abandon` is not an alias for ordinary skip. Before every established
follow-up starts, DearMachine Client forks its committed mct session and records that
clean checkpoint in the durable pending row. The command requires a `running`
pending message with that checkpoint, atomically remaps the thread to it,
records the selected message as locally skipped, removes its pending row, and
deletes the partial source session. AgentMail remains unchanged. Future
follow-ups continue from the last committed sequence; unskipping the abandoned
message makes that message eligible again against the clean checkpoint.

The abandon command accepts one explicit message ID only. It intentionally has
no `--current` mode, so an operator cannot force-skip unrelated unread mail by
accident. Use ordinary `inbox skip` for an unread or provisional message that
has not entered an established session.

Every inbox mutation checks the supplied PID file, or the normal
`~/.dearmachine/run/dearmachine.pid` by default, and refuses to proceed when
that PID is alive. This protects the normal installed invocation and isolated
instances that follow the documented PID-file convention; operators must still
confirm that no client launched without that PID file is polling the database.

Long-running mode logs successful startup and graceful-shutdown counts to
stderr. Pass `--verbose` to also log the unread-message count for every poll;
idle polls remain silent by default.

Pass `--pidfile /path/to/dearmachine.pid` when a process supervisor needs a
PID file. DearMachine Client creates missing parent directories after the initial
`mct-agent sync`, writes its current PID before polling, and removes the file
on controlled exit. Omit the flag for the previous foreground-without-pidfile
behavior. This flag does not self-daemonize the process; supervisors that
require a returning start command must provide a background wrapper.

Set `AGENTMAIL_BASE_URL` to point the SDK at a non-production endpoint when
needed.

## Task project and entry-point repository

`--project` and `--entry-point-repo` are independent settings:

- `--project` selects the working directory and mct-agent project where email
  sessions are created. Its default is `.`, the directory from which DearMachine Client was launched.
- `--entry-point-repo` selects only the repository inspected and maintained by
  entry-point documentation sync. Its default is
  `~/.dearmachine/entrypoint/main`.

Setting `--entry-point-repo` does not make a different `--project` inherit the
entry point's context, documentation, or session history. For normal installed
operation, point both settings at the initialized entry point:

```bash
dearmachine \
  --inbox-id your-inbox-id \
  --project ~/.dearmachine/entrypoint/main \
  --entry-point-repo ~/.dearmachine/entrypoint/main \
  --pidfile ~/.dearmachine/run/dearmachine.pid
```

Use different paths only for deliberate development, migration, or isolated
testing arrangements. A disposable test normally supplies its test project
through `--project` and disables real entry-point maintenance with
`--entry-point-repo ""`.

## Initialize the machine entry point

Before using entry-point session maintenance on a new machine, initialize its
user-owned repository:

```bash
dearmachine init \
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

When `~/.dearmachine/entrypoint/main` exists, DearMachine Client evaluates its
mct-agent sessions after every successful poll. Override the paths with
`--entry-point-repo` and `--entry-point-prompt`, or pass an empty
`--entry-point-repo` to disable the trigger.

Entry-point maintenance is gated on 20 completed email turns by default. Set
`--maintenance-min-turns` or `DEARMACHINE_MAINTENANCE_MIN_TURNS` to change the
threshold; an explicit command-line flag takes precedence over the environment.
Set the threshold to `0` to disable the turn gate and retain the legacy cadence.
Passing the turn gate still requires at least two eligible sessions: one to
review and one newest session to hold.

For email sessions themselves to count toward the trigger, launch DearMachine Client with `--project` set to the entry-point repository. Distinct new threads
create distinct mct-agent sessions. DearMachine Client deliberately holds the newest
session until a later session arrives. Once at least two sessions are newer
than the review boundary, each successful poll reviews the oldest eligible
session while leaving the newest held:

1. forks the oldest eligible session;
2. resumes the fork with the entry-point update prompt;
3. deletes the temporary fork;
4. runs `mct-agent sync --include-docs`; and
5. records that reviewed source session in
   `state/sync-trigger.json`.

Before the first checkpoint, the project commit stored by mct-agent's
internal-README sync supplies the bootstrap boundary. Afterward, the checkpoint
is authoritative; a maintenance commit created after a held source session
must not make that session disappear. The checkpoint prevents an immediate
repeat when review correctly decides that no documentation change is
warranted. It is written only after the complete pipeline succeeds, so failed
runs remain eligible for retry. A failed forked run is cleaned up before the
error is reported.

For example, session B releases A for review, then session C releases B. With a
larger backlog, later successful polls drain the backlog oldest-first while one
newest session remains held. Equal update timestamps use the session ID as a
stable tie-breaker.

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

For normal installed operation against the machine entry point, follow
[`runbooks/operate-entrypoint-client.md`](./runbooks/operate-entrypoint-client.md).
All normal-operation, maintenance, and isolated live-test procedures are
indexed in [`runbooks/README.md`](./runbooks/README.md).

For a recoverable local reset and a rebuild from the current default-branch
HEAD, follow
[`runbooks/uninstall-reinstall.md`](./runbooks/uninstall-reinstall.md). The runbook preserves the
old installation until the fresh instance passes verification and does not
automatically import remote history into a privacy-cleaned repository.

Shared live-test provisioning and teardown are documented in
[`runbooks/testing/temporary-instance.md`](./runbooks/testing/temporary-instance.md).
The ordinary email and
skip/unskip smoke test is in
[`runbooks/testing/disposable-instance.md`](./runbooks/testing/disposable-instance.md);
ordered Forge, Codex, and fallback are in
[`runbooks/testing/live-backends.md`](./runbooks/testing/live-backends.md); and the
rolling checkpoint and internal-README update-sync evaluation is in
[`runbooks/testing/update-sync.md`](./runbooks/testing/update-sync.md).

```bash
go test ./...
go build -o /tmp/dearmachine-spike ./cmd/dearmachine
go build -o /tmp/agent-manager ./cmd/agent-manager
```
