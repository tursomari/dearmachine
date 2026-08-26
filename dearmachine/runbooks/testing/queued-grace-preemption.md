# Queued Grace Preemption QSE

This document is a prompt for a capable local testing agent, not an executable
test script. It proves that DearMachine dispatches every durable same-thread
email to one machtiani session while using one inbox poll interval as a grace
period before stopping a turn that has a newer queued successor.

Use [`temporary-instance.md`](./temporary-instance.md) for provisioning,
isolation, evidence, and teardown. This QSE is a native diagnostic of intake,
dispatch, and graceful session continuation; it does not claim container or
deployment coverage. Do not change product code, tests, documentation, or
prompts in response to the exercise. Report observations and failures.

## Prompt to give the testing agent

Conduct one isolated QSE named `queued-grace-preemption`. Use a freshly built
DearMachine Client and Agent Manager, an authorized live transport, one
disposable receiver, a disposable project and runtime root, and one explicitly
selected real backend. Never use or stop the normal DearMachine Client, its
inbox, database, entry point, Agent Manager home, or machtiani project store.

### Inputs and boundaries

1. Record the DearMachine and machtiani revisions, complete worktree status,
   selected backend and model, transport, credential-file locations, normal
   DearMachine processes, and normal runtime paths. Record paths and credential
   variable names only; never print or copy credential values.
2. Provision and verify a disposable receiver. Prefer a disposable sender too.
   An authorized external correspondent is acceptable when the transport
   cannot deliver reply-natural continuations between two temporary inboxes.
   Do not alter a protected account-wide policy. Create only exact-address,
   run-scoped policy entries when necessary, and journal every created resource
   before sending.
3. Create a mode-`0700` runtime root with `mktemp -d`. Keep the database, PID
   file, logs, binaries, backend configuration, Agent Manager home, evidence,
   and disposable Git/machtiani project below it. Unset inherited
   `MACHTIANI_SESSION_ID` and `MACHTIANI_SESSION_TEMP_ROOT`. Disable entry-point
   maintenance with `--entry-point-repo ""`.
4. Build `dearmachine` and `agent-manager` from the exact revision under test.
   Initialize and sync the disposable project with the exact machtiani binary
   under test. Configure only the selected backend. Load its credential from an
   operator-owned file through the backend's supported environment boundary;
   do not place the secret in TOML, argv, logs, evidence, or versioned files.
5. Use `--concurrency 1`, `--poll-interval 5s`, and `--verbose`. After seeding
   the canonical session as described below, keep the client stopped until the
   two preemption messages have both been delivered and verified. This forces
   both messages to be available to one poll and distinguishes queue-aware
   preemption from arrival-only preemption.

### Seed the session and prepare the co-claimed pair

1. Send a short seed message as a new thread with a unique safe case ID. Ask for
   one read-only result. Launch the temporary client, require its reply, and
   record the canonical session reference in the reply footer. Stop the client
   and prove its PID and descendants are gone. The seed is sequence 1 and
   establishes the same user-visible continuation mechanism used in practice.
2. Reply to the seed response with message 2. Quote the stable DearMachine
   footer when the sender or transport does not preserve quoted reply content
   automatically. Ask for a read-only operation that remains active for at
   least 20 seconds, such as waiting 20 seconds before inspecting and reporting
   the disposable project's current branch. It must not detach work or send an
   early reply.
3. Immediately send message 3 through the same reply-natural continuation
   mechanism. Its body must clearly redirect message 2 and ask for one short,
   distinguishable answer without the requested wait. Preserve the same quoted
   session footer.
4. Read messages 2 and 3 back from the receiver before relaunching DearMachine.
   Require two distinct receiver message IDs, ordered delivery, unread
   eligibility, and evidence that both map to the seeded canonical session.
   Ordinarily they have one receiver thread ID. A provider may report different
   thread IDs only when the quoted footer is present and the client's durable
   alias/session state proves canonical continuity. If neither condition is
   available, retain the provider evidence and classify the QSE `INCONCLUSIVE`;
   do not substitute an unrelated fresh thread while claiming a same-session
   result.

### Launch and observe

1. Launch the temporary client with every path explicit. Confirm its PID,
   descendant processes, database, inbox, project, Agent Manager home, backend
   configuration, and first poll all belong to the QSE. Confirm no second
   process polls the disposable receiver.
2. Sample safe verbose logs, read-only SQLite state, recursive descendants, and
   `machtiani session list --json` about once per second during the expected
   transition. Use observed state and a bounded deadline rather than a fixed
   long sleep. Record timestamps for:

   - both messages becoming durable with sequences 2 and 3;
   - the sequence-2 resumed `machtiani run` launch;
   - the graceful stop request and sequence-2 child exit;
   - the sequence-3 resumed `machtiani run`; and
   - the one outbound reply produced by the preemption pair.

3. Require the sequence-2 run to remain active until approximately one
   configured poll interval after its launch. Scheduling and sampling jitter
   are expected, but an immediate stop on the initial claim fails the grace
   assertion. The stop must occur without waiting for another inbox poll
   because message 3 was already queued.
4. Require the sequence-2 child to exit before the sequence-3 child starts.
   Both argv values must use `--resume <canonical-session-id>`; the latter's
   `--prompt` must contain only message 3 as the new prompt. At no time may two
   machtiani run children for the thread coexist.
5. Inspect the disposable machtiani conversation. It must contain the seed as
   the initial user goal followed by messages 2 and 3 as consecutive user
   inputs in the same session. Do not treat an attached renderer labeling the
   initial turn `GOAL` as absence of that user input; use the persisted
   conversation or structured session output as the evidence.
6. Verify the selected backend through its functional Agent Manager health
   result and closed ticket or equivalent managed-worker evidence. A binary on
   `PATH` or a successful email alone is insufficient.

### Pass criteria

Report `PASS` only when all of these hold:

- the seed was processed as sequence 1, and messages 2 and 3 were both durably
  claimed before the sequence-2 worker finished;
- sequence 2 launched, remained active through the grace interval, then was
  gracefully stopped because sequence 3 was already queued;
- sequence 2 is absent from `pending_messages` and `processed_messages` and is
  present exactly once in `skipped_messages` with reason
  `preempted by newer email`;
- sequence 3 resumed the same canonical session, is present exactly once in
  `processed_messages`, and produced the only substantive reply from the
  preemption pair;
- `thread_sessions.sequence` is 3, no `(thread_id, sequence)` is duplicated,
  and no pending row remains;
- the machtiani conversation contains all three user inputs in order;
- the selected real backend is proven by managed-worker evidence; and
- the normal DearMachine installation and all pre-existing workspace state
  remained unchanged.

Classify a transport continuation-delivery limitation or a first task that
finishes inside the five-second grace as `INCONCLUSIVE`, with the last complete
evidence. Classify any contrary product state as `FAIL`.

### Teardown and report

Stop the exact temporary client and prove its PID, PID file, descendants, Agent
Manager workers, and backend workers are gone. Retain a concise private report
under `.scratch/queued-grace-preemption-evaluations/<UTC-timestamp>/` containing
only the safe revisions, configuration names, timestamps, state transitions,
redacted process/argv evidence, verdict, and resource journal. Never retain
credentials. Delete only the exact temporary policy entries, inboxes, machtiani
store, and validated runtime root created by this run, then prove each is gone.
