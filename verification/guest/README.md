# Guest authorization verification

See the [validation record](VALIDATION.md) for finite-state results, live
provider diagnostics and the remaining participant evaluation gaps.

This suite contains two specifications. `Guest.tla` and the Gobra contracts
describe the existing admission/approval policy and provider reconciliation.
`Participation.tla` specifies the proposed owner To/CC invitation workflow with
one approval per guest message. It is a design verification, **not a claim that
the current application implements that workflow or rejects forged senders**.

Run from the repository root after pulling the digest-pinned Gobra image:

```console
docker pull ghcr.io/viperproject/gobra@sha256:d9dc17cdb3725818943a6224872628610e130c349e059ad393ef4f88878cca19
python3 verification/guest/run.py --output /tmp/guest-proof-results --expand
```

Use `--docker-host unix:///var/run/docker.sock` when the selected socket is
unavailable. The output directory must be new and outside the checkout.
`--tlc-jar` accepts an already-downloaded TLC 1.7.4 jar with the pinned SHA-256.
Only the dependency download uses the network; every check runs with network
isolation and a source-only snapshot, without credentials or Git plumbing.

The runner verifies the exact functions in
`dearmachine/internal/client/guest_policy.go`. It removes only `// @ ` from
contract comments and passes the resulting declarations and bodies to Gobra
v26.02. There is no separately maintained proof implementation. Gobra proves
Boolean contracts for invitation, visible reply delivery, and execution
eligibility. It does not verify parsing, database code, adapter implementation,
or the complete application. Exhaustive Boolean Go tests exercise the same
production functions independently.

TLC explores `Guest.tla` and its finite configuration. `--expand` adds a second
thread sharing one provider entry, then a sparse matrix with two pairs, two
inboxes, two guests and two threads (two diagonal scope keys). Four intentionally weakened configurations
must produce a `Safety` violation: known-thread-only delivery, stale-generation
execution, replayed invitation resurrection, and deletion without ownership.
The runner requires TLC's invariant-failure exit status, not an arbitrary
failure such as a syntax error. Logs and the verified Go source hash are retained
in the requested artifact directory.

The same runner also checks `Participation.tla`, its intentional counterexamples
and reachable success scenarios. It retains both model sources and the new
workflow's exact configurations alongside the logs.

## Proposed participation workflow

Each scope represents exactly one owner pair, provider inbox, guest address and
provider thread. Initial authenticated owner To/CC evidence admits that guest
without a separate admission exchange. Every authenticated guest message has
its own durable request, private approval prompt and owner decision; there is
no permanent trust shortcut or thread-wide instruction approval. Approval
tokens bind the scope, original message ID and grant generation. Authentication
is required independently for invitations, owner instructions, guest messages,
approvals and revocations.

`Invite` persists fresh, visible outer-header evidence. Invitation processing
precedes accepting the associated owner instruction in `ReceiveOwner`.
`ReceiveGuest` holds each exact guest request without executing it. `Approve`
authorizes only that request; `Start` rechecks active status and generation.
`Submit` selects the owner and includes the guest only when the original
instruction made the guest visible and its grant is still active in the same
generation. Recipient sets abstract To/CC encoding; the intended encoding is
To owner, CC guest, which needs adapter conformance tests.
`Retry` preserves the first submission's envelope, even if revocation follows.
Owner continuations omitting the guest neither copy nor revoke that guest.

`Revoke` models an authenticated owner command whose token binds the exact scope
and current generation. A copyable footer command can carry that token; its
syntax, parsing and rendering are implementation work. Revocation immediately
blocks unstarted guest work and old approval tokens. It leaves a tombstone:
neither repeated evidence nor an ordinary fresh reply-all restores access.
Restoration requires explicit owner reinvitation with fresh evidence, followed
by fresh guest messages and approvals. Already-started effects and submitted
mail cannot be recalled. Grants have no modeled clock or automatic expiry.

The default bound has one scope, two independent guest messages, one owner
instruction, two invitation evidence IDs and two grant generations. `--expand`
also checks two scopes with one guest message and one owner instruction each.
Seventeen mutations must fail `Safety`, covering forged senders in all five
roles, replay and implicit reinvitation, misbound approvals, public prompts,
reused approvals, revoked/stale execution, historical/retry recipients,
omission-as-revocation, and wrong-scope delivery/revocation. Three additional
expected counterexamples establish reachability of repeated shared guest
answers, a private owner answer while the guest remains authorized, and an
answer after explicit reinvitation. `AnswerPrivacy` independently checks every
submitted envelope's owner, scope and original visibility.

These are bounded safety checks, not a liveness or implementation proof. Scope
values abstract the complete four-part routing key; messages and evidence are
immutable identities, not raw email. Restarts retain all modeled state. The
model does not verify token secrecy, MIME parsing, header provenance, approval
text interpretation, database atomicity, multi-guest envelope composition,
network delivery, process scheduling or agent behavior. Trusted sender evidence
must establish the exact mailbox for this message and inbox; the model cannot
establish that any real transport supplies it.

Provider list ownership, shared references, lost responses and retries remain
checked by `Guest.tla`. Implementing the new workflow must connect its grant
transitions to that existing durable reconciliation path. These two models
are checked separately; no composed refinement proof is claimed.

The existing Gobra contracts and Go conformance tests remain attached to the
current production policy. Implementation must update them together with
adapter authentication tests, individual-message decision tests, revocation
command tests and recipient persistence tests. In particular, the current
delivery/execution Boolean functions do not require sender authentication, and
the existing flow has a separate admission step. Passing this suite does not
close those application gaps or enable automatic invitations.

## Model boundary

`Invite` corresponds to validated invitation persistence, `Revoke` to local
revocation, `Receive` to immutable work correlation, `Admit`/`Approve` to distinct
controller decisions, and `Start` to the grant-generation check held across
subprocess start. `Add`, `Remove`, and `Inspect` separate durable reconciliation
intent from provider effects. Lost responses preserve uncertain ownership.
Restart retains durable state. Permanent-pair provider needs are modeled beside
guest references. Keys include pair, inbox, guest and provider thread.

The selected state space has bounded evidence IDs and two generations. Events
can repeat and interleave, including local mutations between remote operations.
This is exhaustive exploration of that finite abstraction, not an unbounded
proof or a refinement proof of Go/SQLite. Integration tests supply conformance
evidence. The model abstracts work to one slot per scope, omits message content,
and does not model the operating system's process scheduler or model obedience.
It checks safety only. Eventual reconciliation needs eventual provider
availability, fair retries, and eventually stable desired state; no unconditional
liveness claim is made.

## Trusted contracts and sender evidence

The local operator, registry, transport configuration and SQLite files are
trusted. Canonicalization uses Go's `net/mail` parser and lowercases the full
mailbox address, matching the existing pair contract. No plus-tag stripping or
provider-specific alias expansion occurs. Provider message IDs and exact thread
IDs must be stable within the configured provider inbox; they are routing
identifiers, not authentication evidence. SQLite must provide its documented
transaction and locking semantics. The execution check must remain held through
subprocess `Start`; already-started work may have effects after revocation.

Automatic invitations require adapter-supplied attribution of the exact outer
controller mailbox to the provider message and inbox. Neither a raw From match,
allowlisting, delivery, nor generic SPF/DKIM/DMARC success establishes that fact.
Even strictly aligned DKIM/DMARC authenticates a domain: enabling automatic
grants would additionally require a documented submission-provider guarantee
that the authenticated account controls the exact From local part, plus trusted
receiver metadata that the sender cannot forge. No current adapter implements
that evidence contract, so automatic grants remain disabled for all three.

The AgentMail SDK v0.16.0 exposes headers and labels, but no documented exact
mailbox-owner assertion. AgentMail explicitly permits messages labeled
`unauthenticated`: [inbound authentication documentation](https://docs.agentmail.to/knowledge-base/inbound-emails-missing).
OpenMail's [OpenAPI document](https://docs.openmail.sh/api-reference/openapi.json)
and the Sendmux v1.4.1 SDK's selected/raw headers likewise supply no documented
exact-mailbox assertion. Supplied Authentication-Results headers are not
promoted to trusted assertions. These limitations are independent of whether a
live message happens to have passed SPF or DKIM.

Explicit CLI authorization is a trusted local operator's decision scoped by the
provider-resolved invitation and its visible outer recipients. It does not
assert that the email From mailbox was authenticated. Automatic and explicit
paths share outer sender, recipient, inbox, pair and thread validation; the
source of authorization is adapter attribution versus the explicit operator.

Provider deletion requires an exact entry created successfully by DearMachine,
with matching observed identity/version. Lost additions are never adopted as
owned, and uncertain or pre-existing rules are preserved. Providers without
conditional deletion also require external operators not to replace that exact
entry between inspection and deletion. External provider correctness and
concurrent out-of-band mutation cannot be proved by the local model.


## Answer recipients and permission directions

The reconciliation model applies independently to each exact provider permission
entry, including its direction. Receive, reply and send entries share the grant
reference predicate but have separate durable ownership and operation records.
Migration preserves the existing receive entry identity and does not adopt
pre-existing outbound rules. Directional migration, partial failure and final
reference removal have Go conformance tests; no new refinement claim is made.

Current answer-recipient selection and persisted outbound envelopes are covered
by Go integration and adapter tests. They are outside the three Gobra-verified
Boolean contracts and `Guest.tla`. The proposed `Participation.tla` adds abstract
recipient checks but does not prove the current Go implementation. Recipient
selection uses the original instruction's visible headers and exact active
grants when first submitting the result. Retries preserve that already-submitted
envelope.
