# Stop-and-Resume Preemption Live Protocol

This document is a prompt for a capable local testing agent, not an executable
test script. It proves the simplified same-thread preemption path: a newer
email stops an active machtiani run, suppresses the interrupted email without a
synthetic reply, and resumes the same session using only the newer message.

Use [`temporary-instance.md`](./temporary-instance.md) for provisioning,
isolation, observation, evidence handling, and teardown. Use the polling,
same-thread continuation, and grace-window conventions in
[`continuous-intake.md`](./continuous-intake.md). Those requirements are part
of this protocol. Do not change product code, prompts, documentation, or tests
in response to the exercise.

## Prompt to give the testing agent

Conduct one isolated QSE/live evaluation named `stop-and-resume-preemption`.
Use observed state transitions and bounded deadlines, not fixed long sleeps.
The normal native DearMachine Client, its inbox, its database, its runtime
directories, and its Machtiani stores must remain untouched throughout.

### Provision the disposable instance

1. Record the source revision, complete worktree status, backend order,
   machtiani revision, normal DearMachine processes and normal runtime paths as
   required by the shared reference. Preserve every pre-existing tracked and
   untracked path.
2. Create and secure one unique root, then keep all test-local paths below it:

   ```bash
   export DEARMACHINE_TEST_ROOT="$(mktemp -d -t dearmachine-live-test.XXXXXXXX)"
   chmod 0700 "$DEARMACHINE_TEST_ROOT"
   export DEARMACHINE_HOME="$DEARMACHINE_TEST_ROOT/home"
   export DEARMACHINE_TEST_PROJECT="$DEARMACHINE_TEST_ROOT/project"
   export DEARMACHINE_TEST_DB="$DEARMACHINE_TEST_ROOT/dearmachine.db"
   export DEARMACHINE_TEST_PIDFILE="$DEARMACHINE_TEST_ROOT/dearmachine.pid"
   export DEARMACHINE_TEST_LOG="$DEARMACHINE_TEST_ROOT/dearmachine.log"
   export DEARMACHINE_TEST_TEMP="$DEARMACHINE_TEST_ROOT/tmp"
   export DEARMACHINE_TEST_EVIDENCE="$DEARMACHINE_TEST_ROOT/evidence"
   install -d -m 0700 "$DEARMACHINE_HOME" "$DEARMACHINE_TEST_PROJECT" \
     "$DEARMACHINE_TEST_TEMP" "$DEARMACHINE_TEST_EVIDENCE"
   ```

   Set `TMPDIR` to `DEARMACHINE_TEST_TEMP` for the disposable client and its
   directly launched children. Do not use a normal database, PID file,
   Agent Manager home, project, or runtime directory.
3. Build the tested revision only into the disposable root using the native
   diagnostic exception from `temporary-instance.md`; do not reuse a normal
   client binary:

   ```bash
   cd <DearMachine-source>/dearmachine
   go build -o "$DEARMACHINE_TEST_ROOT/dearmachine" ./cmd/dearmachine
   go build -o "$DEARMACHINE_TEST_ROOT/agent-manager" ./cmd/agent-manager
   ```

   Resolve machtiani to an absolute path, record its version, and initialize a
   disposable Git/mct project beneath `DEARMACHINE_TEST_PROJECT`. Before every
   machtiani invocation, including inspection and cleanup, unset
   `MACHTIANI_SESSION_ID` and `MACHTIANI_SESSION_TEMP_ROOT`.
4. Create a disposable sender and receiver with the selected transport under
   test, verify their exact IDs,
   and configure the receiver as the client's `--inbox-id`. Configure the
   transport's allowed reply recipient as the disposable sender address, using
   the existing transport convention; the reply address must not be a normal
   inbox. Load credentials only from the authorized local secrets environment.
   Do not put credentials, inbox IDs, addresses, or private message contents in
   this runbook, shell history, logs, or versioned files. Inspect and record
   only the exact allow-list entries created for the pair.
5. Write the version-1 backend configuration below the disposable root. Launch
   the freshly built client continuously, with every path explicit and entry
   point maintenance disabled:

   ```text
   $DEARMACHINE_TEST_ROOT/dearmachine
     --inbox-id <temporary-receiver-id>
     --project $DEARMACHINE_TEST_PROJECT
     --config $DEARMACHINE_TEST_ROOT/dearmachine.toml
     --agent-manager $DEARMACHINE_TEST_ROOT/agent-manager
     --agent-bin <absolute-machtiani-path>
     --db $DEARMACHINE_TEST_DB
     --pidfile $DEARMACHINE_TEST_PIDFILE
     --entry-point-repo ""
     --poll-interval 10s
     --concurrency 1
     --verbose
   ```

   Preserve the actual argument array, load the credential without displaying
   it, and redirect only safe client output to `DEARMACHINE_TEST_LOG`. Confirm
   the client PID, command, first successful poll, database, PID file, manager
   state, and every path belong to the disposable root. Confirm no other
   process polls the temporary receiver.

### Send the interrupted turn

1. Prepare the first email body as an unversioned file beneath
   `DEARMACHINE_TEST_TEMP`. Give it a safe case ID and a multi-step,
   read-only task that the selected backend is expected to keep active for
   several minutes. The body must not ask the agent to detach work or send a
   reply before completing the task. Record its exact private text, sender-side
   and receiver-side message IDs, and both provider-local thread IDs.
2. Send that body from the temporary sender to the temporary receiver as a new
   thread. Do not use the normal inbox for sending, receiving, or observation.
3. Poll at short intervals until all of the following have one common or
   closely adjacent UTC timestamp:

   - the first message's disposable `pending_messages` row has `state =
     'running'`;
   - its `thread_sessions` mapping supplies the canonical session ID and
     sequence 1;
   - `machtiani session list --json` reports that exact disposable session as
     running; and
   - a full recursive `/proc` descendant walk from the recorded DearMachine
     Client PID finds its `machtiani ... run` child.

   Do not cap the process walk at a fixed generation depth. Classify any
   short-lived `sync` or maintenance child by arguments, and retain the raw
   process, SQL, session-list, and log samples. If the first run completes
   before this observation is obtained, classify the attempt `INCONCLUSIVE`
   and retry only with new disposable message and thread IDs and a longer
   first task.

   ```bash
   sqlite3 -readonly "$DEARMACHINE_TEST_DB" '
     SELECT p.message_id, p.thread_id, t.session_id, p.sequence, p.state,
            p.created_at, p.updated_at
       FROM pending_messages AS p
       JOIN thread_sessions AS t USING (thread_id)
      ORDER BY p.created_at;'
   ```

### Send the preempting turn

1. Prepare the second, concise email body as a separate unversioned file under
   `DEARMACHINE_TEST_TEMP`. It must clearly redirect the first task and request
   one bounded answer, so its expected reply can be distinguished from an
   answer to the first email.
2. While the first child is still observed running, send the second body by the
   transport's reply-natural continuation mechanism, for example an AgentMail
   reply or an OpenMail same-thread send with reply ancestry. Do not substitute
   a fresh draft: a fresh draft is a new thread and cannot prove this protocol.
   Record the sender-side second message ID and thread ID privately.
3. List the receiver inbox and obtain the receiver-side second message and
   thread IDs. Its receiver-side thread must map to the same canonical
   `thread_sessions` row as the first message. If the transport retains
   sender-side threading but does not deliver the reply-type message to the
   receiver after about 60 seconds, classify the live preemption result
   `INCONCLUSIVE`; retain both inbox histories and do not retry with a fresh
   draft while claiming a same-thread result.

### Observe preemption and resumed reply

Starting with delivery of the second message, sample the sender and receiver
inboxes, client log, disposable database, recursive process tree, and
`machtiani session list --json` about every two seconds while transitions are
expected. Use a bounded reply grace window of 120 seconds after the resumed
child is observed, matching the short transition polling and bounded-deadline
pattern in the adjacent live protocols. Retain the last complete sample on
timeout. Require these assertions in order.

1. **Graceful stop.** The first `machtiani ... run` child disappears before the
   second run is accepted. `machtiani session show <session-id> --json` for the
   disposable session reports an interrupted/resumable state, or the observed
   first child exits with status 130. Either is accepted machtiani evidence;
   do not require a particular OS signal name. A child that remains running,
   or two simultaneous `machtiani ... run` descendants for this thread, fails
   the protocol.
2. **Resume with only the new turn.** Capture the resumed child command line
   from the recursive process sample. It must contain `run`, `--resume
   <canonical-session-id>`, and `--prompt` whose value is exactly the second
   email body; it must not start a new session or include the first email body
   as the new prompt. Inspect the disposable session conversation/trajectory
   and the emitted attachment/result artifacts. The second message is the next
   user turn; the first message must not reappear as another user turn. No
   emitted reply or artifact may contain fabricated notice text such as
   `stopped` or `recovery` in place of an answer. Retain private redacted
   evidence of the command and conversation rather than copying it into the
   repository.
3. **One substantive answer.** Poll the temporary sender inbox through the
   reply grace window. Exactly one substantive reply must arrive, from the
   configured temporary reply address and in the same sender conversation. It
   must answer the second body. Continue temporary-sender inbox polling for a further
   60-second silence window after that reply: no reply for the first body and
   no second reply may arrive. A reply alone is insufficient without the
   session, process, and database evidence above.
4. **Store invariants.** Preserve the initial running-row snapshot for message
   1 and the claimed/running snapshot for message 2, then query the final
   disposable database. Both snapshots must contain one row for their exact
   message ID; message 1 must subsequently be absent from
   `pending_messages`, present once in `skipped_messages` with reason
   `preempted by newer email`, and absent from `processed_messages`. Message 2
   must be present once in `processed_messages` with a non-empty outbound
   message ID and absent from `pending_messages`. The snapshots must show the
   same canonical session ID, sequence 1 then sequence 2, and the final
   `thread_sessions.sequence` must be 2. The final database must have no
   duplicate `(thread_id, sequence)` group and must retain the one canonical
   session reference for the receiver-side thread.

   ```bash
   sqlite3 -readonly "$DEARMACHINE_TEST_DB" '
     SELECT message_id, thread_id, reason, skipped_at
       FROM skipped_messages
      WHERE message_id = "<receiver-message-1>";
     SELECT message_id, thread_id, outbound_message_id, processed_at
       FROM processed_messages
      WHERE message_id IN ("<receiver-message-1>", "<receiver-message-2>");
     SELECT thread_id, session_id, sequence, status, updated_at
       FROM thread_sessions
      WHERE thread_id = "<receiver-thread-id>";
     SELECT thread_id, sequence, COUNT(*) AS rows_at_sequence
       FROM pending_messages
      GROUP BY thread_id, sequence
     HAVING COUNT(*) > 1;
     SELECT sql FROM sqlite_master
      WHERE type = "table" AND name = "pending_messages";'
   ```

   The protocol fails if either message is duplicated, the second starts a new
   session, a sequence is reused or skips the preempted sequence, the first is
   processed/replied, the second lacks its one reply, or a pending row remains.

### KNOWN LIMITATION

The durable schema has no `claimed_once` counter or retained history of deleted
`pending_messages` rows. The timestamped one-row pending snapshots, the
`message_id` primary key, the `UNIQUE(thread_id, sequence)` schema check, and
the mutually exclusive final `skipped_messages`/`processed_messages` rows are
the closest existing SQL evidence of exactly-once claiming. Do not invent a
new tracing script or schema solely for this live protocol. Similarly, if the
installed machtiani `session show --json` does not expose the persisted user
conversation, use the observed resumed argv and the second-only reply as the
available evidence and report that conversation inspection was unavailable.

### Teardown and report

Follow the shared teardown exactly on pass, failure, or inconclusive result:

1. Stop the disposable client gracefully and prove its exact PID, PID file,
   Machtiani children, Agent Manager children, and backend workers are gone.
2. Preserve the concise private evidence report and the safe SQL, process,
   session, log, inbox, and reply samples needed for diagnosis. Record the
   disposable database path, source and machtiani revisions, private IDs,
   timestamps, sequence/session continuity, verdict, and cleanup results.
3. Remove only policy entries created by this run, then permanently delete the
   verified temporary sender and receiver inboxes. Never remove a protected or
   pre-existing policy entry.
4. Resolve the disposable project's exact UUID-backed Machtiani store and
   delete it only after proving it belongs to `DEARMACHINE_TEST_PROJECT` and no
   process uses it. Validate that `DEARMACHINE_TEST_ROOT` is the run-created,
   non-symlinked directory before removing it. Retain only the approved private
   diagnostic evidence outside that root, never credentials.
5. Confirm the live native DearMachine Client, its inbox, normal runtime
   directories, normal mct state, and the source-worktree baseline are
   unchanged. They must have remained untouched for the entire run.

Report `PASS` only when every numbered assertion succeeds. Report a bounded
timeout or a documented transport continuation-delivery limitation as
`INCONCLUSIVE` after retaining the last state; report any contrary assertion
as `FAIL` without changing the tested checkout.
