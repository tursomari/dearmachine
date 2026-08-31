# Multi-pair live QSE

This is a prompt for a capable local testing agent. It exercises the real
provider and backend without touching normal DearMachine state. Follow
[`temporary-instance.md`](./temporary-instance.md) for credential handling,
bounded waits, private evidence, and teardown. Never print credential values.

## Contract under test

DearMachine has one version-2 registry, one daemon, one exclusive PID lock,
one poller per provider inbox, and one UUID-path SQLite database per pair.
Plain `dearmachine up` starts every pair. A repeatable `--pair
<email-or-uuid>` narrows only that invocation and never changes the registry.
There is no active pair, display name, `--new`, or `--switch`.

Create pairs only with `dearmachine up --create` while the daemon is stopped:

- `--new-inbox --transport agentmail` provisions a real randomized AgentMail
  inbox and registers it.
- `--inbox <registered-uuid-or-address>` intentionally shares an inbox.
- `--inbox <exact-provider-id-or-address> --transport <id>` adopts an existing
  provider inbox after exact API inspection.

OpenMail and Sendmux support exact adoption but not provider provisioning.
Incomplete non-interactive creation must fail before any provider mutation.
First-pair creation initializes the missing selected entry-point repository
before provider mutation. Existing Git repositories remain untouched. Unsafe
files, symlinks, and non-empty non-Git directories fail before provider
mutation.

## Isolated setup

Create a mode-0700 temporary root. Set both `HOME` and `DEARMACHINE_HOME` to a
home beneath it. Copy or reference only operator-authorized mode-0600
credential files; do not copy normal configuration, registry, databases, PID
files, or project state. Build the candidate `dearmachine` and matching
`agent-manager` into the temporary root and use a disposable initialized
machtiani project.

Use `--magnifica-humanitas` on every daemon launch. Keep the provider key in an
environment variable or key-file variable, never an argument. Use a short poll
interval and an outer timeout for every command.

## Creation and intent checks

1. Prove plain `up --once` fails with instructions to use `up --create` when
   no registry exists.
2. Prove `up --new` and `up --switch` are unknown flags.
3. Prove incomplete non-interactive `up --create` fails without creating an
   inbox, registry, or database.
4. Create pair A with a new AgentMail inbox and its authorized correspondent.
   Prove the same command initialized the previously absent default entry point
   before calling the provider:

   ```bash
   dearmachine up --create \
     --email '<pair-a-user-address>' \
     --new-inbox --transport agentmail \
     --once --magnifica-humanitas <isolated-run-flags>
   ```

5. Stop the daemon. Create pair B on a second new inbox. Then create pair C on
   A's inbox using `--inbox <a-inbox-uuid>`. Confirm C creation did not call
   the provider create API, that A and C reference the same inbox UUID, and
   that creation ensured exact receive, reply, and send allow entries for C on
   A's provider inbox before publishing C locally.
6. While a daemon owns the PID lock, prove another `up` and every
   `up --create` fail before polling or provider mutation.

Retain a redacted registry snapshot. It must be version 2, contain separate
`[[inboxes]]` and `[[pairs]]` records, and contain none of `active`,
`display_name`, `allow`, or `dear_machine_address`.

## Single-daemon routing and state isolation

Start plain `up` once for A, B, and C. Verify logs show one process and one
startup for each selected pair. Send one uniquely tagged authorized request
from each pair's exact user address. A and C share a physical inbox; B does
not. Pass only if each request gets exactly one reply from its configured
inbox and each pair database contains only its own provider message, thread,
session, and alias rows.

Inspect all database bindings and counts before and after delivery:

```bash
sqlite3 '<pair-db>' 'SELECT pair_id, schema_version FROM pair_meta;
SELECT count(*) FROM pending_messages;
SELECT count(*) FROM processed_messages;
SELECT count(*) FROM thread_sessions;
SELECT count(*) FROM thread_aliases;'
```

Each `pair_meta.pair_id` must equal the registry pair UUID and schema version
must equal 2. The shared inbox must be polled once per interval, not once per
pair. An inbound message whose sender/recipient route is unknown or ambiguous
must fail closed before creating work in any pair database.

## Selection, restart, and scheduling

Hash the registry, run `up --pair <b-email> --once`, then hash it again. Only
B may poll or recover and the hashes must match. Repeat with two `--pair`
flags and UUID/email mixtures. Unknown selectors and ambiguous duplicate email
selectors must fail before opening a transport.

Create a recoverable pending row in A, restart the all-pairs daemon, and prove
only A resumes it exactly once; B and C must not fetch A's message ID. Quote an
A reply footer in a new B conversation and prove B creates or continues only a
B session.

With `--concurrency 2`, deliver long-running work to three independent pairs.
Use timestamped backend evidence to prove no more than two jobs execute at
once. Deliver a second message on one active pair/thread and prove that thread
remains serialized while a different pair can use the other worker slot.

## Teardown and report

Stop the daemon gracefully and prove the PID lock is gone. Delete only the
temporary provider inboxes created by this QSE, remove only the isolated
project store and runtime root, and verify normal service, registry, inboxes,
and databases are unchanged.

Report the candidate revisions, exact commands with secrets redacted, pair and
inbox identities only in the private evidence directory, registry hashes,
database snapshots, reply counts, maximum observed concurrency, lock behavior,
and cleanup result. Mark any provider property that cannot be legitimately
observed `INCONCLUSIVE`; never manufacture provider IDs or SQLite rows to turn
it green.
