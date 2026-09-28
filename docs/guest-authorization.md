# Thread-scoped guests

On AgentMail, an authenticated owner invites a guest by including them in To or
CC alongside Dear Machine. The grant belongs to one owner pair, provider inbox,
guest mailbox and exact provider thread. It persists until explicitly revoked.
DearMachine synchronizes its managed provider address lists automatically.
There is no separate guest admission exchange.

Every guest message must be authenticated, or covered by the owner exception
below, and visibly include Dear Machine and the owner: use Reply All. Each message produces its own private owner approval
prompt, including a quoted preview of the held request. Only an explicit approval
of that request allows lower-authority agent execution. Retained admission or
trust cannot bypass approval. A guest cannot invite others, start an unrelated
conversation, become a controller or access another pair's thread.

After separate outbound approval, answers go To the owner and CC active guests visible in the original request's
From, To or CC. The private approval's recipients do not determine the answer's
recipients. When the owner continues without the guest in To/CC, the answer goes
only to the owner. This omission neither revokes the guest nor cancels pending
approvals. BCC, quoted headers, forwarded inner messages and body addresses do
not invite guests or add answer recipients. Another paired owner cannot become
a guest, including when their worker is not selected.

## Owner approval before shared sending

The runtime implements a durable outbox gate for **all paired shared replies**,
including owner-origin answers, approved guest work, and shared error/status
replies. Instruction approval and outbound approval are separate: guest
instruction `Yes` permits execution, `No` rejects the instruction, and `Other`
requests a newly authored owner replacement. None approves sending its result
to guests. An owner-only reply needs no outbound approval.

Before shared submission, the owner receives a private preview To the owner
with empty CC/BCC. Dear Machine adds no quoted history to the frozen draft;
providers may add branding or quote the original message. The preview contains the exact
frozen plain text, HTML (when present), and files, with a pending header added
to text and HTML. The header begins:

```text
PENDING APPROVAL — this reply has not been sent to guests.
```

It lists the proposed To/CC recipients and an outbound approval reference.
Those listed guests are not recipients of the preview. Reply with only newly
authored `yes` or `no` (case insensitive, surrounding whitespace ignored),
referencing that issued preview through reply correlation or its quoted
`Dear Machine outbound approval:` reference line. Quoted history is excluded
from the decision. Extra authored text, including `Other`, does not approve or
reject an outbound draft. `no` terminates that proposal; a later `yes` cannot
revive it.

The decision must come from the authenticated owner and resolve uniquely to an
actually issued preview for the same pair, inbox, thread, request and revision.
An allocated token or pending row alone does not establish issuance. An
instruction-approval reference cannot authorize outbound sending. Successful
submission uses the frozen draft without the pending header; approval does not
rerun the agent.

Recipients remain bound to the grant generations accepted for that request.
Eligibility is checked at preview and first submission. Removing recipients
creates a new revision and clears its approval: if any guests remain, a new
private preview and a new `yes` are required. If all guests are gone, the answer
can use the owner-only exemption. Reinvitation cannot add a new generation to
an older answer. Content or file substitution under an existing send key is
rejected rather than silently changing what was approved.

Execution may complete while the answer remains pending. The durable outbox
owns the frozen text, HTML and attachment bytes even after execution staging
files are removed. A local `outbound-pending:<token>` receipt means only durable
outbox acceptance: it is **not provider submission or delivery**.

Adapters distinguish local failures before any mutating provider request from
uncertain acceptance. Only the former permit automatic retry, after checking
receipts first. Preview and submission each have at most three attempts per
revision within 15 minutes of their first durable attempt, with a short backoff.
Restart does not reset this budget. Every retry uses the same key and frozen
payload and rechecks guest eligibility under the guest database lock. A revoked
recipient requires a new revision and, if guests remain, fresh approval.
AgentMail's automatic SDK reply retries are disabled, and mutating HTTP request
bodies cannot be transparently replayed, including on redirects. No provider idempotency
retention window is assumed; sending an idempotency header is not proof of one.

After an uncertain preview or final send, recovery only reconciles receipts;
it never blindly retries that send. Missing, ambiguous or mismatched receipts
leave it held. Matching checks the scoped Sent message, reply parent, complete
envelope, plain text, HTML and attachment metadata/bytes. Plain text permits
only CRLF/LF normalization; HTML matches `RawHTML` exactly. AgentMail, OpenMail
and Sendmux adapters retain provider HTML in that field. Whitespace trimming, HTML rendering
equivalence or lossy provider summaries are insufficient. Provider rewriting
can therefore leave a valid send held for diagnosis. A matching Sent copy is evidence of matching content at the provider, not
destination delivery. Without provider evidence tied to the attempt key, it
cannot distinguish an older byte-identical send to the same parent and envelope.
Recovery never uses that observation to authorize a resend.

An authenticated header-only `yes`/`no` whose reply reference is not yet known
while a preview send is uncertain in the same thread remains unprocessed until
receipt reconciliation can resolve it. It neither starts agent work nor becomes
approval merely because the thread matches.

An exhausted or uncertain send produces one private notice attempt per phase
(preview or submission), record and revision. Errors while merely awaiting
approval do not send or reserve a hold notice. Submission notices explain that
the reply may already have reached its recipients and will not be sent again
automatically. Recovery holds and logs a failing record while continuing other
records and inbox polling. A scope mismatch never sends old
owner information to a replacement owner.

Consumed, authenticated owner replies that are invalid, superseded, ambiguous
or not bound to a preview receive private guidance. Feedback neither approves
nor executes work. Each notice is durably reserved before sending, keyed by the
consumed message or held phase and revision. A replay or restart cannot create
a second attempt. If the notice itself fails or its response is lost, it remains
visible in status; delivery of the notice is not guaranteed and it is not
blindly retried.

`dearmachine status` and `dearmachine status --json` show unresolved approval
states and hold reasons, plus unconfirmed private notices. They read metadata
without migrating databases or loading answer bodies, attachments or tokens.

Historical notices with a shared preview/submission key remain unchanged on
upgrade. They do not suppress the new phase-specific notices, so an existing
hold may produce one additional notice after upgrade.

Private pair-database diagnostics are in `outbound_approvals`, keyed by send key
and revision. Records retain the prepared pair/inbox/owner/request/thread scope,
`State`, `HoldReason`, preview/submission grant snapshots, issued preview
payload/receipt history and decision ID, sender and authentication evidence.
Superseded revisions preserve their history. States are `prepared`,
`preview_sending`, `pending`, `approved`, `sending`, `sent`, `rejected` and
`superseded`. Uncertain-send states retain a preview or submission receipt
reconciliation hold reason. Inspect only necessary metadata; records contain
private answer/file bytes and reference tokens and must not be published or
edited to manufacture approval.

The [validation record](../verification/guest/VALIDATION.md) documents passing
scoped AgentMail/OpenMail live checks and their provider rewriting boundary.
The formal approval and recovery models and Go regressions do not establish a
Go refinement/composition proof or actual provider delivery.

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

The check always applies to owner instructions, invitations, approvals and
removal commands. Guest requests default to the same check; the explicit
owner exception below changes only guest sender eligibility. A signature that omits a present authorization
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

## Unverified guest messages

An existing guest's message that fails authentication is held without executing
it or creating an instruction approval. DearMachine sends a private owner warning
with a quoted preview, a plain-language impersonation warning, and a technical
reason. An authentication failure is not evidence that the sender is malicious;
incorrect mail settings or unavailable evidence can also prevent verification.
Transient transport and DNS outages remain operational errors, not invitations
to bypass authentication.

To accept the risk, the authenticated owner replies in the same thread with the
exact command supplied in the warning (case insensitive):

```text
ALLOW UNVERIFIED <code>
```

This remembers an exception for that owner pair, inbox, guest, thread and current
grant generation. It is **not proof of identity**: anyone impersonating that
address could submit a request. Every message still needs its own Yes/No/Other
approval, including the first held message. A plain Yes to the warning does not
accept the exception. Risk acceptance does not confer owner powers or invite
other addresses. Future verified messages continue through the ordinary path.

Removing the guest invalidates the exception and old commands. Explicitly
reinviting the guest requires fresh risk acceptance for unverified mail. Merely
omitting a guest from an owner continuation does not remove the guest or exception.
After outbound approval, approved unverified requests send their answer to the owner and that requesting
guest only; their unverified To/CC cannot subscribe other guests to the answer.
Owner continuations retain the ordinary recipient rules.

There is at most one warning per grant generation, with at most three warning
attempts per owner pair per rolling hour. Rate-limited warnings are deferred.
Held message IDs and content fingerprints persist across restarts; the provider
message is marked read and retrieved again when acceptance permits approval.
Changed content under the same ID is rejected. A send with an uncertain result
is recovered only through its matching private Sent receipt, without a blind
resend. If delivery never becomes observable, concierge diagnosis is needed;
no automatic delivery or exactly-once guarantee is claimed.

AgentMail polling explicitly includes messages labeled `unauthenticated` for
this diagnostic path. Unknown senders, paired-owner impersonation, wrong scopes,
and revoked guests do not receive exceptions or generate owner warnings.
The client logs authentication failures, notification outcomes, exception
acceptance/rejection and email removal in `dearmachine.log`, with quoted IDs,
claimed sender, scope and safe reasons; it does not log message bodies or keys.
Repeated held-message outcomes are deduplicated. Launch `dearmachine` and ask
about the warning's authentication reference for diagnosis. Guest authorization
state and notice tokens are local private data, not credentials for a sender.

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
The outbox persists the frozen payload and To/CC envelope before previewing it.
First submission saves eligibility evidence while holding the grant lock across
the send attempt. Receipt reconciliation retains that payload, envelope and
idempotency key even after revocation; it does not resend uncertain mail.
Already-submitted uncertain deliveries may complete.

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
and `References` and keep a durable Sent receipt. A local submission journal
persists intent and identifiers before remote changes. When a response is lost
and provider reads lag, it waits for a positive receipt instead of resubmitting. The outbound relay may rewrite
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
