# Thread-scoped guests

On AgentMail, an authenticated owner invites a guest by including them in To or
CC alongside Dear Machine. The grant belongs to one owner pair, provider inbox,
guest mailbox and exact provider thread. It persists until explicitly revoked.
DearMachine synchronizes its managed provider address lists automatically.
There is no separate guest admission exchange.

Every guest message must be authenticated and visibly include Dear Machine and
the owner: use Reply All. Each message produces its own private owner approval
prompt, including a quoted preview of the held request. Only an explicit approval
of that request allows lower-authority agent execution. Retained admission or
trust cannot bypass approval. A guest cannot invite others, start an unrelated
conversation, become a controller or access another pair's thread.

Answers go To the owner and CC active guests visible in the original request's
From, To or CC. The private approval's recipients do not determine the answer's
recipients. When the owner continues without the guest in To/CC, the answer goes
only to the owner. This omission neither revokes the guest nor cancels pending
approvals. BCC, quoted headers, forwarded inner messages and body addresses do
not invite guests or add answer recipients. Another paired owner cannot become
a guest, including when their worker is not selected.

## Sender authentication

AgentMail's adapter fetches the scoped message's
[raw RFC822 bytes](https://docs.agentmail.to/api-reference/inboxes/messages/get-raw)
and verifies DKIM locally with DNS keys. One valid signature from the **exact
From domain** must cover every present author, recipient, message-ID, subject,
reply-correlation and MIME interpretation header, including every `Content-*`
field. The body must verify in full;
duplicate authorization headers, missing evidence, unsigned CC or reply headers,
invalid signatures and provider `unauthenticated` labels are rejected. Parsed
sender, recipients, subject, message ID and reply correlation must match the
provider's normalized message. API credentials are never sent to the raw-message
download URL. Positive caching binds the immutable normalized message content.

This policy **trusts the From domain's mail operator to enforce mailbox ownership**.
DNS key resolution and the signing key's custody are also trusted.
DKIM proves signing-domain authority and integrity, not the identity of a human
or independent control of a mailbox local part. A malicious/compromised domain
operator or signing key can impersonate mailboxes in that domain. There is no
hardcoded trusted-domain list. Trust also covers AgentMail's configured inbox,
thread IDs, outbound labels, MIME/body extraction and attachment mapping to the
verified raw message. This is an explicit boundary, not a proof of the provider.
Raw From matching, allowlisting and generic SPF/DKIM/DMARC or
Authentication-Results verdicts are never sufficient.

The same check applies to owner instructions, invitations, guest requests,
approvals and removal commands. A signature that omits a present authorization
header is insufficient even if an email provider accepts it normally.
**OpenMail supports a restricted plain-text subset.** Its live message responses
include an ordered header list in `raw.message-headers`, along with
`rfcMessageId`, `inReplyTo` and `references`. The adapter reconstructs a
single-part `text/plain` message with no transfer encoding, `7bit`, or `8bit`,
and requires the same exact-domain DKIM and signed-header checks as AgentMail.
Only UTF-8 and US-ASCII bodies are supported. HTML, multipart, attachments,
encoded bodies, missing evidence and reconstruction failures are rejected.
The header field is currently undocumented; if its shape changes, verification
fails closed. A provider verdict never substitutes for the signature.

OpenMail and Sendmux API message IDs remain distinct from signed Internet Message-IDs.
Approval replies resolve signed references against outbound records in the same
inbox/thread before looking up the exact private approval prompt. Provider
inbox/thread metadata and that ID mapping remain trusted.

**Sendmux retrieves original RFC 822 messages over verified TLS IMAP** at
`mail.sendmux.ai:993`, using the same mailbox-scoped credential as REST. The
infrastructure key is only for provisioning. Original MIME, including HTML,
encoded bodies and attachments, must pass the same exact-domain DKIM and
signed-header policy. The adapter checks the REST record's normalized fingerprint
and single Internet Message-ID against the retrieved message. REST inbox/thread,
MIME extraction and attachment mapping remain part of the provider trust boundary.
Sent status requires provider Sent-folder membership, not just a matching From.

IMAP opens folders read-only and uses `BODY.PEEK`, preserving unread flags.
It reads Message-ID headers directly because provider HEADER searches did not
find a newly delivered message in a live probe. A verification scans at most 64
folders and 10,000 messages, downloads at most 64 MiB in total, limits individual
header responses to 64 KiB and original messages to 32 MiB, and times out after
30 seconds. Missing evidence, duplicate matches (including folder copies), changed
UID validity, exceeded limits and invalid signatures never authorize work.
Successful verification is cached in memory for up to 2,048 exact message
fingerprints; changing a cached request is rejected. Daemon startup and `guest
list` report the available authentication path.

## Remove and reinvite

Private approval prompts and owner-only answers include a copyable command:

```text
REMOVE GUEST <code>
```

The owner replies in that thread with the actual command from the footer.
The random token binds the exact pair, inbox, guest, thread and grant generation.
It is not included in shared answers. Local revocation blocks subsequent guest
execution starts, including queued, held and recovered work. Already-started work
can still have effects, and submitted email cannot be recalled. Replaying a
completed removal command is harmless; an old token cannot revoke a later grant generation.

Ordinary reply-all and replayed invitations cannot restore a revoked grant.
Explicitly reinvite using the native CLI, then send a **new guest request** and
approve it. An older qualifying owner invitation may be selected deliberately;
old guest work and approvals never regain authority.

```console
dearmachine guest list --pair owner@example.test
dearmachine guest allow guest@example.test --pair owner@example.test --message-id invitation-id
dearmachine guest revoke guest@example.test --pair owner@example.test --thread-id provider-thread-id
dearmachine guest revoke guest@example.test --pair owner@example.test --all
```

Select a pair by its unambiguous email or UUID. `allow` resolves the specified
message in that inbox and requires the owner as outer From, with Dear Machine
and the guest visibly in To/CC. This command is the trusted local operator's
explicit authorization of those facts; it does not assert cryptographic sender
authentication or bypass authentication of subsequent inbound work. Concierge
should inspect installed `guest --help` first because older versions differ.
Commands work while the daemon runs and create no permanent pair.

## Persistence and provider synchronization

Guest requests are durably bound to their grant generation and normalized
content fingerprint before prompting. Refetching changed content under the same
message ID cannot substitute a different instruction for the owner's approval.
Approval materialization is transactional; execution rechecks the exact approved
request and current grant while holding the revocation lock across process start.

Recipient grant generations are recorded at instruction acceptance. Revoking
and reinviting a guest cannot subscribe them to an older, unsubmitted answer.
The complete To/CC envelope is persisted immediately before first submission.
Receipt recovery matches that envelope; retries retain it and its idempotency
key even after revocation. Already-submitted uncertain deliveries may complete.

An upgrade creates the new tables automatically. Existing guest grants remain.
Old queued/held guest work without content bindings fails closed: send a fresh
request for approval. Older unsubmitted work without recipient snapshots defaults
to owner-only answers; existing persisted submission envelopes remain unchanged.

Provider permissions are shared across applicable grants and permanent pairs in
an inbox. AgentMail needs receive, reply and send entries (`reply` filters incoming
replies). OpenMail has inbound/outbound rules. Sendmux uses local authorization and DKIM
verification without managing SMTP sender filters: envelope senders can differ
from authenticated From addresses. Existing operator filters remain unchanged;
an older DearMachine-managed allow-list may need to be turned off in Sendmux
before legitimate messages arrive. Sendmux runtime needs only the mailbox key. Only a final unneeded entry established as owned
by DearMachine may be removed. Directional ownership is independent. Pre-existing
entries, uncertain additions after lost responses and externally changed entries
are preserved. No guest action changes global policy mode or unrelated rules.

Local changes commit before remote reconciliation. `guest list` reports grants
and each permission's direction, ownership, pending state and last error. Provider
failures leave synchronization pending; local revocation still applies. The daemon
retries synchronization on later polls. Adding a provider permission cannot
recover a message the provider already rejected; send it again after sync succeeds.

The [verification guide](../verification/guest/README.md) and
[validation record](../verification/guest/VALIDATION.md) distinguish bounded
proofs, adapter tests, trusted boundaries and live evaluation gaps.


Sendmux replies use its native mailbox submission API to preserve `In-Reply-To`
and `References` and keep a durable Sent receipt. The outbound relay may rewrite
Message-ID. Private approval prompts therefore include a random approval
reference: reply with `Yes` while quoting the prompt, or copy its reference line
beneath `Yes`. DearMachine requires an authenticated owner, the same inbox and
thread, and a matching private prompt for the exact pending request. Missing or
ambiguous references never approve work. The reference is not sent in shared
answers and does not grant continuing approval.

A Sent receipt proves submission, not arrival. Inspect a Sendmux outbound message
with `dearmachine inbox delivery --pair owner@example.test <outbound-message-id>`.
Queued or unconfirmed statuses must not be reported as delivery. Shared sending
limits count recipients, including CC; quota delays can hold an accepted reply.
