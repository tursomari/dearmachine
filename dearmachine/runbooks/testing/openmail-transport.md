# OpenMail Transport Live-Test Runbook

This protocol verifies the selectable OpenMail transport against the live
OpenMail API without sharing the normal DearMachine home, inbox, pair database,
project, or Agent Manager state. It exercises constructor configuration,
inbox resolution, unread-thread polling, thread history, idempotent reply, and
thread-level read acknowledgement.

Use the isolation requirements in
[`temporary-instance.md`](./temporary-instance.md), adapted as described here.
This is a native transport diagnostic, not evidence for the OCI or Compose
deployment path.

## Safety boundaries

- Confirm the official OpenAPI document still declares
  `https://api.openmail.sh` and the required `/v1` paths before a credentialed
  request. Never send the key, mail, or an `Authorization` header to another
  host.
- Load the key through `OPENMAIL_API_KEY` or `OPENMAIL_API_KEY_FILE`. Never
  print it, place it in a command argument, copy it into evidence, or retain an
  HTTP trace containing request headers.
- Provisioning requires an account-wide operator key: the OpenAPI contract
  requires that scope for pod discovery, inbox provisioning, and policy-rule
  operations. A narrower key may still be suitable for the read/send paths,
  but it is not sufficient for this runbook's provisioning or policy changes.
- Use two temporary OpenMail inboxes when account capacity and policy permit.
  Record their exact IDs privately, and never delete or change an inbox the run
  did not create unless the operator explicitly authorizes deleting existing
  test-only OpenMail inboxes to refresh capacity. Under that authorization,
  inventory and record the exact metadata first, delete only the minimum number
  needed for the fresh pair, and verify every deleted ID is not found before
  provisioning the run-created inboxes.
- Keep any normal account-level correspondent allow-list unchanged. If the
  temporary pair is blocked by an inherited policy, apply exact-address rules
  only at each run-created inbox scope. The sender needs inbound/outbound access
  to the receiver, and the receiver needs inbound/outbound access to the
  sender. Deleting each temporary inbox also removes its scoped policy.
- Register the temporary sender as the pair email. Pair routing then enforces
  the exact sender and registered inbox recipient.
- OpenMail is inspect-only unless both `DEARMACHINE_LIVE_OPENMAIL=1` and
  `DEARMACHINE_LIVE_OPENMAIL_APPLY=1` are present. Leave both unset for the
  first poll.

## API contract to verify

Check these operations in the current official OpenAPI document:

- `GET /v1/pods` discovers the account pod used for new inboxes;
- `GET /v1/inboxes` lists inboxes, and `POST /v1/inboxes` creates one with
  that pod's `podId`;
- `GET /v1/inboxes/{id}` verifies an inbox ID;
- `POST /v1/inboxes/{id}/send` sends with `Idempotency-Key`;
- `GET /v1/inboxes/{id}/threads?is_read=false` lists unread threads;
- `GET /v1/threads/{id}/messages` returns ordered thread history; and
- `PATCH /v1/threads/{id}` with `{"is_read":true}` marks the thread read.

The API also documents `DELETE /v1/inboxes/{id}`. It is irreversible and is
used during teardown only for exact IDs created by the current run. The sole
pre-provisioning exception is the explicitly authorized test-only capacity
refresh described above; it must be recorded separately from run teardown.

## Isolated inspect

1. Create a runtime root with `mktemp -d` and require mode `0700`. Put the
   disposable home, project, device configuration, binaries,
   logs, Agent Manager home, and Machtiani project store below it. Unset an
   inherited `MACHTIANI_SESSION_ID`, and disable entry-point maintenance with
   `--entry-point-repo ""`.
2. Build `dearmachine` and `agent-manager` from the revision under test. Create
   a temporary Git project and initialize it with Machtiani using isolated
   home and session-temp roots.
3. Discover the account pod with `GET /v1/pods` without printing the
   credential. List inboxes, then create and verify a temporary receiver if no
   suitable run-created receiver exists, passing the discovered `podId` on
   each `POST /v1/inboxes` request. Prefer a second temporary inbox as the
   sender.
4. Leave both mutation gates unset and run one empty poll:

   ```bash
   export OPENMAIL_API_KEY_FILE=/path/to/one-line-key
   HOME="$TMP/home" "$TMP/dearmachine" up --create \
     --email 'temp-sender@example.test' \
     --inbox '<temporary-receiver-id-or-address>' \
     --transport openmail \
     --once \
     --verbose \
     --project "$TMP/project" \
     --config "$TMP/dearmachine.toml" \
     --agent-manager "$TMP/agent-manager" \
     --agent-bin '<absolute-machtiani-path>' \
     --entry-point-repo ""
   ```

The inspect step passes when the constructor reads the key file, resolves the
inbox, polls the canonical API host, and exits without changing any thread.

## Poll, reply, continue, and acknowledge

1. Confirm the provider-side policy allows the temporary pair. If the account
   policy denies the pair, add only inbox-scoped exact-address policies to the
   two run-created inboxes. Do not replace or expand the account policy.
2. From the temporary sender, send one new-thread message to the receiver with
   a unique `Idempotency-Key`. Use a short, read-only task scoped to the
   disposable project, such as asking for its current Git status.
3. Wait until the receiver lists exactly that thread as unread. Then enable
   both mutation gates and run `HOME="$TMP/home" "$TMP/dearmachine" up --once`
   with the same run flags:

   ```bash
   export DEARMACHINE_LIVE_OPENMAIL=1
   export DEARMACHINE_LIVE_OPENMAIL_APPLY=1
   ```

4. Wait for the first Dear Machine response. Record its stable conversation
   reference privately, then reply to it from the temporary sender's existing
   email thread with a meaningful follow-up to the original task. Preserve the
   quoted reply and footer as a normal mail client would.
5. Wait for the receiver thread to become unread again and run the same
   isolated client against the same live database. Record both inboxes' local
   message and thread identifiers; OpenMail may use different identifiers on
   each side or for the continuation.
6. Verify all of the following independently:

   - the client polled and claimed exactly two allowed inbound messages;
   - both inbound turns map to one mct session and the second turn advances it
     to sequence 2;
   - both outbound replies contain the same valid stable Dear Machine
     conversation reference;
   - the second machtiani prompt contains the new follow-up but neither the
     Dear Machine footer nor quoted history as new user content;
   - the receiver history contains exactly two later outbound replies to the
     temporary sender;
   - the sender received both replies in its corresponding email thread;
   - the receiver thread is read after successful processing;
   - the isolated database has no pending message and exactly two processed
     messages; and
   - a repeated poll does not create a duplicate session or reply.

OpenMail read state is thread-level. `Poll` returns the newest inbound message
from each unread, fully allowed thread, and `MarkProcessed` marks the whole
thread read. OpenMail assigns a different local thread ID in each inbox, so the
sender and receiver histories can correspond even when their IDs differ.

## Teardown

Stop the isolated client and confirm its PID file is gone. Delete only the
temporary inbox IDs recorded by this run, using the official inbox deletion
operation. Verify those IDs now return not found and that the normal
account-level policy is unchanged. Remove the exact temporary runtime root,
including copied runtime credentials and session artifacts. Finally confirm
the normal DearMachine process still has its original command, inbox, database,
and PID file.
