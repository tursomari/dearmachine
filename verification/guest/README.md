# Guest authorization verification

The [validation record](VALIDATION.md) separates current checks from historical
provider diagnostics and remaining live evaluation. The
[operational contract](../../docs/guest-authorization.md) describes the shipped
workflow and authentication assumptions.

This suite has three separately checked TLA+ specifications. `Guest.tla` covers
grant generations, authenticated work and provider reconciliation.
`Participation.tla` covers automatic owner To/CC invitations, an independent
private approval for every guest message, recipient privacy and explicit removal.
`Replacement.tla` covers pending owner replacements, repeated polling, completion
and restart recovery. None is a complete refinement proof of the Go application.

```console
docker pull ghcr.io/viperproject/gobra@sha256:d9dc17cdb3725818943a6224872628610e130c349e059ad393ef4f88878cca19
python3 verification/guest/run.py --output /tmp/guest-proof-results --expand
```

Run from the repository root. The output directory must be new and outside the
checkout. `--docker-host unix:///var/run/docker.sock` selects another socket.
`--tlc-jar` accepts the pinned TLC release jar. The runner records exact sources,
configurations, hashes and logs. Only dependency download needs networking;
checks use isolated containers with source-only snapshots and no credentials or
Git plumbing.

## Production Boolean contracts

The runner removes only `// @ ` from contracts in
`dearmachine/internal/client/guest_policy.go` and submits the actual declarations
and bodies to Gobra v26.02. There is no separate proof implementation. The six
functions cover invitation, eligible visible reply delivery, approved
execution, answer-recipient eligibility, sender eligibility under a scoped risk
exception, and authenticated-owner exception acceptance. Exhaustive Boolean Go tests exercise
the same production functions independently.

Gobra proves those Boolean implications. It does not verify email parsing,
cryptography, SQLite transactions, adapter implementation or the full program.

## Participation workflow

Each scope abstracts an exact owner pair, provider inbox, guest mailbox and
provider thread. `Invite` uses authenticated owner evidence with Dear Machine
and the guest in outer To/CC. Initial invitation needs no separate admission.
Invitation processing precedes `ReceiveOwner`. `ReceiveGuest` holds an immutable
request. Each private approval binds its original message, scope and generation;
`Approve` releases only that request and `Start` checks the grant again. There
is no retained approval shortcut. Authentication is required independently for
invitations, owner instructions, approvals and revocations. Guest identity may
instead be unverified under an explicitly accepted current-generation exception.
`WarnGuest` holds immutable work at stage 5; `AcceptException` requires an
authenticated owner and the exact warning scope/generation. `ReleaseHeld` moves
that work only to awaiting ordinary approval. It cannot approve or start work.
`ReceiveGuest` may use the same exception for later requests, each independently
approved. `Start` requires verified identity or the current explicit exception.
Removal disables it immediately; reinvitation cannot reuse the old exception.
The model retains verified identity as a separate work fact.

`Submit` includes the owner and only guests visible in the original request
whose grant remains active in the same generation recorded at acceptance.
Production encodes this as To owner, CC guest(s). `Retry` keeps the first
submission's envelope. Owner omission neither copies nor revokes the guest and
does not invalidate pending approvals.

`Revoke` binds an authenticated owner command to the exact current generation.
Production renders a copyable `REMOVE GUEST <code>` in private prompts and
owner-only answers. The model abstracts token secrecy, parsing and rendering.
Revocation blocks unstarted guest work and old approval tokens. Its tombstone
prevents both replay and ordinary fresh reply-all from restoring access.
Explicit local reinvitation can deliberately reference an earlier qualifying
invitation; subsequent guest work needs fresh requests and approvals. Already
started effects and submitted mail cannot be recalled. Grants have no expiry.

The default bound has one scope, two guest messages, one owner instruction,
two evidence IDs and two generations. The expanded configuration has two scopes
with one guest message and one owner instruction each, and one invitation
evidence identity (the default retains two). Twenty-two intentional
mutations must fail `Safety`: forged senders in five roles, replay/implicit
reinvitation, misbound/reused approvals, public prompts, revoked/stale execution,
historical/retry recipients, omission-as-revocation and wrong-scope actions.
Four separate reachability checks must find repeated shared guest answers,
private owner answers while participation remains active, and an answer after
explicit reinvitation, plus repeated approved unverified guest answers. `AnswerPrivacy` independently checks submitted envelopes.
The five exception mutations cover forged acceptance, wrong scope, stale tokens,
stale remembered exceptions and acceptance that also approves work. The runner
requires invariant-failure exit status, not arbitrary tool failure.

Messages and evidence are immutable identities, not raw MIME. Restarts retain
modeled state. The model omits multi-guest envelope composition, database
atomicity, parsing, actual network delivery, process scheduling and agent
obedience. Tests provide implementation conformance evidence; bounded safety
exploration does not prove unbounded liveness.

## Replacement execution and recovery

`Replacement.tla` models the persistence layer omitted by the authorization
specifications: owner `Other`, replacement claim, execution, saved result,
provider submission, atomic completion, acknowledgement, repeated polling and
crash/restart between steps. Pending execution takes precedence over control
correlation. Completion advances the sequence and removes pending work in one
transaction. Receipt recovery uses the existing provider answer after an
uncertain send; a saved result must not start a fresh agent invocation.

Four initial-state fixtures check a clean workflow, the old empty control record
with an unsent or already-sent saved answer, and an unrelated receipt conflict.
Five mutations must produce specific counterexamples: repolling pending work as
control traffic, forgetting the sent receipt, executing a saved result again,
repairing an unrelated receipt, and removing legacy repair. The last mutation
violates conditional progress; the others violate safety. Progress requires
fair work, eventual uninterrupted uptime and reliable provider receipt lookup.
Unrelated conflicts intentionally fail closed and have no completion guarantee.

This is a finite, single-replacement abstraction. It assumes authenticated,
immutable message identity and matching owner-only receipts; it does not compose
those assumptions with `Participation.tla`. SQLite transactions are modeled as
atomic steps, not verified from SQL. Running-process/checkpoint recovery is
abstracted; no exactly-once guarantee is claimed for agent effects before a
result is durable, or for a provider with unreliable receipt visibility.

Implementation correspondence lives in `participant_replacement_test.go`:
real subprocess-start repolls exercise `pollAndClaim`; reopened SQLite recovery
exercises `processWork`/receipt lookup and `Store.Complete` with sent and unsent
results. Negative tests preserve unrelated receipts, other threads and ordinary
work, require a ready result and receipt, and roll back repair if sequence
advancement fails. Thread aliases are also covered. These tests connect the
model to production behavior but are not a formal Go refinement proof.

## Provider reconciliation model

`Guest.tla` maps `Invite` to grant persistence, `Revoke` to local revocation,
`Receive` to authenticated immutable work correlation, `Approve` to an individual
owner decision and `Start` to the generation check across subprocess start.
`Add`, `Remove` and `Inspect` separate durable intent from provider effects.
Lost responses preserve uncertain ownership; restarts retain durable state.
Permanent-pair provider needs coexist with guest references.

The expanded runs add two threads sharing a provider entry, then a sparse matrix
of two pairs, inboxes, guests and threads (two diagonal keys). Four mutations
must violate safety: known-thread-only delivery, stale-generation execution,
replayed invitation resurrection and deletion without ownership.

This reconciliation abstraction applies independently to each permission
direction. AgentMail receive, reply and send records share the grant reference
predicate but retain separate ownership. Directional migrations, shared needs,
partial failures and lost responses have Go tests. The TLA+ models are not
composed into a refinement proof. Eventual reconciliation requires provider
availability, fair retries and eventually stable desired state.

## Trusted boundary and implementation evidence

The local operator, pair registry, configured provider endpoint, SQLite state
and platform process-start semantics are trusted. Canonicalization uses Go's
`net/mail` and lowercases the full mailbox, matching pairing; it does not expand
aliases or strip plus tags. Stable provider message/thread IDs route exact scopes
but do not themselves authenticate senders.

Production AgentMail authentication verifies raw RFC822 DKIM locally. One valid
signature must come from the exact From domain and cover all present
security-relevant headers and the full body. It also binds parsed author,
recipients, subject, message ID and approval correlation to normalized metadata.
**The domain's mail operator is trusted to enforce mailbox ownership.** The
signature proves the domain's authority, not independent control of a local part
or the human account holder. This assumption supplies the model's authenticated
mailbox fact; the TLA+ code does not establish it. Provider body/MIME extraction,
attachment mapping, inbox/thread scope and outbound labels remain trusted.
No raw From match or supplied Authentication-Results verdict replaces verification.
An explicit owner exception accepts the risk of impersonation in a narrow scope;
it does not establish the model's authenticated mailbox fact.
OpenMail verifies the same signature contract for reconstructable, unencoded
single-part plain text. Unsupported MIME formats and missing evidence are
rejected. Its provider IDs are mapped separately from signed Internet
Message-IDs using scoped outbound records. Sendmux verifies original MIME fetched
through mailbox-scoped TLS IMAP. Its SMTP filters do not represent authenticated
From identities, so local authorization replaces managed address entries.
Sendmux private approval references are matched only in authenticated owner
bodies against private Sent records in the same inbox and thread. Nonce secrecy,
provider storage and parsing remain trusted boundaries; the model abstracts
correct correlation and does not prove these provider adapters.

The explicit native `guest allow` path instead carries trusted local operator
authorization, with provider-resolved visible invitation facts. It cannot create an authentication exception or bypass individual guest approvals.
Only the separate authenticated owner risk decision enables unverified guests.

Go regressions cover real signature verification and SDK raw downloads, all
unauthenticated roles, scoped removal/replay/restart, fingerprint substitution,
independent approvals, private continuations, generations at acceptance and
submission, receipt recovery, adapter recipient encoding, and the database lock
held through execution start. Provider deletion additionally requires exact
ownership and observed identity/version. Providers without conditional deletion
require external operators not to replace a rule between inspection and removal.
External provider correctness and out-of-band mutations are outside the proof.

## Exception implementation correspondence

`guest_auth_exception_test.go` exercises private warning/ordinary approval
separation, continuing per-message approval, generation reset, wrong-owner and
unauthenticated decisions, restart, deferred rate limits, immutable held bodies,
uncertain-send recovery, escaped content-free logging and atomic current-generation
binding. `agentmail_test.go` verifies both unread and recovery SDK queries include
quarantined messages and that acknowledgement prevents repeat polling.
`sender_auth_test.go` separates temporary DNS failures from identity rejections.

Notification delivery, rate limits, SQL atomicity and provider behavior are tested
implementation boundaries, not claims proved by the Boolean contracts or TLA+.
The model's single guest per scope abstracts multi-guest envelope composition;
production additionally prevents unverified To/CC from adding answer recipients.
The reconciliation and replacement specifications retain their authenticated-input
abstractions and are not composed with the exception model. There is no full Go
refinement proof or guarantee of actual email delivery.
