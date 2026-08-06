# Agent-Guided Live Checkpoint and Update-Sync Protocol

This document is a prompt for a capable local agent, not an executable test
script. It evaluates DearMachine's rolling session checkpoint, maintenance
fork, documentation sensitivity, and internal-README update sync through real
email threads and a disposable entry-point repository.

Follow [`TEMPORARY_INSTANCE.md`](./TEMPORARY_INSTANCE.md) for all shared
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

DearMachine holds the newest source session until another distinct session
arrives:

```text
A arrives -> no maintenance; A is held
B arrives -> review, sync, and checkpoint A; B is held
C arrives -> review, sync, and checkpoint B; C is held
D arrives -> review, sync, and checkpoint C; D is held
```

A successful review may legitimately make no documentation change. Even then,
`mct-agent sync --include-docs` must succeed and the reviewed source session
must be checkpointed. The test therefore reports checkpoint mechanics and
documentation sensitivity separately.

### Provision the sync-specific instance

1. Apply the shared temporary-instance procedure with a fresh sender/receiver
   inbox pair, runtime root, Device Client, Agent Manager, configuration,
   database, PID file, and isolated `DEARMACHINE_HOME`. Use the receiver only
   for the temporary Device Client and the sender only for test delivery and
   reply observation.
2. Create an empty entry-point directory beneath the runtime root and initialize
   it with the freshly built client. Pass the absolute mct-agent path and a
   snapshot directory beneath the runtime root.
3. Resolve and record the disposable entry point's mct UUID, store path,
   source Git history, internal-README artifact repository, artifact Git
   history, and initialization snapshots.
4. Launch the temporary client with the disposable entry point supplied as
   both `--project` and `--entry-point-repo`. Pass its seeded
   `documentation/update-prompt-template.md` through `--entry-point-prompt`.
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
thread IDs, source session ID and `updated_at`, relevant Device Client log
lines, checkpoint contents or absence, source and artifact Git heads/status,
internal-README hash, and temporary-fork presence or absence. Never copy
credentials.

### Stable prompt corpus

Send each case as a new email thread, in this exact order. Do not reply in a
test thread: a continuation changes the source session's `updated_at` and makes
the ordering evidence inconclusive. If a prompt unexpectedly requires
clarification, record the case as inconclusive and stop before sending the next
case.

#### Case A: `ephemeral-v1`

```text
This is a one-time, read-only inspection. Do not modify files and do not retain
this as durable guidance. Inspect the top-level entry-point README and briefly
describe the purpose of its main directories.
```

Expected sensitivity: no durable documentation update when A is eventually
reviewed.

#### Case B: `durable-evidence-v1`

```text
This turn is read-only: do not modify files now. For future sessions on this
temporary machine entry point, retain this durable machine-level reporting
convention: clearly separate directly observed evidence from inference. Briefly
explain why that convention is useful.
```

Expected sensitivity: when B is later reviewed, maintenance adds and commits a
concise durable convention or equivalent guidance, and the internal README is
updated from that new entry-point commit.

#### Case C: `borderline-summary-v1`

```text
Do not modify files. I often find a short status summary before detailed
results useful. Treat this as an observation rather than an explicit request
to document it, and briefly discuss when that presentation would help.
```

Expected sensitivity: observational only. Either a justified update or a
justified no-op is recorded for comparison with later runs.

#### Case D: `tail-sentinel-v1`

```text
This is a one-time, read-only check with no durable documentation value. Report
the current branch name of the temporary entry-point repository and make no
changes.
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
- Expected sensitivity for A: source and artifact Git heads and the internal
  README remain unchanged, and Git status matches the recorded baselines. A
  checkpoint advance with no documentation commit is a successful no-op, not a
  failure.

#### After C completes

- Confirm maintenance selects B while holding C.
- Confirm B's temporary fork completes and is deleted.
- Confirm B's durable convention produces a new commit in the disposable entry
  point, followed by a successful documentation-aware sync.
- Confirm the artifact repository has a new revision associated with that
  entry-point commit and the internal README conveys the evidence-versus-
  inference convention.
- Confirm the checkpoint now records B, even though the maintenance commit and
  artifact sync occurred after C's predecessor timestamps. This is the live
  regression proof that Git sync time did not hide B.

#### After D completes

- Confirm maintenance selects C while holding D.
- Record whether C produces a source commit and artifact update or a no-op. Do
  not make the overall mechanical result depend on this borderline decision.
- Confirm the checkpoint records C.
- Observe at least one more successful poll without sending another thread.
- Confirm no maintenance fork selects D, the checkpoint remains C, and D is
  the sole intentionally held source session.

### Required outcome classification

Report each case in a table with:

- safe case ID;
- source-session order and timestamps;
- trigger classification: `below-threshold`, `reviewed`, or `held-tail`;
- maintenance classification: `not-run`, `no-op`, `update`, or `failed`;
- checkpoint before and after;
- source and artifact Git changes;
- temporary-fork cleanup; and
- email reply outcome.

Give separate verdicts:

1. **Checkpoint mechanics:** pass only if A, B, and C are reviewed and
   checkpointed in that order while D remains held.
2. **Update sync:** pass only if every reviewed case runs documentation sync,
   A remains a no-op, and B produces a source commit plus a corresponding
   internal-README artifact revision.
3. **Sensitivity observation:** record C without forcing a pass/fail outcome.
4. **Isolation and cleanup:** pass only if the normal installation and source
   worktree remain unchanged and shared teardown completes.

When a previous private report exists, compare the stable case outcomes and
identify any shift toward over-eager or over-conservative documentation. Do not
call a change in C alone a regression; report it as sensitivity drift with the
tested DearMachine revision, mct-agent revision, and model selection.

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
