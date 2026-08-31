# DearMachine Client and Agent Manager

This module proves the DearMachine Client alpha happy path:

1. Poll an AgentMail, OpenMail, or Sendmux inbox through a selectable mail
   transport.
2. Resolve provider thread aliases and stable Dear Machine footer references
   to durable machtiani session IDs in SQLite.
3. Invoke machtiani with a preallocated session ID for new threads or
   `--resume` for existing threads.
4. Inspect `machtiani session show --json`.
5. Send either the final answer or an AskUser clarification as a threaded
   transport reply.

The DearMachine Client processes independent email threads concurrently while
preserving FIFO order within each thread. It does not implement retries,
preemption, or sandbox policy.

## Run

SQLite uses `go-sqlite3`, so source builds require CGO and
a C compiler; the resulting binary has no separate SQLite runtime dependency.
The default Nix package contains both `dearmachine` and its matching
`agent-manager`:

```bash
cd ..
nix profile install .#dearmachine
command -v dearmachine agent-manager
```

Install `machtiani` and backend CLIs separately. Native DearMachine resolves
them from the inherited host `PATH` and uses their normal host configuration
and credentials.

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
managed machtiani run. Restart DearMachine Client after changing the configuration.
Agent Manager includes `codex`, `codex-yolo`, and `forge`. The ordinary
`codex` adapter uses Codex's `workspace-write` sandbox. `codex-yolo` is an
explicit opt-in adapter that passes
`--dangerously-bypass-approvals-and-sandbox`, giving an email-dispatched
worker every host permission available to the DearMachine user. Do not select
it unless that unrestricted trust boundary is intentional. A version-1 config using
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

```bash
export AGENTMAIL_API_KEY_FILE="$HOME/.config/dearmachine/agentmail-api-key"

dearmachine up --create \
  --email you@example.com \
  --new-inbox --transport agentmail \
  --project ~/.dearmachine/entrypoint/main \
  --entry-point-repo ~/.dearmachine/entrypoint/main \
  --maintenance-min-turns 20 \
  --magnifica-humanitas
```

`AGENTMAIL_API_KEY_FILE` must contain exactly one non-empty line.
`AGENTMAIL_API_KEY` remains supported as an environment-based alternative.
Creation first initializes the selected entry-point repository when it is
absent. With no override, that is the default repository at
`~/.dearmachine/entrypoint/main`. It then provisions and registers the inbox,
asks the selected transport to authorize the exact correspondent, creates the
pair's UUID-path SQLite database, and starts the daemon. AgentMail pairing
idempotently ensures
inbox-scoped receive, reply, and send allow entries before the local pair is
published. Later, plain `dearmachine up` starts every registered pair. Repeat
`--pair <email-or-uuid>` to run only a specific subset for that invocation.
Selection never changes registry state.

To add a pair, stop the daemon and run `up --create` again. Use `--new-inbox`
for a new provider inbox, or `--inbox <registered-uuid-or-address>` to
intentionally share an existing inbox. The version-2 registry stores inboxes
separately from pairs; there is no active pair, display name, `--new`, or
`--switch`. Plain `up` backgrounds the native client and waits until startup is
ready. Use `status` and `down` to inspect and stop it. Use `up --foreground`
under systemd or in a test container; the canonical PID lock remains internal.

### Optional OpenMail transport

To adopt a dedicated OpenMail inbox as the first pair, provide its exact inbox
ID or full address and transport during creation:

```bash
export OPENMAIL_API_KEY_FILE="$HOME/.config/dearmachine/openmail-api-key"
# Or point OPENMAIL_API_KEY_FILE at another operator-managed one-line key file.
dearmachine up --create --email 'user@example.test' \
  --inbox '<openmail-inbox-id-or-address>' --transport openmail --once
```

`OPENMAIL_API_KEY` is the direct environment alternative. OpenMail credentials
are loaded only by the OpenMail constructor; selecting OpenMail never invokes
the AgentMail credential loader. If neither credential variable is set, the
constructor optionally checks
`$HOME/.config/dearmachine/openmail-api-key`. That generic path is not created
or written by DearMachine.

The production adapter uses only the documented
`https://api.openmail.sh/v1` API. Its default mode is read-only inspection. To
let the running client reply and mark processed threads read, explicitly enable
both live gates:

```bash
export DEARMACHINE_LIVE_OPENMAIL=1
export DEARMACHINE_LIVE_OPENMAIL_APPLY=1
```

The central inbox router authorizes the exact canonical pair sender and inbox
recipient before work is created. Mutations are reauthorized and unknown
attachment IDs are rejected. There is no permissive default.

OpenMail exposes unread state per thread rather than per message. Polling
therefore returns the newest inbound message in each unread, fully allowed
thread, and successful processing marks that whole thread read. `inbox skip
--pair <email-or-uuid> --current` uses the registered pair's transport; skip
decisions remain local and never change the remote inbox.

For a credentialed test that does not reuse normal runtime state or a normal
inbox, follow the
[OpenMail transport runbook](runbooks/testing/openmail-transport.md).

### Optional Sendmux transport

Adopt Sendmux with an exact mailbox ID or full address. The mailbox key may be
provided directly or through an operator-owned one-line file; when neither is set, the adapter optionally checks
`$HOME/.config/dearmachine/sendmux-api-key`.

```bash
export SENDMUX_MAILBOX_API_KEY_FILE="$HOME/.config/dearmachine/sendmux-api-key"
# Optional: use Sendmux's separate email.send credential for outbound replies.
export SENDMUX_SEND_API_KEY_FILE="$HOME/.config/dearmachine/sendmux-send-api-key"
dearmachine up --create --email '<paired-address>' \
  --inbox '<sendmux-mailbox-id-or-address>' --transport sendmux --once
```

`SENDMUX_MAILBOX_API_KEY` and `SENDMUX_SEND_API_KEY` are the direct
environment alternatives. The optional send credential is used only for
outbound replies; receiving and marking messages processed continue to use the
mailbox credential. Inbound mail must come from the pair's exact canonical
sender and be addressed to the registered mailbox. Other correspondents fail
closed without creating pair work.

Sendmux is inspect-only unless both live gates are explicitly enabled:

```bash
export DEARMACHINE_LIVE_SENDMUX=1
export DEARMACHINE_LIVE_SENDMUX_APPLY=1
```

The adapter uses Sendmux's native idempotency key. Sendmux's HTTP send API
accepts X-headers but not caller-supplied RFC `In-Reply-To` or `References`
headers, and exposes no reply endpoint. An outbound answer may therefore begin
a new provider-local thread; the stable Dear Machine footer associates the
human's next reply with the existing session. Presigned attachment URLs are
fetched without the mailbox credential and remain subject to Dear Machine's
byte limit. SDK retries are disabled pending a separate operational retry
policy. Follow the isolated
[Sendmux transport runbook](runbooks/testing/sendmux-transport.md) for a
credentialed two-turn continuation test.

Each pair's SQLite state database is
`~/.dearmachine/pairs/<pair-uuid>/state/dearmachine.db`. DearMachine Client
creates its state directory with mode `0700` and creates or tightens the
database to mode `0600`. Database paths come only from the pair registry; an
arbitrary database override is not part of the public interface.

The daemon runs `machtiani sync` before polling begins and again immediately
before each `machtiani run`. Runs use `--mode agent-managed` and receive the
approved order in `DEARMACHINE_BACKENDS` plus the absolute `AGENT_MANAGER_PATH`.
Pass `--model your-model-alias` to override the project's configured default
model; when omitted, no model flag is forwarded.

For each inbound email, DearMachine Client passes the sender/thread metadata and the
newly authored text reported by the selected transport. Follow-ups resume the mapped
`machtiani` session with `--resume`; that persisted session owns prior
conversation context, so DearMachine Client does not replay the email thread.

Every outbound answer and AskUser response ends with a stable, opaque Dear
Machine conversation reference. The reference maps to local SQLite state and
does not expose the machtiani session ID. On inbound mail the adapters recover
the reference from full text or HTML even when they separately provide a clean
new-message extract. DearMachine removes the footer before prompt construction.
If a provider assigns a legitimate reply a new thread ID, one valid reference
associates that ID as another alias of the existing conversation. A known
thread mapping takes precedence over quoted references, and multiple distinct
references never cause an automatic association. Forward/fork classification
is intentionally separate from this continuity rule.

The first poll runs immediately. Later polls start 60 seconds after the prior
poll completes. Override that with `--poll-interval`; use `--once` for a single
poll. Each poll claims messages before dispatching them to a worker pool. The
`--concurrency` flag limits the pool to three active email threads by default;
set `--concurrency 1` for sequential processing. The value must be at least one.
Messages from one resolved conversation always run one at a time and in
sequence, so one machtiani session never has multiple active children.

## Locally skip inbox messages

Stop DearMachine Client before changing its local inbox decisions. To suppress the
exact snapshot of messages that are currently eligible without changing the
remote inbox, run:

```bash
dearmachine inbox skip \
  --pair you@example.com \
  --current \
  --project ~/.dearmachine/entrypoint/main \
  --reason "stale before restart"
```

Pass one or more message IDs instead of `--current` to select individual
messages. `--current` records only the IDs returned by that read-only snapshot;
mail arriving afterward remains eligible. The pair email address or UUID
selects its registry-owned database and inbox transport.

Inspect and reverse local decisions with:

```bash
dearmachine inbox skipped
dearmachine inbox skipped --pair you@example.com --json
dearmachine inbox unskip --pair you@example.com <message-id>
```

These commands never delete messages, change labels or thread state, mark
messages processed, or send replies. A skipped eligible message therefore
remains visible in each remote poll, but DearMachine Client recognizes its local
ID and does nothing. After `unskip`, the next poll handles the message normally.

Skipping also removes a provisional first-message queue record left by an
interrupted run and asks machtiani to delete that abandoned session. If session
cleanup fails, the message remains safely skipped and the command reports the
cleanup error. An already-running follow-up in a session with committed history
is rejected because silently removing it could leave that continuing session
with ambiguous partial context.

To deliberately abandon exactly one already-running follow-up, stop DearMachine Client and use the explicit recovery command instead:

```bash
dearmachine inbox abandon \
  --pair you@example.com \
  --project ~/.dearmachine/entrypoint/main \
  --reason "stalled disposable test" \
  '<message-id>'
```

`inbox abandon` is not an alias for ordinary skip. Before every established
follow-up starts, DearMachine Client forks its committed agent session and records that
clean checkpoint in the durable pending row. The command requires a `running`
pending message with that checkpoint, atomically remaps the thread to it,
records the selected message as locally skipped, removes its pending row, and
deletes the partial source session. The remote inbox remains unchanged. Future
follow-ups continue from the last committed sequence; unskipping the abandoned
message makes that message eligible again against the clean checkpoint.

The abandon command accepts one explicit message ID only. It intentionally has
no `--current` mode, so an operator cannot force-skip unrelated unread mail by
accident. Use ordinary `inbox skip` for an unread or provisional message that
has not entered an established session.

Every inbox mutation checks the canonical daemon lock and refuses to proceed
while the client is running.

Long-running mode logs successful startup and graceful-shutdown counts to
stderr. Pass `--verbose` to also log the unread-message count for every poll;
idle polls remain silent by default.

Plain `dearmachine up` launches the native client in the background and waits
for readiness. `dearmachine up --foreground` keeps it attached for a process
supervisor or test container. Both forms use the same internal lock and
readiness files.

Set `AGENTMAIL_BASE_URL` to point the SDK at a non-production endpoint when
needed.

## Task project and entry-point repository

`--project` and `--entry-point-repo` are independent settings:

- `--project` selects the working directory and machtiani project where email
  sessions are created. Its default is `.`, the directory from which DearMachine Client was launched.
- `--entry-point-repo` selects only the repository inspected and maintained by
  entry-point documentation sync. Its default is
  `~/.dearmachine/entrypoint/main`.

Setting `--entry-point-repo` does not make a different `--project` inherit the
entry point's context, documentation, or session history. For normal installed
operation, point both settings at the initialized entry point:

```bash
dearmachine up \
  --project ~/.dearmachine/entrypoint/main \
  --entry-point-repo ~/.dearmachine/entrypoint/main \
  --magnifica-humanitas
```

Use different paths only for deliberate development or isolated
testing arrangements. A disposable test normally supplies its test project
through `--project` and disables real entry-point maintenance with
`--entry-point-repo ""`.

## Custom machine entry points

`up --create` initializes its selected `--entry-point-repo` automatically when
the repository is absent. It never replaces or rewrites an existing Git
repository. The separate `init` command remains available when deliberately
preparing a custom entry-point path before pair creation:

```bash
dearmachine init \
  --entry-point-repo ~/.dearmachine/entrypoint/main \
  --agent-bin /absolute/path/to/machtiani
```

Initialization configures local Git LFS and bootstraps the repository in two
stages. The first commit contains only a neutral machine-entry-point skeleton:
the top-level README, directory READMEs, repository rules, and the generic
maintenance prompt. machtiani initializes the project identity and syncs that
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
machtiani sessions after every successful poll. Override the paths with
`--entry-point-repo` and `--entry-point-prompt`, or pass an empty
`--entry-point-repo` to disable the trigger.

Entry-point maintenance is gated on 20 completed email turns by default. Set
`--maintenance-min-turns` or `DEARMACHINE_MAINTENANCE_MIN_TURNS` to change the
threshold; an explicit command-line flag takes precedence over the environment.
Set the threshold to `0` to disable the turn gate and retain the legacy cadence.
Passing the turn gate still requires at least two eligible sessions: one to
review and one newest session to hold.

For email sessions themselves to count toward the trigger, launch DearMachine Client with `--project` set to the entry-point repository. Distinct new threads
create distinct machtiani sessions. DearMachine Client deliberately holds the newest
session until a later session arrives. Once at least two sessions are newer
than the review boundary, one maintenance pass reviews every eligible session
from oldest to newest while leaving only the newest held. For each reviewed
source, the client:

1. forks the oldest eligible session;
2. resumes the fork with the entry-point update prompt;
3. deletes the temporary fork;
4. runs `machtiani sync --include-docs`; and
5. records that reviewed source session in
   `state/sync-trigger.json`.

Before the first checkpoint, the project commit stored by machtiani's
internal-README sync supplies the bootstrap boundary. Afterward, the checkpoint
is authoritative; a maintenance commit created after a held source session
must not make that session disappear. The checkpoint prevents an immediate
repeat when review correctly decides that no documentation change is
warranted. The checkpoint advances after each complete source pipeline. The
turn gate resets only after the entire eligible snapshot drains, so a failure
retains completed progress and retries the remaining backlog without waiting
for another threshold crossing. A failed forked run is cleaned up before the
error is reported.

For example, session B releases A for review. If a gate opens with A, B, C, and
D eligible, that maintenance pass reviews A, B, and C in order and holds D.
Equal update timestamps use the session ID as a stable tie-breaker. While the
maintenance lane owns the repository, normal polling and durable claiming
continue; claimed email work is recovered and dispatched after maintenance
releases the repository lane.

The `state/` checkpoint is host-local and should remain ignored by Git. The
internal-README marker remains owned by machtiani under the UUID project store
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

For default native operation against the machine entry point, follow
[`runbooks/native-install.md`](./runbooks/native-install.md). For the optional
installed Linux container stack, follow
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
