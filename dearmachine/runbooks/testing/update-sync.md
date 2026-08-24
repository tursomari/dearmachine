# Agent-Guided Live Checkpoint and Update-Sync Protocol

This document is a prompt for a capable local agent, not an executable test
script. It evaluates the DearMachine Client's rolling session checkpoint, maintenance
fork, documentation sensitivity, and internal-README update sync through real
email threads and a disposable entry-point repository.

Follow [`temporary-instance.md`](./temporary-instance.md) for all shared
provisioning, isolation, observation, evidence, and teardown requirements. Do
not run this protocol against the normal inbox, entry point, database, or mct
project store.

## Prompt to give the testing agent

Conduct a live, human-readable evaluation of rolling entry-point maintenance.
Do not replace this protocol with a timing-dependent shell script. Use commands
to inspect state, but decide progress from observed transitions and retain a
clear evidence trail.

Test the checked-out product revision as-is. Do not fix code, edit prompts,
stage files, or commit to the DearMachine source repository in response to the
exercise. Changes and commits made by maintenance inside the disposable entry
point are expected test output.

### Intended behavior

The DearMachine Client holds the newest source session until another distinct session
arrives:

```text
A arrives -> no maintenance; A is held
B arrives -> review, sync, and checkpoint A; B is held
C arrives -> review, sync, and checkpoint B; C is held
D arrives -> review, sync, and checkpoint C; D is held
```

A successful review may legitimately make no documentation change. Even then,
`machtiani sync --include-docs` must succeed and the reviewed source session
must be checkpointed. The test therefore reports checkpoint mechanics and
documentation sensitivity separately.

### Provision the sync-specific instance

1. Apply the shared containerized temporary-instance procedure with a fresh
   sender/receiver inbox pair, runtime root, OCI image, packaged Agent Manager, configuration,
   database, PID file, and isolated `DEARMACHINE_HOME`. Use the receiver only
   for the temporary DearMachine Client and the sender only for test delivery and
   reply observation.
2. Create an empty entry-point directory beneath the runtime root and initialize
   it with the tested image/client. Pass the mounted machtiani path and a
   snapshot directory beneath the runtime root.
3. Resolve and record the disposable entry point's mct UUID, store path,
   source Git history, internal-README artifact repository, artifact Git
   history, and initialization snapshots.
4. Launch the temporary client with the disposable entry point supplied as
   both `--project` and `--entry-point-repo`. Pass its seeded
   `documentation/update-prompt-template.md` through `--entry-point-prompt`
   and pass `--maintenance-min-turns 0`; this protocol exercises the legacy
   per-poll rolling cadence, while `continuous-intake.md` owns turn-gate and
   multi-source backlog-drain coverage.
5. Before sending, prove:
   - `state/sync-trigger.json` does not exist;
   - no ordinary email sessions exist in the disposable mct project;
   - the source Git repository is clean;
   - artifact Git status is recorded as the baseline, including any
     generator-owned untracked file left by initialization;
   - only the two expected initialization artifact revisions exist; and
   - the first client poll succeeds.

Create a private evidence directory at:

```text
<DearMachine-source>/.scratch/update-sync-evaluations/<UTC-run-timestamp>/
```

For every boundary below, record the safe case ID, sender-side and receiver-side
thread IDs, source session ID and `updated_at`, relevant DearMachine Client log
lines, checkpoint contents or absence, source and artifact Git heads/status,
internal-README hash, and temporary-fork presence or absence. Never copy
credentials.

### Stable prompt sets

Use the natural-user set by default. It exercises the behavior the DearMachine Client must
handle in normal use: inferring durable value without test-specific wording.
Use the todo-maintenance set when evaluating the seeded `todo/` workflow. Use
the explicit-control set only when a baseline with unambiguous retention
instructions is specifically useful. Record the selected set and its case
versions.

Send its cases as new email threads in exact A-B-C order, followed by the shared
D tail. Do not mix sets in one run. Do not reply in a test thread: a
continuation changes the source session's `updated_at` and makes the ordering
evidence inconclusive. If a prompt unexpectedly requires clarification, record
the case as inconclusive and stop before sending the next case.

#### Priority set: natural user requests

This set tests whether maintenance infers durable value from an ordinary
recurring problem rather than reacting to words about documentation or
retention.

##### Case A: `power-modes-v2`

```text
What's the practical difference between putting a laptop to sleep, hibernating
it, and shutting it down?
```

Expected sensitivity: no durable documentation update. This is an ordinary
general-information request without a recurring machine workflow.

##### Case B: `recurring-memory-v2`

```text
My computer sometimes slows down, and I often want to know whether one
application is consuming most of the memory. Can you check the current memory
usage and trace it back to the responsible application?
```

Expected sensitivity: maintenance recognizes the recurring operational need
and commits a reusable memory-diagnosis procedure or equivalent guidance. A
snapshot of current consumers may support the immediate answer, but retaining
short-lived process names and measurements as durable documentation should be
reported as over-eager sensitivity unless there is a clear continuing reason.

##### Case C: `recommendation-first-v2`

```text
Can you compare ZIP and tar.gz for sending a folder to someone? I usually find
it easier when the recommendation comes first and the supporting details
follow.
```

Expected sensitivity: observational only. Either a justified update or a
justified no-op is recorded, with attention to whether one naturally phrased
preference is enough to establish a durable convention.

#### Todo-maintenance set

This set tests whether maintenance creates useful reminders and deferred-work
entries while declining to retain a trivial fact. Use it when validating the
seeded `todo/` directory and the `Maintain todo/` maintenance instruction.
Because this protocol intentionally uses the entry point as both `--project`
and `--entry-point-repo`, each initial source prompt explicitly forbids file
and durable-state changes while answering. This isolates later maintenance
from work the ordinary source agent could otherwise perform directly. If an
initial source session changes or commits the entry point, classify the attempt
as invalid, stop before the next case, and restart from a fresh instance.

##### Case A: `explicit-reminder-v2`

```text
This is an initial read-only answer: do not modify files or durable machine
state while answering this email. I need to renew my domain registration
before the end of the month. Tell me the current date and confirm what should
be remembered. A later entry-point maintenance review, not this initial
answer, should retain the reminder for future sessions.
```

Expected sensitivity: when B releases A for review, maintenance adds a concise,
actionable todo entry for renewing the domain before the end of the month.

##### Case B: `deferred-decision-v2`

```text
This is an initial read-only answer: do not modify files or durable machine
state while answering this email. I'm trying to choose between two backup
strategies for my home directory: rsync to an external drive, or a managed
cloud backup service. Outline the trade-offs; I'm not ready to decide yet. A
later entry-point maintenance review, not this initial answer, should retain
the open decision and its useful context for future sessions.
```

Expected sensitivity: when C releases B for review, maintenance preserves the
open backup-strategy decision and its useful context as a second todo entry.

##### Case C: `no-reminder-content-v2`

```text
This is an initial read-only answer: do not modify files or durable machine
state while answering this email. What is the capital of Portugal?
```

Expected sensitivity: when D releases C for review, maintenance makes no todo
or other durable entry for this resolved factual question.

#### Optional control set: explicit boundary controls

##### Case A: `ephemeral-v1`

```text
This is a one-time, read-only inspection. Do not modify files and do not retain
this as durable guidance. Inspect the top-level entry-point README and briefly
describe the purpose of its main directories.
```

Expected sensitivity: no durable documentation update when A is eventually
reviewed.

##### Case B: `durable-evidence-v1`

```text
This turn is read-only: do not modify files now. For future sessions on this
temporary machine entry point, retain this durable machine-level reporting
convention: clearly separate directly observed evidence from inference. Briefly
explain why that convention is useful.
```

Expected sensitivity: when B is later reviewed, maintenance adds and commits a
concise durable convention or equivalent guidance, and the internal README is
updated from that new entry-point commit.

##### Case C: `borderline-summary-v1`

```text
Do not modify files. I often find a short status summary before detailed
results useful. Treat this as an observation rather than an explicit request
to document it, and briefly discuss when that presentation would help.
```

Expected sensitivity: observational only. Either a justified update or a
justified no-op is recorded for comparison with later runs.

#### Shared Case D: `current-time-tail-v2`

```text
What day of the week is it, and what is the current local time?
```

Expected trigger behavior: D remains held and uncheckpointed at the end.

### Exercise the rolling boundary

#### After A completes

- Confirm A has one successful source session and one same-thread email reply.
- Observe at least one subsequent successful poll.
- Confirm no maintenance fork started, no checkpoint exists, source and
  artifact Git heads and status are unchanged from their recorded baselines,
  and the internal-README hash is unchanged.
- Classify A's current state as `below-threshold`.

#### After B completes

- Confirm B created a second distinct successful source session.
- Observe maintenance select A, not B. Logs must identify A as the reviewed
  source and state that a newer session is held.
- Confirm the temporary fork of A completes and is deleted before sync.
- Confirm documentation sync succeeds and the checkpoint records A's exact
  session ID and `updated_at`.
- Confirm B remains newer than the checkpoint and unreviewed.
- For the natural-user and explicit-control sets, source and artifact Git heads
  and the internal README remain unchanged. A checkpoint advance with no
  documentation commit is a successful no-op, not a failure.
- For the todo-maintenance set, confirm maintenance commits a domain-renewal
  entry without modifying `todo/README.md`, documentation-aware sync creates a
  corresponding artifact revision, and the internal README exposes the todo.

#### After C completes

- Confirm maintenance selects B while holding C.
- Confirm B's temporary fork completes and is deleted.
- Confirm B's expected durable content for the selected set produces a new
  commit in the disposable entry point, followed by a successful
  documentation-aware sync.
- Confirm the artifact repository has a new revision associated with that
  entry-point commit. For the priority natural-user set, the internal README
  must convey a reusable way to attribute memory use to an application;
  separately classify any retained volatile process snapshot. For the optional
  explicit-control set, it must convey the evidence-versus-inference
  convention. For the todo-maintenance set, the new source commit must add the
  deferred backup-strategy entry while preserving the domain-renewal entry and
  `todo/README.md`; the corresponding artifact revision must expose both.
- Confirm the checkpoint now records B, even though the maintenance commit and
  artifact sync occurred after C's predecessor timestamps. This is the live
  regression proof that Git sync time did not hide B.

#### After D completes

- Confirm maintenance selects C while holding D.
- Record whether C produces a source commit and artifact update or a no-op. Do
  not make the natural-user or explicit-control mechanical result depend on
  this borderline decision. For the todo-maintenance set, C must be a no-op and
  must not add a Portugal-related entry anywhere in the entry point.
- Confirm the checkpoint records C.
- Observe at least one more successful poll without sending another thread.
- Confirm no maintenance fork selects D, the checkpoint remains C, and D is
  the sole intentionally held source session.

### Required outcome classification

Report each case in its own short section. Avoid a wide table because session
and thread identifiers make it difficult to read. Include:

- safe case ID;
- source-session order and timestamps;
- trigger classification: `below-threshold`, `reviewed`, or `held-tail`;
- maintenance classification: `not-run`, `no-op`, `update`, or `failed`;
- checkpoint before and after;
- source and artifact Git changes;
- todo file names, hashes, and concise content descriptions when testing
  todo-maintenance;
- temporary-fork cleanup; and
- email reply outcome.

Give separate verdicts:

1. **Checkpoint mechanics:** pass only if A, B, and C are reviewed and
   checkpointed in that order while D remains held.
2. **Update sync:** pass only if every reviewed case runs documentation sync.
   For the natural-user and explicit-control sets, A remains a no-op and B
   produces a source commit plus a corresponding internal-README artifact
   revision. For the todo-maintenance set, A and B each produce a source commit
   and corresponding artifact revision, while C completes sync as a no-op.
3. **Sensitivity observation:** record C without forcing a pass/fail outcome.
4. **Isolation and cleanup:** pass only if the normal installation and source
   worktree remain unchanged and shared teardown completes.
5. **Todo maintenance:** when using the todo-maintenance set, pass only if A
   records the domain-renewal reminder, B records the deferred backup decision,
   C records nothing, both positive entries persist through the final boundary,
   `todo/README.md` remains unchanged, and the agent-selected entry structure is
   concise and internally consistent. Mark this verdict not applicable for the
   other prompt sets.

When a previous private report exists, compare the stable case outcomes and
identify any shift toward over-eager or over-conservative documentation. Do not
call a change in C alone a regression; report it as sensitivity drift with the
tested prompt set, DearMachine revision, machtiani revision, and model
selection.

### Finish and refine

Preserve the private report before applying the shared teardown. Delete the
temporary inbox, runtime root, and exact disposable UUID-backed store; do not
retain live resources merely to retain evidence.

Maintenance and documentation sync may each involve several model calls and
can take substantially longer than ordinary email handling. Treat a live
process, advancing trajectory, completed managed work, or changing Git state as
progress. Do not classify a run as hung merely because a fixed sleep elapsed.

After the exercise, update this runbook only for durable operational lessons:
missing observations, misleading assertions, unsafe cleanup assumptions, or
steps that proved ambiguous. Do not encode transient IDs, exact response times,
model wording, or one machine's private paths.
