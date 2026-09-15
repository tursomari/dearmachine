# Sendmux Transport Live-Test Runbook

This protocol verifies the selectable Sendmux adapter without sharing the
normal Dear Machine home, pair database, project, Agent Manager state, or
mailbox transport. It proves one complete two-turn conversation:

`external send -> Dear Machine reply -> external reply -> Dear Machine reply`

For To/CC/BCC preservation and the adapter-neutral routing contract, also run
the Sendmux receiver row in [`recipient-delivery.md`](./recipient-delivery.md).

Use the isolation requirements in
[`temporary-instance.md`](./temporary-instance.md). This is a native transport
diagnostic unless the test explicitly follows the containerized production
path from that shared runbook.

## Safety boundaries

- Use a Sendmux Infrastructure key for provisioning. Keep it in a user-owned
  mode-`0600` file; never print it, put it in a command argument, or retain
  authorization headers in evidence. DearMachine stores the one-time
  mailbox-scoped credential beneath the disposable home and uses that scoped
  key for runtime.
- Keep the Sendmux mailbox and external correspondent addresses in a separate
  mode-`0600` operator environment file. Do not commit them, add them to test
  fixtures, or quote them in test reports.
- The external correspondent may be any specifically authorized mailbox. This
  one-off operational choice must not introduce provider-, organization-, or
  account-specific behavior into Dear Machine or this procedure.
- Register the external correspondent as the pair email. A message from any
  other sender must remain invisible to the pair router.
- Production Sendmux operation needs no DearMachine-specific mutation gate.
  The disposable home, run-created mailbox, and bounded cleanup are the
  live-test safety boundary.
- Do not stop, reconfigure, or point this test at the normal Dear Machine
  service. Use a unique runtime root and database.

## Sender authentication integration check

The adapter uses the mailbox credential for both REST and verified TLS IMAP at
`mail.sendmux.ai:993`. Keep that port reachable. A message must have an exact
From-domain DKIM signature covering its entire body and every present author,
routing and MIME header. Parsed text, SPF verdicts and From matching alone are
not sufficient. See [the authentication contract](../../../docs/guest-authorization.md).

The narrow integration check may insert one locally generated, DKIM-signed
multipart fixture into a run-created mailbox using IMAP APPEND, then exercise
REST polling and the production IMAP fetcher. Inject only the generated fixture's
DNS public key in the test verifier. Verify opaque/RFC ID binding, decoded
attachment presence, successful signature verification, unchanged unread flags,
and provider Sent-folder role discovery. Delete exactly the created mailbox.
This validates adapter wiring without sending external mail; it does not replace
the external two-turn scenario below or prove the sender domain's real DNS keys.

The deterministic suite also rejects changed recipients and bodies, attachment
byte tampering, ambiguous Internet IDs, changed UID validity, oversized literals,
scan-limit exhaustion, connection failures and forged inbox From/keywords. These
checks run without live credentials. Private live fixtures and credentials must
remain outside tracked source.

## Private operator configuration

Create an untracked file outside the repository, owned by the operator and
mode `0600`, with values appropriate to this run:

```bash
SENDMUX_API_KEY_FILE=/absolute/path/to/sendmux-infrastructure-api-key
SENDMUX_LSE_CORRESPONDENT=<exact-external-address>
```

Load it without echoing its contents:

```bash
set -a
. /absolute/path/to/sendmux-lse.env
set +a
```

Before continuing, require that the credential file and operator environment
file are regular files owned by the current user with no group or other bits.

Before the first poll, inventory mailbox IDs privately. The run must create a
new mailbox and later delete exactly that ID; it must not adopt or mutate an
existing mailbox.

## Isolated creation and empty poll

1. Create a runtime root with `mktemp -d` and require mode `0700`. Put the
   disposable home, Git project, device configuration,
   logs, Agent Manager home, and Machtiani state below it. Disable entry-point
   maintenance with `--entry-point-repo ""`.
2. Build `dearmachine` and `agent-manager` from the revision under test. Use a
   dedicated trivial Git project and an isolated Machtiani session root.
3. Run one creation and empty poll through the official APIs:

   ```bash
   HOME="$LSE_ROOT/home" "$LSE_ROOT/dearmachine" up --create \
     --email "$SENDMUX_LSE_CORRESPONDENT" \
     --new-inbox \
     --transport sendmux \
     --once \
     --verbose \
     --project "$LSE_ROOT/project" \
     --config "$LSE_ROOT/dearmachine.toml" \
     --agent-manager "$LSE_ROOT/agent-manager" \
     --agent-bin '<absolute-machtiani-path>' \
     --entry-point-repo ""
   ```

The step passes only if the Infrastructure key creates one active mailbox,
DearMachine stores its one-time scoped credential privately, pair policy
contains the exact correspondent, and the scoped key polls successfully. An
authentication failure is a credential blocker, not evidence about delivery.

## Send, reply, send, reply

1. With the client stopped, establish the fresh pair baseline by locally skipping
   the exact current eligible snapshot with a run-specific reason:

   ```bash
   HOME="$LSE_ROOT/home" "$LSE_ROOT/dearmachine" inbox skip \
     --pair "$SENDMUX_LSE_CORRESPONDENT" \
     --current \
     --project "$LSE_ROOT/project" \
     --agent-bin '<absolute-machtiani-path>' \
     --reason "sendmux-lse-<run-id>-baseline"
   ```

   Record the skipped message IDs privately as the baseline. Do not reuse an
   inspect database or a database from another run.
2. From the external correspondent, send a new message to the Sendmux mailbox
   with a unique subject and a small read-only task scoped to the disposable
   project. Record identifiers privately; do not copy addresses or message
   contents into a committed artifact.
3. Confirm through the Sendmux mailbox API that exactly that run-created
   inbound message is unread, has an RFC Message-ID, and is in a thread
   containing only the two authorized participants.
4. Immediately before every client start in this procedure, re-poll through
   the mailbox API. Abort if any eligible unread message ID is neither a
   recorded baseline ID nor a message created by this run. Then run the
   isolated client against the live database.

5. Wait for the first Dear Machine response. Reply to that response in the
   existing conversation and make the follow-up meaningfully depend on the
   first turn. For an automated follow-up, set `In-Reply-To` to the prior Dear
   Machine response's RFC Message-ID, not a Gmail resource ID, Sendmux ID, or
   thread ID. Extend `References` by appending that Message-ID exactly once to
   the prior `References`; supply the provider thread ID in the send request;
   use a `Re:` subject; and quote the full prior response in the body, including
   the Dear Machine footer. This exercises both the ancestry and footer-
   fallback paths on the receiving side.
6. Wait for the second run-created inbound message to become unread, repeat the
   pre-start re-poll guard, and rerun the same isolated client with the same
   live database and project. Identify the follow-up by its authenticated
   sender and reply ancestry or stable conversation reference; do not require
   exact subject equality because mail clients commonly add or normalize a
   `Re:` prefix.
7. Verify independently that:

   - exactly the two run-created, non-skipped inbound messages were claimed and
     processed;
   - both turns map to one Machtiani session and the second advances its
     sequence rather than creating a new session;
   - both Dear Machine replies contain the same valid stable conversation
     reference;
   - each outbound send has a unique durable idempotency key;
   - each outbound message has a provider-generated RFC Message-ID, and the
     follow-up references the answer it replied to;
   - the second prompt contains the new human contribution but not the footer
     or quoted history as new user text;
   - Sendmux reports both run-created inbound messages seen after successful
     processing;
   - no baseline ID caused a session, Agent Manager ticket, or reply;
   - the isolated database has exactly two processed run-created messages and
     no pending rows; and
   - a repeated poll creates neither a third agent turn nor a duplicate reply.

Sendmux replies use native JMAP over verified HTTPS at `mail.sendmux.ai`, using
only the mailbox-scoped credential. The native API preserves `In-Reply-To` and
`References`, stores the outgoing message, and returns a durable submission.
The legacy `SENDMUX_SEND_API_KEY` configuration is rejected: the separate Sending
API cannot express the required reply ancestry. SMTP alone lacks a recoverable
Sent receipt and is not used.

The relay can rewrite the delivered Message-ID. Private guest approval prompts
include an unguessable reference bound to a private Sent message in the same
inbox and thread. The authenticated owner must retain that prompt in recognized
mail-client reply history or copy the reference line below their exact decision.
When the sender's reply endpoint supplies that history, submit only `Yes`, `No`
or `Other` as the newly authored body. Do not also prepend a manually quoted
prompt: free-standing quotations count as additional authored text and cause a
private retry prompt. Verify the delivered reply retains the reference before
expecting approval to resolve. Test missing, wrong, ambiguous and
cross-thread references; none may approve a request. Public replies must not
contain private approval references.

Do not enable SMTP envelope allow-lists for paired or guest From identities.
Provisioned test inboxes retain their default filter mode; local authentication
and grants enforce access. Preserve all pre-existing operator filters.

Check sending capacity before the scenario. Shared quotas count recipients, not
messages. An answer to owner plus guest requires two recipient slots. Queued
submissions and SMTP acceptance are not inbox delivery evidence: inspect with
`dearmachine inbox delivery --pair <pair> <outbound-message-id>` and independently
observe each recipient's inbox. Keep accepted but delayed mail distinct from
failed requests. Do not retry a queued submission to work around the quota.

## Backend-native completion live probe

Run the forced-delegation proof as a separate isolated live probe with a fresh
database and disposable Git project. Give the project a deterministic failing
test and one bounded implementation gap, such as implementing `slug.Normalize`
so the provided tests pass. Require delegation so the coordinator cannot answer
the task directly.

Assert that exactly one Agent Manager ticket is created; its ticket envelope
places the completion protocol after the delegated request and explicitly
exempts control-plane bookkeeping from conflicting read-only language; and it
reaches `closed` with a nonempty, regular-file, mode-`0600`
`ticket-close.md`. The close-file content must equal the backend's native final
reply, and `completion_source == native_reply`. Confirm that only the expected
project file changed and that the project tests pass.

Cover failure diagnostics in separate deterministic Agent Manager probes. Use
a controlled backend that writes a stderr sentinel and exits nonzero, then
assert `failed`, the recorded failure reason and exit code, and a bounded
stderr tail. Use a signal-terminated worker to assert `crashed` and the signal;
do not induce real-provider failures through the live mailbox to test these
paths.

## Teardown

Stop only the isolated client and confirm its PID file is gone. Delete exactly
the mailbox ID created by this run with the management API. Verify an exact-ID
lookup either reports `not_found` or returns that same mailbox with status
`deleted`; Sendmux retains soft-deleted mailbox metadata. Preserve the minimum
redacted evidence needed for the result, then remove the exact temporary
runtime root using the shared runbook's teardown procedure. Confirm the normal
Dear Machine service still has its original command, transport, database, and
PID file.
