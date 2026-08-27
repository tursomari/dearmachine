# Multi-pair QSE Protocol

This document is a prompt for a capable local testing agent, not an executable
test script. It runs quiet live quality smoke experiments against the real
AgentMail transport. Use [`temporary-instance.md`](./temporary-instance.md)
for provisioning, isolation, evidence retention, and teardown; every
requirement in that reference is part of this protocol.

## Prompt to give the testing agent

Exercise four independent paired-user lanes end to end. Do not alter product
code, tests, configuration outside the temporary root, or normal mailbox
state. Obtain authorization for every temporary inbox, sender, allow-policy
entry, and message before starting. Use placeholders in the report and retain
live IDs only in the Git-excluded private evidence directory.

This protocol provisions and selects registry records only through the
`dearmachine up` wizard. Use `up --new` to add each record and `up --switch
<pair-id-or-name>` before a selected launch. If that wizard is unavailable,
record the missing CLI surface and classify the affected scenarios as blocked;
do not manually author `pairs.toml` as a test-only workaround.

## Multi-pair lane isolation

### Authorized two-inbox variant

When authorization permits exactly two temporary inboxes, create pair A first
and create pair B only after A has state; B takes the fresh-state role normally
assigned to pair C. Verify B's fresh `pair_meta` binding and empty tables before
its first delivery, then compare A/B table independence. AgentMail IDs are
inbox-scoped, so equal provider IDs across the two inboxes must not be
manufactured through SQLite; classify that equality sub-check as
not-legitimately-observable. If a self-send API call does not produce an
inbound message, use the documented cross-lane allow-set pattern instead and
record the provider behavior.

Create a unique root and keep both `DEARMACHINE_HOME` and `HOME` below it so
the registry's default home lookup is isolated. The operator-owned credential
file must already have mode `0600`; export its path only, never its contents.

```bash
runtime_root=$(mktemp -d)
chmod 0700 "$runtime_root"
export DEARMACHINE_HOME="$runtime_root/dearmachine-home"
export HOME="$DEARMACHINE_HOME"
install -d -m 0700 "$DEARMACHINE_HOME" "$runtime_root/project-a" \
  "$runtime_root/project-b" "$runtime_root/project-c"
export DEEPSEEK_API_KEY
export AGENTMAIL_API_KEY_FILE=<mode-0600-operator-owned-key-file>
test "$(stat -c %a "$AGENTMAIL_API_KEY_FILE")" = 600
```

Load and validate `DEEPSEEK_API_KEY` through the shared temporary-instance
credential procedure before this block. Initialize machtiani with
`--api-key-env DEEPSEEK_API_KEY`, verify its configuration contains the
literal `${DEEPSEEK_API_KEY}` reference, and keep the variable exported in the
same shell for every `up` launch and backend process.

Create three temporary AgentMail inboxes, then use `dearmachine up --new` three
times to enter the corresponding registry records in its guided prompts:
pair A uses `<user-paired-address-a>` and `<dear-machine-address-a>`, pair B
uses `<user-paired-address-b>` and `<dear-machine-address-b>`, and pair C uses
`<user-paired-address-c>` and `<dear-machine-address-c>`. Give each record a
new UUID v4, its returned inbox ID, transport `agentmail`, and its own allow
set. The wizard stores them in `$DEARMACHINE_HOME/.dearmachine/pairs.toml` and
creates the empty metadata-bound state database. Select a lane with `up
--switch` before each launch; the selected record becomes the current client's
registry-default lane. Record the pair UUIDs privately as
`<pair-a-uuid>`, `<pair-b-uuid>`, and `<pair-c-uuid>`.

For all launches, create a version-1 config below the root, use a disposable
project, and pass an empty `--entry-point-repo`. Do not use a normal project,
normal Agent Manager, normal database, or a credential in an argument. The
following paths name the state that must be observed:

```bash
pair_a_db="$DEARMACHINE_HOME/.dearmachine/pairs/<pair-a-uuid>/state/dearmachine.db"
pair_b_db="$DEARMACHINE_HOME/.dearmachine/pairs/<pair-b-uuid>/state/dearmachine.db"
pair_c_db="$DEARMACHINE_HOME/.dearmachine/pairs/<pair-c-uuid>/state/dearmachine.db"
```

Before and after every launch, inspect the registry and the applicable database
without modifying it:

```bash
cat "$DEARMACHINE_HOME/.dearmachine/pairs.toml"
sqlite3 "$pair_a_db" 'SELECT pair_id, schema_version FROM pair_meta; SELECT count(*) FROM thread_sessions; SELECT count(*) FROM pending_messages; SELECT count(*) FROM processed_messages; SELECT count(*) FROM thread_aliases;'
sqlite3 "$pair_b_db" 'SELECT pair_id, schema_version FROM pair_meta; SELECT count(*) FROM thread_sessions; SELECT count(*) FROM pending_messages; SELECT count(*) FROM processed_messages; SELECT count(*) FROM thread_aliases;'
sqlite3 "$pair_c_db" 'SELECT pair_id, schema_version FROM pair_meta; SELECT count(*) FROM thread_sessions; SELECT count(*) FROM pending_messages; SELECT count(*) FROM processed_messages; SELECT count(*) FROM thread_aliases;'
```

Confirm each `pair_meta.pair_id` equals its registry UUID and no database has a
foreign binding. Inspect individual rows as needed with `SELECT * FROM
thread_sessions`, `pending_messages`, `processed_messages`, and
`thread_aliases`; retain the resulting live identifiers only in private
evidence. Observe a successful poll before sending and use bounded deadlines
for every expected transition.

## Pair-A pending revival falls through to pair B without pair isolation

First demonstrate the shared-database red phase using the pre-pairs binary.
Keep no registry at the shared home and deliberately leave pair A's message in
the shared database's `pending_messages` table by stopping during its turn, or
by delivering it while the client is stopped. Record that pending row and the
pair-A transport message ID privately. Start pair B against that same shared
database:

```bash
AGENTMAIL_API_KEY_FILE="$AGENTMAIL_API_KEY_FILE" \
DEARMACHINE_HOME="$runtime_root/shared-home" HOME="$runtime_root/shared-home" \
DEARMACHINE_ALLOW='<user-paired-address-b>' \
<pre-pairs-dearmachine-binary> \
  --inbox-id <pair-b-inbox-id> \
  --db "$runtime_root/shared-state/dearmachine.db" \
  --project "$runtime_root/project-b" \
  --config "$runtime_root/dearmachine-b.toml" \
  --agent-manager "$runtime_root/agent-manager" \
  --agent-bin <absolute-machtiani-path> \
  --entry-point-repo "" \
  --poll-interval 10s \
  --verbose
```

Observe the expected red behavior: B startup attempts to revive or fetch A's
pending row through B's transport, errors or blocks startup, or otherwise
touches A's state. Stop it and preserve the shared database evidence. This is a
baseline demonstration only; do not send any message through the wrong lane.

For the isolated green phase, select pair A through `up --switch`, create a
fresh pending A row in `pair_a_db`, stop A, then select pair B through `up
--switch`. Launch B with B's registered database and allow policy:

```bash
AGENTMAIL_API_KEY_FILE="$AGENTMAIL_API_KEY_FILE" \
DEARMACHINE_HOME="$DEARMACHINE_HOME" HOME="$DEARMACHINE_HOME" \
DEARMACHINE_ALLOW='<user-paired-address-b>' \
"$runtime_root/dearmachine" up --switch <pair-b-uuid> \
  --project "$runtime_root/project-b" \
  --config "$runtime_root/dearmachine-b.toml" \
  --agent-manager "$runtime_root/agent-manager" \
  --agent-bin <absolute-machtiani-path> \
  --entry-point-repo "" \
  --poll-interval 10s \
  --verbose
```

Verify B completes its first poll and, if a B message is sent, processes only
that message. While B is running and after it stops, query both databases:
A's pending row must remain pending, B must have no A row, and no request may
reach the pair-A transport. Select A through the wizard and relaunch it:

```bash
AGENTMAIL_API_KEY_FILE="$AGENTMAIL_API_KEY_FILE" \
DEARMACHINE_HOME="$DEARMACHINE_HOME" HOME="$DEARMACHINE_HOME" \
"$runtime_root/dearmachine" up --switch <pair-a-uuid> \
  --project "$runtime_root/project-a" \
  --config "$runtime_root/dearmachine-a.toml" \
  --agent-manager "$runtime_root/agent-manager" \
  --agent-bin <absolute-machtiani-path> \
  --entry-point-repo "" \
  --poll-interval 10s \
  --verbose
```

Pass only if the red baseline shows the shared-state failure, B starts and
processes solely in B's lane, A's pending row remains untouched until A starts,
and A alone resumes it exactly once with no duplicate reply.

## Pair-A reply footer cannot join pair B session

Create and complete one pair-A conversation, record its real reply footer
privately as `dm1-<pair-a-conversation-reference>`, and preserve A's session
and `thread_sessions` count. In the shared-database red phase, send a new
pair-B-lane email whose quoted content includes that A footer and launch the
pre-pairs binary using the shared command above (substitute the pair-B inbox).
Confirm and retain evidence that the footer joins or resumes A's session: the
wrong-session attachment is the expected red outcome.

For the green phase, keep the A state only in `pair_a_db`, select B through
the wizard, and send a pair-B email with the same quoted A footer. Launch B:

```bash
AGENTMAIL_API_KEY_FILE="$AGENTMAIL_API_KEY_FILE" \
DEARMACHINE_HOME="$DEARMACHINE_HOME" HOME="$DEARMACHINE_HOME" \
"$runtime_root/dearmachine" up --switch <pair-b-uuid> \
  --project "$runtime_root/project-b" \
  --config "$runtime_root/dearmachine-b.toml" \
  --agent-manager "$runtime_root/agent-manager" \
  --agent-bin <absolute-machtiani-path> \
  --entry-point-repo "" \
  --poll-interval 10s \
  --verbose
```

Use `sqlite3` to compare A and B `thread_sessions`, `processed_messages`, and
`thread_aliases` before and after. B must create a new B session (or safely
ignore the quoted footer), never reference A's session, and leave every A row
unchanged. Pass only when the shared baseline joins the wrong session and the
isolated run neither joins nor mutates A while producing at most the expected
single B-lane result.

## Provider ID overlap stays independent

Using temporary inboxes, arrange or observe a legitimate AgentMail overlap:
pair C receives a thread or message ID equal to one that previously existed in
pair A's inbox. AgentMail identifiers are inbox-scoped, so do not manufacture
an overlap by editing SQLite. Create C only after A has state. Before mail is
sent, inspect `pairs.toml` and `pair_c_db`: it must be a fresh database with
an empty `thread_sessions`, `pending_messages`, `processed_messages`, and
`thread_aliases` set, plus exactly the C `pair_meta` binding.

Select C through the wizard and launch its registry-default lane:

```bash
AGENTMAIL_API_KEY_FILE="$AGENTMAIL_API_KEY_FILE" \
DEARMACHINE_HOME="$DEARMACHINE_HOME" HOME="$DEARMACHINE_HOME" \
DEARMACHINE_ALLOW='<user-paired-address-c>' \
"$runtime_root/dearmachine" up --switch <pair-c-uuid> \
  --project "$runtime_root/project-c" \
  --config "$runtime_root/dearmachine-c.toml" \
  --agent-manager "$runtime_root/agent-manager" \
  --agent-bin <absolute-machtiani-path> \
  --entry-point-repo "" \
  --poll-interval 10s \
  --verbose
```

Send the authorized C-lane message and inspect every listed table in A and C.
Pass only if each lane records and deduplicates its same-looking provider ID
independently, C's newly created database began empty with correct `pair_meta`,
and neither lane gains the other's processed row, alias, or session.

## Per-pair allow set keeps traffic in lane

Before sending, inspect the exact transport policy and registry values. Verify
that pair A allows `<user-paired-address-a>` and refuses
`<user-paired-address-b>`, while pair B allows `<user-paired-address-b>` and
refuses `<user-paired-address-a>`. Do not send a negative-case message until
the recipient inbox's allow policy is confirmed; a rejected message is not
retroactively recoverable by changing policy.

Select A through the wizard and launch it with its registered allow set:

```bash
AGENTMAIL_API_KEY_FILE="$AGENTMAIL_API_KEY_FILE" \
DEARMACHINE_HOME="$DEARMACHINE_HOME" HOME="$DEARMACHINE_HOME" \
"$runtime_root/dearmachine" up --switch <pair-a-uuid> \
  --project "$runtime_root/project-a" \
  --config "$runtime_root/dearmachine-a.toml" \
  --agent-manager "$runtime_root/agent-manager" \
  --agent-bin <absolute-machtiani-path> \
  --entry-point-repo "" \
  --poll-interval 10s \
  --verbose
```

Send the allowed A message and the B-address negative case to A's inbox.
Confirm only the allowed message reaches processing and reply. Then stop A,
select B through `up --switch`, and launch B with its distinct registered
allow set:

```bash
AGENTMAIL_API_KEY_FILE="$AGENTMAIL_API_KEY_FILE" \
DEARMACHINE_HOME="$DEARMACHINE_HOME" HOME="$DEARMACHINE_HOME" \
DEARMACHINE_ALLOW='<user-paired-address-b>' \
"$runtime_root/dearmachine" up --switch <pair-b-uuid> \
  --project "$runtime_root/project-b" \
  --config "$runtime_root/dearmachine-b.toml" \
  --agent-manager "$runtime_root/agent-manager" \
  --agent-bin <absolute-machtiani-path> \
  --entry-point-repo "" \
  --poll-interval 10s \
  --verbose
```

Send the allowed B message and the A-address negative case to B's inbox.
Inspect both databases with `sqlite3`: each allowed message alone may create a
pending, processed, alias, or session row in its own lane; refused traffic must
not create a session, backend execution, reply, pending row, or processed row.
Pass only if each positive sender is processed in its own lane, both converse
senders fail closed, and all per-pair state and `pair_meta` bindings remain
independent.

## Final report

Report source revisions, each red and green outcome, pair UUIDs and transport
IDs privately, launch logs, table-count snapshots, policy verification, reply
counts, and cleanup result. Classify a scenario as `INCONCLUSIVE` if provider
ID reuse cannot be legitimately observed. Follow the shared reference for
graceful shutdown, inbox and policy deletion, exact project-store cleanup,
runtime-root removal, and proof that normal state was preserved.
