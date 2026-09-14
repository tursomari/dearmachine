# Disposable DearMachine Client Smoke-Test Runbook

This document is a prompt for a capable local agent, not an executable test
script. It exercises one isolated two-turn email-to-session-to-reply lifecycle
or the optional local skip/unskip behavior.

Use [`temporary-instance.md`](./temporary-instance.md) for provisioning,
isolation, observation, evidence handling, and teardown. Those requirements are
part of this protocol. For ordered backend selection, use
[`live-backends.md`](./live-backends.md). For session checkpoints
and internal-README maintenance, use
[`update-sync.md`](./update-sync.md).

## Prompt to give the testing agent

Conduct one disposable DearMachine Client smoke test using the shared temporary
instance reference. Exercise the real email-to-session-to-reply lifecycle
without sharing the normal inbox, configuration, database, PID file, Agent
Manager state, runtime directory, or mct project.

Do not change product code, documentation, prompts, or tests in response to the
exercise. Preserve all pre-existing workspace state and retain no credentials.

### Configure this protocol

- Use a dedicated temporary inbox and project.
- Pass an empty `--entry-point-repo` value. Entry-point maintenance is outside
  this protocol.
- Use the backend order supplied by the tester.
- Confirm the sender is accepted before sending the test message.
- Record the source revision, temporary paths, inbox ID, project UUID, backend
  order, and normal-state baseline privately.

### Ordinary email lifecycle exercise

1. Start the isolated client and verify its first successful poll.
2. Send a new-thread email from the authorized account. Use an ordinary,
   meaningful task scoped to the temporary project. Do not mention the expected
   backend or describe it as a backend test. Prefer a short read-only inspection
   unless repository modification is explicitly under test.
3. Record the sender-side message and thread identifiers and the AgentMail
   thread identifier in private evidence.
4. Observe the process, disposable database, mapped mct session, trajectory,
   Agent Manager state when used, and AgentMail thread until the first
   substantive reply arrives. Record the reply's stable Dear Machine
   conversation reference privately.
5. Reply to that response in the sender's existing email thread with a
   meaningful follow-up to the original task. Preserve the quoted reply and
   its Dear Machine footer as a normal mail client would. Record the second
   sender-side message and thread identifiers and the second receiver-side
   AgentMail message and thread identifiers; provider-local thread identifiers
   are allowed to differ.
6. Observe the second turn through completion. Confirm it resumes the original
   mct session at sequence 2, retains the same stable Dear Machine conversation
   reference, does not pass the footer or quoted history to machtiani as new
   user content, and produces exactly one second reply to the sender's existing
   email thread. Observe one further poll and confirm it creates no duplicate
   execution or reply.
7. Apply a bounded deadline to each turn. On timeout, preserve the last state
   and report the stage where progress stopped.

For agent-managed work, do not infer success from the reply alone. Verify the
approved backend order, health-check results, selected ticket worker, closed
ticket result, and same-thread reply.

### Optional local skip and unskip exercise

Use this instead of the ordinary exercise when validating local inbox
suppression:

1. Keep the disposable client stopped and send one meaningful task to its
   temporary inbox. Record the exact inbound message and thread IDs.
2. Run `dearmachine inbox skip --current` with the temporary inbox, database,
   PID, project, and machtiani paths supplied explicitly. Confirm the local skip
   list contains exactly that message.
3. Start the client and observe at least two polls. Confirm the message is marked
   read in AgentMail, no mct session or Agent Manager ticket is created, no
   backend starts, and no reply is sent.
4. Stop the client. Run `dearmachine inbox unskip <message-id>` against the
   same database and PID file. Confirm the local list is empty and that exact AgentMail message is
   unread again.
5. Restart the same client and monitor the full lifecycle. Confirm exactly one
   session, one execution, and one same-thread reply; no pending row remains.
6. Stop the client and perform the shared teardown.

Do not perform this proof against the normal inbox. The temporary inbox makes
both the negative and positive assertions conclusive without risking real
messages.

### Pass criteria

The ordinary exercise passes only when:

- exactly one sender-visible email conversation is exercised, containing two
  inbound user turns; provider-local thread identifiers may differ;
- two inbound user turns map to one mct session with sequence 2 without
  replaying quoted history or exposing the Dear Machine footer to machtiani;
- both outbound replies contain the same valid stable conversation reference;
- any managed ticket closes with the intended worker;
- exactly two substantive replies arrive in the sender's original email
  thread;
- the disposable database has exactly two processed messages and no pending
  message;
- no duplicate reply or session is created;
- entry-point session maintenance remains disabled;
- normal DearMachine Client and repository state remain untouched; and
- the shared shutdown, inbox deletion, project-store cleanup, runtime cleanup,
  and baseline verification all succeed.

### Final report

Report the temporary isolation paths and inbox ID privately, source revisions,
backend order, email/session/ticket/reply outcomes, pass/fail/inconclusive
classification, retained evidence location, cleanup outcomes, and proof that
normal state and unrelated files were preserved.
