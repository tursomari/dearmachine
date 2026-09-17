# OpenMail Transport Live-Test Runbook

This protocol verifies the selectable OpenMail transport against the live
OpenMail API without sharing the normal DearMachine home, inbox, pair database,
project, or Agent Manager state. It exercises constructor configuration,
inbox resolution, unread-thread polling, thread history, idempotent reply, and
thread-level read acknowledgement.

For To/CC/BCC preservation and the adapter-neutral routing contract, also run
the OpenMail receiver row in [`recipient-delivery.md`](./recipient-delivery.md).

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
- Production OpenMail operation needs no DearMachine-specific mutation gate.
  Use the isolated home, disposable inbox, and exact-address provider policy
  in this runbook as the live-test safety boundary.

## API contract to verify

Check these operations in the current official OpenAPI document:

- `GET /v1/inboxes` lists inboxes, and `POST /v1/inboxes` with an empty object
  creates one using the account defaults;
- `GET /v1/inboxes/{id}` verifies an inbox ID;
- `POST /v1/inboxes/{id}/send` sends with `Idempotency-Key`;
- `GET /v1/inboxes/{id}/threads?is_read=false` lists unread threads;
- `GET /v1/threads/{id}/messages` returns ordered thread history; and
- `PATCH /v1/threads/{id}` with `{"is_read":true}` marks the thread read.

The API also documents `DELETE /v1/inboxes/{id}`. It is irreversible and is
used during teardown only for exact IDs created by the current run. The sole
pre-provisioning exception is the explicitly authorized test-only capacity
refresh described above; it must be recorded separately from run teardown.

## Isolated creation and empty poll

1. Create a runtime root with `mktemp -d` and require mode `0700`. Put the
   disposable home, project, device configuration, binaries,
   logs, Agent Manager home, and Machtiani project store below it. Unset an
   inherited `MACHTIANI_SESSION_ID`, and disable entry-point maintenance with
   `--entry-point-repo ""`.
2. Build `dearmachine` and `agent-manager` from the revision under test. Create
   a temporary Git project and initialize it with Machtiani using isolated
   home and session-temp roots.
3. List the account inboxes without printing the credential. Record the exact
   baseline privately, then let the CLI create the temporary receiver and its
   pair policy with one empty poll. Prefer a second temporary inbox as the
   sender.
4. Run:

   ```bash
   export OPENMAIL_API_KEY_FILE=/path/to/one-line-key
   HOME="$TMP/home" "$TMP/dearmachine" up --create \
     --email 'temp-sender@example.test' \
     --new-inbox \
     --transport openmail \
     --once \
     --verbose \
     --project "$TMP/project" \
     --config "$TMP/dearmachine.toml" \
     --agent-manager "$TMP/agent-manager" \
     --agent-bin '<absolute-machtiani-path>' \
     --entry-point-repo ""
   ```

The step passes when the constructor reads the key file, creates and registers
one inbox, establishes exact pair policy, polls the canonical API host, and
exits without changing any pre-existing thread.

## Poll, reply, continue, and acknowledge

1. Confirm the provider-side policy allows the temporary pair. If the account
   policy denies the pair, add only inbox-scoped exact-address policies to the
   two run-created inboxes. Do not replace or expand the account policy.
2. From the temporary sender, send one new-thread message to the receiver with
   a unique `Idempotency-Key`. Use a short, read-only task scoped to the
   disposable project, such as asking for its current Git status.
3. Wait until the receiver lists exactly that thread as unread. Then run
   `HOME="$TMP/home" "$TMP/dearmachine" up --once` with the same run flags.

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

## Sender evidence and MIME coverage

The production adapter authenticates a message by fetching its complete raw
MIME and verifying its DKIM signature before parsing it, but only once the
polled message reports a `rawUrl`. That value is provider-controlled and is
never used to build the fetch: it is only a presence signal that raw evidence
exists. The request path is always the deterministic `GET
/v1/messages/{id}/raw` built from the configured API base URL and the
message's own opaque ID, so a compromised or malformed `rawUrl` can never
redirect the authenticated request to another host. Only after the signature
check passes does the adapter parse the body and any multipart attachments; a
message without a `rawUrl`, a failed fetch, a malformed MIME structure, an
unsupported Content-Transfer-Encoding, or a failed or unsupported signature is
rejected, never authorized by a provider verdict. Plain text, HTML, multipart
and attachments are all positive cases once signed; the parsed body and
attachments must also match what the provider's JSON listing reported, or the
message is rejected. Decoded text and HTML comparisons normalize CRLF to LF
only after DKIM verifies the original raw bytes. All content whitespace,
including every terminal newline, must match; an added or missing final newline
is rejected. Signed parent and References headers must survive replies of every
supported shape.

After authentication, the transport keeps the message fingerprint and the size
and SHA-256 digest of each decoded, signed attachment. Every attachment download
must match that verified size and digest before any bytes are returned to the
caller. Provider metadata alone never authorizes a download. Duplicate attachment
filenames are rejected because the download endpoint addresses parts by filename.
The in-memory evidence cache is bounded; after restart or eviction, a message
must be authenticated again before downloading its attachments. The router
authenticates inbound messages on polling and direct retrieval, including recovery.

For a direct API diagnostic, send synthetic fixtures from an explicitly
authorized external mailbox into a disposable OpenMail receiver. This avoids
OpenMail outbound new-recipient limits. Disable the receiver webhook, permit
only the exact sender on its scoped inbound policy, and retain no credentials.
Match the received fixture by its unique subject and verified sender; a sending
provider may replace the submitted Message-ID. Record the received
`rfcMessageId`, the distinct API `id`, and the original reply headers.

Fetch the live raw MIME from `GET /v1/messages/{id}/raw` with local DKIM
verification, then alter body, attachment and recipient bytes and require
failure. Run the actual
adapter against privately retained API fixtures with DNS verification.
Also substitute same-length bytes at the attachment download endpoint while
keeping the original signed MIME unchanged; the production download must fail
without returning any bytes. Check truncated and extended downloads, missing
authentication, duplicate filenames, and successful reauthentication after
restart. Use synthetic binary payloads and derive all addresses and identifiers
from the disposable inbox creation responses.
Include forged From, unsigned CC/parent headers, absent `rawUrl`, a raw fetch
that fails or times out, and wrong inbox IDs in credential-free tests. Test
approval correlation using distinct Internet and provider IDs. Delete exact
scoped rules with their `inboxId` query parameter before deleting the
receiver, and verify account policy is unchanged. These diagnostics prove
evidence handling, not a deployed agent round trip.
