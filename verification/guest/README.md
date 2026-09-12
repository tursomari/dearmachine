# Guest authorization verification

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
