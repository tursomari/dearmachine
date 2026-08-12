# Continuous Intake and Turn-Gated Maintenance Live Protocol

This document is a prompt for a capable local agent, not an executable test script. It verifies
that live inbox intake continues while an active worker batch drains and entry-point maintenance
waits for accumulated turns without weakening FIFO, concurrency, durability, or rolling boundaries.

Follow [`temporary-instance.md`](./temporary-instance.md) for all shared provisioning, isolation,
observation, evidence, and teardown requirements. Follow the natural-user A-B-C-D evidence and
classification conventions in [`update-sync.md`](./update-sync.md). Do not use a normal inbox,
database, Agent Manager home, project, or entry point.

## Prompt to give the testing agent

Conduct one isolated two-phase live evaluation of continuous intake and turn-gated maintenance.
Use observed state transitions, not fixed sleeps, to decide when to proceed. Test the checked-out
product revision as-is. Do not edit product code, tests, prompts, or documentation in response to
the run. Only maintenance changes inside the disposable entry point are expected test output.

### Intended behavior

During a normal `ProcessOnce`, DearMachine Client recovers durable pending work once at entry, polls and
claims the inbox, and dispatches a worker batch. A per-batch claim loop then calls the extracted
`pollAndClaim` step at the client poll interval while workers remain active. Each arrival is claimed
only through `Store.BeginMessage`, which creates a `pending_messages` row, and is enqueued on the
shared queue. The buffered `threadWorkQueue.added` wakeup lets an idle worker take the new
independent thread without waiting for the active batch to end.

The claim loop stops and is joined when the batch drains. There is deliberately no final extra poll.
A claim completed by the just-stopped loop is durable; if not dispatched in that batch, the next
`ProcessOnce` replays it through `recoverPending`. A claim-loop error is returned after the join
unless an earlier worker error owns the batch's `firstErr`. A surfaced poll error is not an idle cycle.

These invariants remain in force:

- `take()` skips an `inFlight` thread, so its queued items retain FIFO sequence
  order while later independent threads may run;
- the jobs channel and worker pool enforce the global `--concurrency` limit;
- inbound durability is exclusively through `BeginMessage` and
  `pending_messages`;
- `recoverPending` runs only at `ProcessOnce` entry; and
- `RunOnce`/`--once` retains serial-batch semantics. It may claim arrivals
  while its one batch is active, but it exits after that batch drains and does
  not add a final poll.

Entry-point maintenance defaults to `--maintenance-min-turns 20`. Before session detection,
`mct-agent session list`, or Git-boundary lookup, `OrchestrateSync` returns when the accumulated
count is below threshold. One `processed_messages` row is one completed inbound turn. The count is
derived with `Store.CountProcessedSince(checkpoint.UpdatedAt)`, equivalent to:

```sql
SELECT COUNT(*)
FROM processed_messages
WHERE processed_at > <checkpoint-updated-at>;
```

This needs no schema migration and follows technological subsidiarity by deriving the decision
entirely from client-owned SQLite state. A successful fork, run, delete, and
`sync --include-docs` advances the checkpoint and writes `turns_accumulated: 0`. Passing the gate
does not alter the rolling rule: review the oldest eligible source and hold the newer tail.

### Provision the protocol-specific instance

1. Record the source revision, complete source worktree status, backend order,
   mct-agent revision, normal processes, and normal runtime paths as required
   by the shared reference. Preserve every pre-existing tracked and untracked
   path.
2. Create and secure a unique runtime root exactly as follows, retaining its
   absolute path privately:

   ```bash
   runtime_root=$(mktemp -d -t dearmachine-live-test.XXXXXXXX)
   chmod 0700 "$runtime_root"
   ```

3. Build fresh DearMachine Client and Agent Manager binaries beneath that root. Set
   `DEARMACHINE_HOME` to an isolated directory beneath it. Resolve mct-agent to
   an absolute path. Unset `MACHTIANI_SESSION_ID` and
   `MACHTIANI_SESSION_TEMP_ROOT` before initialization, inspection, and cleanup
   commands.
4. Create a temporary AgentMail sender and receiver. Verify both by exact ID,
   and use the receiver only for DearMachine Client intake and the sender only for
   delivery and reply receipts. AgentMail may assign different sender-side and
   receiver-side thread IDs; record both for every case.
5. Inspect organization, pod, and inbox send, receive, and reply allow-lists
   before sending. Add only exact entries required for the temporary pair,
   record every entry created by this run, and never remove a pre-existing
   entry. The user's protected Gmail address must never be removed. Confirm the
   sender is accepted before the first protocol message.
6. Never print, copy, interpolate, or retain credential contents. Load the
   AgentMail credential from its authorized location without exposing it in
   process arguments, logs, prompts, evidence, or shell history.
7. Create two isolated projects beneath the runtime root: an ordinary
   disposable Git/mct project for Phase 1 and a separately initialized
   disposable entry point for Phase 2. Record each UUID-backed mct store. Keep
   phase-specific SQLite databases, PID files, logs, and process boundaries so
   Phase 1 messages cannot affect Phase 2 turn counts or session ordering.
8. Create the private evidence directory at:

   ```text
   <DearMachine-source>/.scratch/continuous-intake-evaluations/<UTC-run-timestamp>/
   ```

   Keep credentials out. Record safe case IDs, private message/thread/session
   IDs, UTC timestamps, client logs, database samples, process samples, reply
   receipts, checkpoint snapshots, Git state, and cleanup results. Verify that
   `.scratch/` and runtime evidence remain absent from `git status`.

For both phases, pass every important path explicitly. The Phase 1 launch must
include this effective argument set, with `--concurrency 3` or another recorded
value of at least two:

```text
dearmachine
  --inbox-id <temporary-receiver-id>
  --project <phase-1-disposable-project>
  --config <runtime-root>/dearmachine.toml
  --agent-manager <runtime-root>/agent-manager
  --mct-agent <absolute-mct-agent-path>
  --db <runtime-root>/phase-1-dearmachine.db
  --pidfile <runtime-root>/phase-1-dearmachine.pid
  --entry-point-repo ""
  --entry-point-prompt <runtime-root>/phase-1-unused-prompt.md
  --concurrency 3
  --poll-interval 10s
  --verbose
```

Run the client continuously rather than with `--once` for the main Phase 1
scenario. Verify its PID, command line, paths, first successful poll, and lack
of competing receiver pollers before sending.

### Phase 1: arrival during an active worker batch

Use two new receiver-side threads, A and B, with safe versioned case IDs and no confidential content.

1. Send A a substantial research, repository-analysis, or comparison task
   known to keep the selected mct-agent backend working for several minutes.
   Record the exact prompt privately. Poll `mct-agent session list --json`
   until A's mapped session reports `state='running'`. Cross-check that
   A's `pending_messages` row is `running` and a descendant
   `mct-agent ... run` process exists. If A completes before B can be observed,
   classify the attempt inconclusive and retry with new thread IDs and a
   suitably longer task.
2. While A is still running, send a concise, independent B request. B must be
   useful but short enough that its reply can arrive while A remains active.
   Record delivery time, both AgentMail thread IDs, receiver message ID, and
   the session ID allocated through `thread_sessions`.
3. Starting immediately after B delivery, sample about every two seconds while
   transitions are expected. At one UTC timestamp, collect all of:

   - the full B row from `pending_messages`, including `message_id`,
     `thread_id`, `sequence`, and `state`;
   - `SELECT COUNT(*) FROM pending_messages WHERE state = 'running'`;
   - A and B session IDs and states from mct-agent's session list;
   - a full recursive `/proc` descendant walk from the recorded DearMachine Client
     PID, counting every descendant whose command line is `mct-agent ... run`;
     do not stop at a fixed generation, and distinguish maintenance sync/run
     children by arguments; and
   - new verbose DearMachine Client poll lines and sender-side AgentMail reply
     receipts.

   The schema's possible pending states are `received`, `running`, and
   `result_ready`. The durable-claim assertion needs a B row in any of those
   states while A is still running; `state = 'running'` is specifically the
   active email-worker count. Retain raw samples rather than only their maxima.
4. Prove the core user scenario in this order: A is running, B is delivered, a
   durable B row appears, B's session becomes running, and B's sender-visible
   reply arrives, all before A's reply. A reply alone or a later B reply is not
   sufficient. B must not wait for A.
5. **Same-thread continuation delivery:** With a fully isolated AgentMail
   sender/receiver pair, reply-type messages (Drafts API `InReplyTo`, raw-send
   `In-Reply-To`/`References` headers, or reply-natural derivation) thread
   correctly on the sender side but are not delivered to the receiver inbox.
   Fresh drafts are delivered, but receive a new sender-side thread ID and
   therefore create a new receiver thread. This makes live same-thread FIFO
   observation inconclusive through AgentMail alone. Attempt one reply-natural
   continuation A2 while A1 is still running, then verify delivery by listing
   the receiver inbox. If A2 is not delivered after about 60 seconds, classify
   the FIFO assertion as `INCONCLUSIVE`, not as a product failure; record both
   sender-side thread IDs and the non-delivered message ID in the evidence, and
   rely on `TestProcessOnceLimitsConcurrentThreadsAndPreservesThreadFIFO` and
   the `concurrency_test.go` suite for the FIFO verdict. Do not retry with fresh
   drafts while claiming they are continuations: a fresh draft is a new thread
   by definition. If A2 is delivered, confirm it is durably claimed at the next
   in-batch poll but does not start a second A run before A1 completes. After A1
   completes, A2 must advance the same mapping from sequence one to sequence
   two without a gap. B must independently advance from sequence zero to one
   and must not be serialized behind A.
6. Across every sample, assert that both the SQLite `running` count and the
   recursive-process count are no greater than the recorded `--concurrency`.
   Explain any short-lived mismatch by timestamp and process arguments; do not
   discard it. At no point may one thread have two active run children.
7. After all replies, confirm each inbound message appears exactly once in
   `processed_messages`, each has exactly one outbound reply, thread A is at
   sequence two, thread B is at sequence one, and `pending_messages` is empty.
   Relative to the Phase 1 baseline, require exactly two new source sessions
   and two new `thread_sessions` mappings. Concurrent completion across
   different threads is unconstrained except for the required B-before-A1
   observation.

Do not manufacture an arrival at the exact batch-stop race. If one occurs
naturally, distinguish an unread remote message from an already durable
`pending_messages` row. Do not expect a final extra poll. A durable row left by
the stopping claim loop must be recovered at the next `ProcessOnce` entry and
processed exactly once; an unclaimed remote arrival belongs to the next normal
poll.

### Phase 2: accumulated-turn maintenance gate

Stop Phase 1 cleanly and record its final boundary. Initialize a fresh disposable entry point
beneath the runtime root. Set both `--project` and `--entry-point-repo` to it and pass its seeded
`documentation/update-prompt-template.md` as `--entry-point-prompt`. Keep the explicit receiver,
config, Agent Manager, and mct-agent paths; substitute `<runtime-root>/phase-2-dearmachine.db`
and `<runtime-root>/phase-2-dearmachine.pid`; and preserve `--poll-interval 10s`, `--verbose`,
and the recorded concurrency.

The production default is 20 completed turns. For a tractable live run, pass a
smaller positive `--maintenance-min-turns`, such as `3`, and record the chosen
threshold in the report. Negative values must be rejected. If the flag is not
explicitly set, `DEARMACHINE_MAINTENANCE_MIN_TURNS` is the fallback; an
explicit flag takes precedence. A value of `0` disables the gate and restores
legacy cadence, so do not use zero for this gate test.

Before sending, record the threshold, absence or complete contents of
`state/sync-trigger.json`, source and artifact Git heads/status, internal-README
hash, mct session list, Phase 2 database counts, and absence of temporary
forks. The source and entry-point repo must be clean apart from any recorded
generator-owned initialization baseline.

#### Natural-user A-B-C-D cases

Send these as four distinct new threads in exact order. Wait for each ordinary
email reply and capture the subsequent gate decision before sending the next.
Do not send same-thread follow-ups because they change session `updated_at`.

- A, `power-modes-v2`: ask for the practical difference between laptop sleep,
  hibernation, and shutdown.
- B, `recurring-memory-v2`: ask how to check current memory use, identify
  whether one application is consuming most of it, and trace usage to the
  responsible application.
- C, `recommendation-first-v2`: ask for a ZIP versus tar.gz comparison for
  sending a folder, noting a preference for the recommendation before the
  supporting details.
- D, `current-time-tail-v2`: ask for the current day of the week and local
  time. D is the shared newest tail and must remain unreviewed.

At every boundary, compute and record the processed-message count using the
checkpoint `updated_at` and the exact `processed_at > since` predicate. Also
record the total Phase 2 processed rows so the one-message/one-turn mapping is
auditable. When the checkpoint is absent, retain that absence and the
bootstrap Git/session boundary separately; do not invent a checkpoint or edit
the state file.

#### Below-threshold boundaries

After each completed case for which the accumulated count is below the chosen
threshold:

- require a log line equivalent to `sync trigger: N accumulated turn(s) below
  threshold M; skipping maintenance`, with the observed N and configured M;
- confirm no review fork, maintenance run, delete, sync, source commit, or
  artifact revision occurred;
- confirm session detection was skipped: no `mct-agent session list` or
  Git-boundary lookup belongs to that gate cycle, and no `reviewing source`
  line appears; and
- preserve `state/sync-trigger.json` as absent or unchanged. If present, its
  `turns_accumulated` remains below threshold and its session/`updated_at`
  checkpoint does not advance.

The exact skip log is the primary evidence that the return occurred before
session detection. If OS-level process observation cannot reliably catch a
short-lived list command, report that limitation; do not infer a call merely
from an independently captured session-list sample used by the tester.

#### Threshold crossings and rolling boundary

Once the count reaches or crosses the threshold, require exactly one pipeline
for that crossing: one fork of the oldest eligible source session, one update
prompt run on the fork, one fork delete, and one
`mct-agent sync --include-docs`. Confirm the fork is absent after cleanup,
`state/sync-trigger.json` advances to the reviewed source session with its
exact `updated_at`, and `turns_accumulated` is reset to `0` only after the full
pipeline succeeds. The newest source session must not be forked, checkpointed,
or otherwise reviewed.

With threshold 3 and ordered A-B-C-D delivery, the expected rolling evidence
is:

```text
A completes -> below threshold; A is held
B completes -> below threshold; A and B are held
C completes -> threshold crossing reviews A; B and C are held
D completes -> next crossing may review B; C and newest D remain held
```

Derive the second crossing from the recorded `processed_at > checkpoint` count
rather than assuming it. If it has not reached three, require another skip and
leave B unreviewed. In every case, a later crossing must review the oldest
eligible session while preserving D as the newest held tail. Observe at least
one additional successful poll without new mail and prove that the same
crossing does not start a duplicate maintenance pipeline.

A maintenance review that makes no documentation commit is a legitimate
`no-op`, provided fork, run, delete, documentation-aware sync, and checkpoint
advance all succeed. Gate assertions concern when maintenance runs, not what
the model chooses to commit. If maintenance fails, the checkpoint must not
advance; preserve the failed state and cleanup evidence rather than manually
repairing or committing output.

### Required outcome classification

Report Phase 1 A1, A2, and B and every Phase 2 case in separate short sections,
not a wide table. Each applicable section must include:

- safe case ID, sender-side and receiver-side thread IDs, and message ID;
- source-session order, session ID, and relevant UTC timestamps;
- trigger classification: `below-threshold`, `reviewed`, or `held-tail`;
- maintenance classification: `not-run`, `no-op`, `update`, or `failed`;
- complete checkpoint before and after, including `turns_accumulated`;
- fork ID and cleanup outcome;
- source/artifact changes when maintenance ran; and
- email delivery and sender-visible reply outcome.

Include the chosen threshold, each computed turn count, exact skip/review log
lines, fork/run/delete/sync counts, Phase 1 maximum SQLite/process worker
counts, pending-row samples, and the B-before-A reply timeline.

Give four separate verdicts:

1. **Intake decoupling:** pass only if B is durably claimed, starts, and replies
   while A1 is still running, with exactly-once processing and reply delivery.
2. **Maintenance gating:** pass only if below-threshold cycles perform no
   session detection or maintenance, every observed crossing performs exactly
   one complete pipeline, successful maintenance resets
   `turns_accumulated`, checkpoints only the oldest eligible source, and leaves
   the newest tail untouched. A `no-op` review can pass.
3. **FIFO and concurrency-limit preservation:** pass only if A1 precedes A2
   without a gap or overlap, B is not cross-thread serialized, no thread has
   two active runs, and neither active-count measure exceeds `--concurrency`.
   If AgentMail reply delivery cannot be observed with the isolated pair,
   classify the live FIFO portion as inconclusive per Phase 1 step 5 and base
   the verdict on the unit-level FIFO suite together with any observed
   cross-thread concurrency.
4. **Isolation and cleanup:** pass only if normal inboxes, allow-lists, runtime
   state, mct stores, source worktree, and unrelated processes are unchanged,
   and every run-created live resource is removed.

Classify a bounded timeout as inconclusive only after retaining the latest
database, session, process, log, checkpoint, Git, and inbox state. A failed
assertion is evidence; do not modify the tested checkout during diagnosis.

### Finish and refine

Preserve the approved private report before applying the shared teardown. Stop
the temporary client, prove all descendants are gone, remove only allow-list
entries recorded as run-created without removing the user's protected Gmail
address, permanently delete both temporary inboxes by
verified exact ID, delete only the two verified disposable UUID-backed mct
stores, and validate before deleting the exact runtime root.

Maintenance and ordinary agent work can involve several model calls. Treat a
live process, advancing trajectory, new durable row, changing session state,
or bounded Git transition as progress rather than relying on an exact response
time.

The isolated sender/receiver pair cannot reliably deliver reply-type messages;
treat Phase 1 continuation delivery failure as an AgentMail API limitation, not
as evidence about the product.

After the exercise, update this runbook only for durable operational lessons:
missing observations, misleading assertions, unsafe cleanup assumptions, or
ambiguous steps. Do not encode transient IDs, exact response times, model
wording, credentials, or one machine's private paths.
