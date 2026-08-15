# Concurrent DearMachine Client Sessions Runbook

This document is a prompt for a capable local testing agent, not an executable
test script. It compares the sequential compatibility mode with the default
per-thread worker pool by processing three real email threads that are waiting
before each client starts.

Use [`temporary-instance.md`](./temporary-instance.md) for provisioning,
isolation, observation, evidence handling, and teardown. Those requirements are
part of this protocol. Do not change product code, prompts, documentation, or
tests in response to the exercise.

## Prompt to give the testing agent

Conduct one isolated concurrent-session DearMachine Client test using a temporary
sender inbox, temporary receiver inbox, disposable project, database, PID file,
configuration, Agent Manager state, and runtime root. Preserve pre-existing
workspace state and retain no credentials.

### Required harness preparation

Apply these details in addition to the shared temporary-instance reference:

1. Unset `MACHTIANI_SESSION_ID` and `MACHTIANI_SESSION_TEMP_ROOT` before every
   machtiani invocation, including initialization, inspection, and cleanup.
2. In the new disposable Git project, run `machtiani init` before creating the
   initial commit. Then create that commit before the first `machtiani sync`;
   otherwise sync fails with `no commits yet`.
3. Inspect the organization, pod, and inbox receive/reply allow-lists before
   sending. Ensure the temporary receiver address is present in the applicable
   organization receive and reply allow-lists, creating only the exact
   temporary entries the run needs.
   Record which entries were newly created so teardown cannot remove an
   existing rule. Never remove the user's protected Gmail address.
4. Disable entry-point maintenance with `--entry-point-repo ""`. Use the same
   temporary sender, receiver, and disposable project for both phases, while
   recording the database and session-count boundary before each phase.

For every phase, create three independent email threads by sending three real
emails from the temporary sender to the temporary receiver. Give them distinct,
phase-specific subjects and small meaningful tasks. Send them back-to-back
before starting DearMachine Client; do not start the client until all three are
visible as unread in the receiver inbox. Record both sender-side and
receiver-side message/thread IDs privately.

### Phase 1: sequential baseline

1. Record the starting count and IDs of mct sessions and `thread_sessions` rows.
2. Send the three baseline emails back-to-back, confirm all three are unread,
   then launch the isolated client with `--concurrency 1` and the explicit paths
   required by the shared reference. A bounded `--once` run is preferred.
3. Observe each email through claim, machtiani run, reply, completion, and remote
   processed marking. A follow-up is outside this protocol; each new thread must
   advance cleanly from sequence zero to sequence one without a gap or reorder.
4. Confirm all of the following before stopping the phase:

   - exactly three new mct sessions and three new thread mappings exist;
   - each receiver-side thread maps to one session at sequence one;
   - every inbound message appears exactly once in `processed_messages` and has
     exactly one outbound reply;
   - all three replies are visible in the temporary sender inbox in their
     corresponding threads; and
   - `SELECT COUNT(*) FROM pending_messages` returns zero at shutdown.

Retain the phase boundary, IDs, timestamps, session mappings, database counts,
and reply proof without retaining credentials or unnecessary message content.

### Phase 2: three-worker processing

1. Keep the client stopped. Record the new session and database boundary, then
   send three new concurrent-phase emails with distinct subjects back-to-back.
   Confirm all three are unread before startup.
2. Launch the same isolated client with `--concurrency 3` and otherwise identical
   explicit configuration.
3. While work is active, poll `SELECT COUNT(*) FROM pending_messages WHERE
   state = 'running'` against the phase database about every two seconds as the
   primary worker-slot measure. Record the maximum simultaneous `running` row
   count. At the same samples, cross-check it with a full recursive walk of all
   descendants in `/proc` from the DearMachine Client PID, counting descendants whose
   command line is `machtiani ... run`; do not cap the walk at a fixed generation
   depth. Classify transient sync/run children by their arguments.
4. Repeat every sequential-phase assertion for the three new thread/message IDs:
   exactly three new sessions, one mapping and ordered sequence advancement per
   thread, exactly-once processing and reply, all three sender-visible replies,
   and zero pending rows at shutdown.
5. Additionally assert that the observed active email worker count never exceeds
   three: the maximum SQLite `state = 'running'` row count is at most three, and
   the full descendant-process-walk `machtiani ... run` maximum is also at most
   three as a cross-check. Concurrent completion order across the three threads
   is unconstrained.

The phase fails if the pool exceeds three active threads, one session has two
active machtiani children, any message or reply is duplicated, a sequence has a
gap, a reply is absent, or a pending row remains. A timeout is inconclusive only
after retaining the last process, database, session, and inbox state.

### Teardown and report

Follow the shared teardown exactly, for both success and failure:

1. Stop the client gracefully and prove its PID, PID file, machtiani children,
   Agent Manager children, and backend workers are gone.
2. Preserve the concise approved evidence report first. It may contain private
   temporary IDs and timestamps, but no credentials.
3. Remove only allow-list entries created by this run and confirm their absence.
   Never remove the user's protected Gmail address.
4. Delete both temporary inboxes—the sender and receiver—by their exact verified
   IDs, then confirm both are absent.
5. Resolve and delete only the disposable project's exact UUID-backed mct store,
   after proving the store belongs to the recorded temporary project and no
   process uses it.
6. Validate and delete only the exact run-created runtime root. Confirm normal
   DearMachine Client state, normal inboxes, unrelated allow-list entries, and the
   source workspace remain unchanged.

Report source revisions, backend order, phase boundaries, the sequential and
concurrent assertions, maximum observed active count, pass/fail/inconclusive
classification, retained evidence location, every cleanup result, and proof
that normal state was preserved.
