# Guest authorization verification

The [validation record](VALIDATION.md) separates current checks from historical
provider diagnostics and remaining live evaluation. The
[operational contract](../../docs/guest-authorization.md) describes the shipped
workflow and authentication assumptions.

This suite has six separately checked TLA+ specifications. `Guest.tla` covers
grant generations, authenticated work and provider reconciliation.
`Participation.tla` covers automatic owner To/CC invitations, an independent
private approval for every guest message, recipient privacy and explicit removal.
`Replacement.tla` covers pending owner replacements, repeated polling, completion
and restart recovery. `Outbound.tla` specifies owner approval of
guest-visible answers, now implemented by the runtime durable outbox gate. None is a
complete refinement proof of the Go application. `OutboundRecovery.tla` is a
separate companion for recovery and owner feedback. `OutboundScheduling.tla`
checks outbox progress while unrelated execution or maintenance stays busy.

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

## Outbound approval contract and runtime correspondence

`Outbound.tla` is unchanged by the runtime implementation. The existing
participation model still describes direct submission after execution. The
models are checked separately; composition and a Go refinement proof remain
future work. The six Gobra-verified production predicates below are unchanged;
the runtime outbound gate is additional code outside those Boolean proofs.

A scope identifies the exact owner pair, provider inbox and original thread.
`Accept` captures original recipient visibility and each active guest's grant
generation. `Prepare` abstracts the completed result of upstream execution.
Neither owner nor guest origin bypasses outbound approval. Guest execution and
its instruction approval are assumed upstream; the model does not represent an
instruction-approval record or claim a composed proof of the two approvals. The
gate is intended for every guest-visible reply, including error/status answers;
the model does not enumerate the Go sending call sites.

The modeled workflow is:

1. Prepare an immutable answer containing body, attachment identity, recipients,
   per-guest generation bindings and a false approval-banner flag.
2. Check recipient eligibility and send an owner-only preview. Its payload is
   exactly the draft with the banner set to true; proposed recipients are part
   of the preview, not its actual transport envelope. Record the issued revision,
   complete preview payload and actual owner-only envelope in `issuedPreviews`.
3. Accept an authenticated owner's Yes or No correlated to an actually issued
   preview of that exact request and revision. Record the sender, authentication,
   value, request and revision independently of the resulting approval status.
   No terminates that proposal. Other replies leave it pending.
4. Recheck eligibility and submit the exact approved draft without the banner.
   Record the actual grant state and eligible guests at first submission.
   Owner-only replies need no approval, even in a previously shared thread.

**Recipient changes require re-preview when any guests remain.** If the owner
removes a guest while a preview is pending, a Yes to that old preview cannot
release the old envelope. Dear Machine must prepare a new revision with the
reduced recipients, send a new private preview and obtain a new Yes. If no guests
remain, the owner-only exemption applies. A guest removed before preview creation
must not appear in a newly sent preview: `Preview` checks current eligibility,
requiring redrafting first. This model allows shrinking the original recipient
set, not adding new recipients to an old request.

Content or attachment changes likewise invalidate approval. Redrafting clears
the decision and approved payload while preserving the original acceptance
generations and issued-preview history. Approval must match an issued record for
the current revision and exact payload; an older preview cannot authorize the
revised answer. Revoking and reinviting cannot subscribe the new generation to old
work; fresh work accepted after reinvitation can receive its own preview and
approval. The finite `MaxRevision` bound can leave a revoked-while-pending answer
permanently unsent, including after a valid Yes. This is intentional bounded
safety, not a liveness or eventual-delivery claim.

Once submission may have occurred, retries retain its first payload, envelope
and submission-time eligibility evidence. Later revocation does not retroactively
invalidate that submission. Submission is not proof of delivery or recall.
Correct `Restart` is a stutter: durable state preservation is **assumed**, not
proved. Its mutation checks that recovery cannot invent an approval without a
recorded owner decision; it does not verify crash/write ordering or receipt
recovery.

The outbound model has no `violation` variable or `Safety` flag. Its independent
state invariants are:

| Invariant | Property |
| --- | --- |
| `PreviewPrivacy` | Owner-only envelope; complete preview with the banner. |
| `PreviewEligibility` | Every previewed guest was eligible when that preview was sent. |
| `ApprovalEvidence` | Recorded Yes/No decisions are authenticated and bound to the exact owner, request and revision; approval or shared submission specifically requires Yes. |
| `ApprovedWhatWasPreviewed` | Approval and shared submission require an issued owner-only preview of the exact draft and decision revision. |
| `ReleaseWithoutBanner` | Every submitted payload has its banner cleared. |
| `RetryIdentity` | Retries preserve the first-submission payload and envelope. |
| `ApprovedDisclosure` | Submitted content matches the prepared draft and, when shared, the exact approved draft. |
| `SubmissionEligibility` | Recipients match the saved eligible set and were active on the accepted generations at submission. |

`TypeOK` checks all state types. Preview/submission eligibility uses recorded
historical grants, not current grants that may subsequently be revoked.

The runner maps each of 23 mutations to its required named invariant failure,
with all state invariants enabled. The approval bypass is tested separately for
owner and guest origins, for 24 negative checks. There are no separate mutations
claiming to model instruction-approval reuse. Tests cover forged or misbound
decisions, an old token incorrectly authorizing the current revision, No/other
treated as Yes, stale previews, revoked/stale grants, approval bypass/reuse,
rejection, content/attachment/recipient substitution, banner leakage, mutable
retries and recovery inventing approval. `ApproveUnseen` permits a valid owner
Yes from `ready`, skips the issued-preview lookup and approves the draft directly;
`ApprovedWhatWasPreviewed` must catch it even though the other decision fields
are valid. TLC must exit 12 and name the expected invariant as its first reported
failure. A mutation may violate multiple invariants; parser/tool failures or a
different first-reported invariant cannot pass the check.

Nine default and two expanded positive reachability checks require valid owner,
guest, private, repeated and independent answers; rejection; reapproval of an
already-approved edited draft; fresh generation-2 work; the finite revision-limit
hold; a multi-guest answer; and new approval after reducing a recipient set.
These expected witness-invariant failures retain every safety invariant. The
default bound has one scope, an owner and a guest request, one guest, two
revisions and two generations. Expanded safety checks cover two scopes and two
guests in one envelope.

The normal full runner includes outbound checks. To run only this contract:

```console
python3 verification/guest/run.py --outbound-only --expand --output /path/to/new-proof-artifacts
```

Choose a new output directory outside the checkout. Artifacts include the exact
models, configurations, runner, production Boolean source, logs, pinned-tool
identifiers and a `SHA256SUMS` manifest. See the [validation record](VALIDATION.md)
for the full-suite reproduce command and results.

An outbound decision must resolve to the approval reference of an **issued
outbound-preview message** for the exact scope, request, revision and payload.
Instruction-approval and outbound-approval references must have distinct
namespaces or type tags. An instruction-approval reference must never resolve to
an outbound decision, even if an outbound preview for the same request is already
pending. Looking up approval by request or thread alone is insufficient.

Allocating a reference or creating pending state does not establish issuance.
`Preview` abstracts successful private issuance and durable history recording as
one atomic action. Failed sends must not create issued records; uncertain sends
require authoritative receipt reconciliation before decisions can be released.
Issuance does not prove arrival or that the owner read the message. Reference
parsing/type separation, receipt matching and persistence remain implementation
obligations: the model has no instruction-approval records and assumes correctly
resolved outbound references.

The runtime regression obligations include:

- A Yes to the instruction-approval email cannot release that request's answer,
  including when its outbound preview is already pending.
- A Yes with no matching issued outbound preview is ignored: failed-send,
  guessed/unknown and superseded-revision references cannot release a draft.
- A Yes to request R's outbound preview cannot approve request S, including in
  the same thread.

Body and attachment identities abstract serialized content, not MIME parsing,
rendering or file storage. Yes/No abstracts normalization and exclusion of quoted
history, not a verified parser. Authentication, prompt correlation, private
transport envelopes, To-owner/CC-guest encoding, durable writes, provider
idempotency and atomic eligibility-check/submission remain implementation
obligations. There is no composed proof, Go refinement proof, liveness guarantee,
or proof that the current client obeys the gate in every execution.

## Current runtime implementation

`outbound_approval.go` gates every paired shared reply, including shared
error/status replies, through the durable pair-store `outbound_approvals` table.
`approvedReply` freezes text, HTML, file bytes and envelope under a send key;
`advanceOutbound` rechecks the request's accepted grant generations before
preview and first submission. Owner-only replies are exempt. Execution can
complete with a local `outbound-pending:` receipt because the outbox owns the
bytes; that receipt is neither provider submission nor delivery. Routine control
routing reads a small reference projection; recovery loads payloads only for
active records. State/reference projections update atomically with payload
records, and upgrades backfill them without changing pending approvals.

Prepared records persist pair, inbox, owner, request and original thread scope.
Production correspondence to the issued, version-bound reference contract is
explicit: each revision has a random token, stored preview payload and receipt,
preview grant snapshot, and separate decision ID, sender and authentication
fact. Superseded revisions retain their issued-history snapshots. An allocated
token does not count as issuance. `handleOutboundDecision`
requires an authenticated owner, exact scope, one matching issued preview and
newly authored exact `yes`/`no` after case/whitespace normalization and quoted
history exclusion. It gives instruction-approval references precedence rather
than reusing their Yes/No/Other decisions. Before shared release, the saved
preview must equal the current frozen draft with its pending header and private
envelope. Recipient reduction supersedes the old revision and clears decision
evidence; remaining guests require a fresh preview/Yes. No remaining guests
uses the private exemption. Content/file mutation under the same key is rejected;
the model's general edited-draft transition is not an implemented editing UI.

The generated preview carries the exact draft text/HTML/files with the pending
header added to text and HTML; actual To is owner-only and CC/BCC are empty.
Provider branding and quoted-message additions are outside the frozen API
payload; destination checks must also exclude private control history. Shared
submission removes the header and saves the eligible grant generations while
the guest-store transaction orders the first send attempt against revocation.

Intent is durable before network I/O. `preview_sending` and `sending` reconcile
receipts first. An adapter's typed local non-submission failure permits at most
three same-key attempts per phase/revision within 15 minutes of the first
attempt, with eligibility checked again under the guest lock. All uncertain
attempts recover only from a unique matching Sent receipt, without blind resend.
No runtime provider deduplication window is assumed; AgentMail SDK reply retries
and transparent replay of mutating HTTP request bodies are disabled. Matching uses
scope, parent, envelope, text, HTML and attachment metadata/bytes. Text permits
only CRLF/LF normalization; HTML must equal `RawHTML` exactly. AgentMail,
OpenMail and Sendmux adapters retain provider HTML in `RawHTML`. Missing, ambiguous or mismatched
receipts remain held, including provider rewrites that cannot be conservatively
matched. Content matching alone cannot distinguish an older byte-identical send
to the same parent and envelope from the uncertain attempt; exact attempt
provenance remains a provider boundary. It never authorizes a resend. Pair DB
`State` and `HoldReason` provide diagnostic evidence in native status. Failing
records do not stop other recovery or polling. Private hold and invalid-control
notices are reserved durably before I/O and attempted once, so lost responses
or restarts cannot create notice loops; status reports unconfirmed notices. The
[operational contract](../../docs/guest-authorization.md#owner-approval-before-shared-sending)
describes all states and privacy precautions for inspection.

An authenticated header-only exact yes/no with an unresolved reply reference
while a preview is `preview_sending` in the same thread remains unprocessed.
Receipt recovery must establish issuance and reference correlation before the
decision can resolve; it does not become an agent instruction or gain authority
from the thread alone.

`outbound_approval_test.go` contains regressions for frozen payload/restart,
exact decisions and terminal rejection, authentication/scope/reference separation,
recipient reduction and reinvitation, removal, payload/key substitution,
uncertain preview/submission recovery, delayed header-only decisions, HTML
mismatch, shared status replies, private exemption and concurrent revocation.
The guest regression tests explicitly approve outbound previews before checking
shared answers. Run the source client suite and outbound race tests in a
credential-free container; Nix checks and live provider delivery validation
remain separate obligations. See the [validation record](VALIDATION.md) for
results and the scope and revision of each run.

Trusted boundaries still include authentication and authored-body parsing,
provider ID/reference mapping, normalized MIME and file extraction, Sent labels,
transport submission semantics, SQLite durability and transaction ordering, and
the local operator/platform. A provider success receipt or exact reconciled Sent
copy is treated as issuance/submission evidence, not proof of arrival or reading.
The formal model abstracts these boundaries; no Go refinement or composed proof
of instruction approval, execution, outbox and delivery is claimed.

## Outbound recovery and feedback companion

`OutboundRecovery.tla` and `OutboundRecovery.cfg` check a bounded recovery
contract separately from `Outbound.tla`; they are not a composed proof or a Go
refinement. Preview and submission retain separate phase fixtures. Each has
three durable attempts per revision, with a 15-minute retry budget abstracted to
one time unit starting at its first call. Waiting for approval does not consume
submission budget. Safe recipient redrafting resets the new revision's budget;
same-revision retries and restarts cannot refresh it. Exploration bounds time at
two units and revisions at two. Preview keys change with revision; submission
keys remain stable. These are provider request keys, separate from notice keys.

The `PhaseFlow = TRUE` fixture follows one record through a preview sending
hold, matched-receipt reconciliation, abstract exact owner approval, and a
submission sending hold at the **same revision**. Both operations retain their
attempt history; submission starts its own budget. This fixture fixes revision
at one and bounds provider attempts at one per phase; the separate fixtures
retain three attempts and safe redrafting through revision two. No composition
with the owner-decision model is claimed.

The default `VerifiedWindow = FALSE` permits retries only after authoritative
typed evidence that no mutating provider call occurred. Every retry reconciles
first, stays within its attempt/deadline bounds, and atomically rechecks current
eligibility. Unknown outcomes cannot resend or redraft; they remain held unless
receipt reconciliation resolves them, including receipts found after a hold
notice. The separate `VerifiedWindow = TRUE` fixture assumes a hypothetical
external deduplication contract. It does **not** establish a provider guarantee
or describe runtime support; runtime assumes no verified provider window.

Seventeen state invariants cover types and attempt bounds, durable deadlines,
retry evidence, at-most-one provider acceptance, current eligibility, frozen
payload/key identity, safe redrafting, notice privacy and at-most-once attempts,
visible notice/hold status, no invented approval, and premature deferral. Held
notices are identified by record/revision/phase; consumed-invalid feedback by
message ID. Only sending holds are eligible: generic transient failures before
sending or while awaiting the owner, initial eligibility holds and broken records
cannot reserve or consume a hold notice. Reservation records retain their
eligibility and canonical key as history for independent invariants. Every eligible unreserved notice remains
reservable even when the other phase's notice is already reserved. Durable
reservation precedes notice I/O. A crash can leave a notice unsent,
and failure remains visible: notice delivery is not guaranteed. Malformed,
stale, ambiguous and unbound controls are abstract consumed-invalid inputs;
premature controls defer without consumption or invalid feedback. Classification,
authentication, receipt authority, durable storage and lock ordering are trusted
boundaries, not parser, adapter or database proofs.

The runner includes 56 companion checks: six successful baseline/property
checks, 32 expected-failing reachability witnesses, 17 safety mutations and one
liveness mutation. New witnesses require both notice attempts on revision one,
including a failed preview notice and a preceding non-sending transient failure;
an interrupted preview reservation also allows a submission notice. A dedicated
waiting-owner witness completes the preview, encounters a non-sending transient
failure, recovers to the same pending state, then reaches a submission hold and
notice at revision one with the preview notice still unreserved. Preview `done`
is the pending owner-decision state in this fixture; `BeginSubmission` abstracts
exact owner approval. Fault recovery restores the saved phase and cannot use
receipt reconciliation to bypass that pending state. Mutations collapse phase
storage keys, let a preview reservation suppress submission, reserve notices for
pre-send and waiting-owner failures, and separately forget preview or submission
reservations on restart. All checks retain every safety invariant; expected
failures must identify the named invariant and TLC exit status.

The two-record isolation fixture assumes fair scheduling and a reliable provider
for the healthy record, with clock/revocation interference disabled. Under those assumptions a permanently failing record cannot prevent
healthy-record completion or polling; this is not unconditional send liveness.

The full runner and `--outbound-only` include the companion. To check only it:

```sh
python3 verification/guest/run.py --recovery-only --output /path/to/new-recovery-artifacts
```

## Outbound scheduling progress

`OutboundScheduling.tla` isolates a scheduling obligation omitted by the earlier
recovery-isolation fixture: polling must advance the outbox even while an agent
worker or maintenance owns an execution lane. The runtime now calls
`recoverOutbound` at the end of `pollAndClaim`, after processing the entire
incoming batch, including decisions and removals. Dispatch and maintenance both
use that polling path. Initial and post-dispatch recovery remain in place.

The model starts with an issued immutable preview and assumes a correctly
authenticated, correlated owner decision from the outbound contract. A poll
records approval; the following outbox pass checks eligibility and attempts
submission. Unrelated work may remain busy forever: its completion is not a
fairness assumption. With fair completed polling/outbox passes, stable eligible
recipients and a responsive reliable provider, the approved reply eventually
submits. Fairness assumes local database/lock operations finish; arbitrary
provider outages, endless cancellation and process crashes have no delivery
guarantee.

Fifteen checks cover idle, busy-worker and busy-maintenance schedules, successful
submission before the busy lane finishes, uncertain acceptance without retry,
and revocation requiring redrafting. Mutations restore the old wait-for-idle
dependency, skip eligibility or retry uncertain sends. The wait-for-idle
mutations must fail the temporal progress property even though polling continues.
All safety checks remain enabled for witnesses and mutations. The model is a
separate bounded abstraction, not a composition or Go refinement proof; immutable
payloads, exact approval and recovery details remain in the other models.

`outbound_scheduling_test.go` exercises both production polling loops with a
blocked execution lane, real SQLite state and the fake transport. It requires
submission of the exact approved content and envelope before releasing the lane,
and rejects repeated submission on approval replay. A same-poll approval and
removal must redraft before any guest-visible submission. These are deterministic
scheduler regressions, not live-provider delivery evidence.

The full runner, `--outbound-only` and `--recovery-only` include scheduling.
To run just this companion:

```sh
python3 verification/guest/run.py --scheduling-only --output /path/to/new-scheduling-artifacts
```

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
