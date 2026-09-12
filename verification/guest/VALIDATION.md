# Guest authorization validation

The implementation has deterministic and finite-model coverage. It is **not a
completed live participant evaluation**. Current adapters cannot authenticate
the exact controller mailbox required for automatic invitations, and therefore
leave that capability disabled.

## Verification scope

The pinned runner completed Gobra verification of the three production Boolean
policy functions with zero errors. TLC exhausted these configurations:

| Configuration | Distinct states | Generated states | Maximum depth |
| --- | ---: | ---: | ---: |
| One scope | 664 | 3,969 | 10 |
| Two threads sharing a provider entry | 44,884 | 464,749 | 17 |
| Sparse two-pair/inbox/guest/thread matrix | 7,054,336 | 77,194,240 | 19 |

All four weakened models produced the expected invariant counterexample.
The [verification guide](README.md) states the bounds and trusted assumptions;
these results do not establish Go/SQLite refinement or model obedience.

The Go suites cover persistence, replay, generations, exact routing and visible
replies, separate admission/decisions/trust, stale and recovered work, the
subprocess-start boundary, provider references and ownership, concurrent
reconciliation, alternate retrieval paths, and native CLI commands. The
credential-free container runner executes the full suite, race tests and vet
with network isolation. Concierge's actual management prompt has a command
contract test in the installer repository.

## Live provider diagnostic, 2026-09-12

A production Nix-built OCI image exercised native `guest allow/list/revoke`
against one disposable AgentMail inbox and two disposable OpenMail inboxes.
Each command ran in a new container using the same isolated client home.
The pair registry was seeded for this diagnostic; no daemon, Compose backend,
participant admission exchange or model execution was involved.

Both providers delivered two controller invitations with the guest visibly in
CC. For each receiver, explicit CLI grants created an owned receive entry and
two durable thread grants. Revoking one retained the entry; revoking the last
removed it and preserved the controller's entry. Explicit reauthorization
worked, and a separately pre-existing guest entry survived final revocation.
All run-created inboxes were deleted and their absence verified. No production
inbox or normal service state was used as disposable state.

The first OpenMail removal exposed a missing inbox scope in the DELETE request.
Local revocation committed and the command truthfully reported synchronization
pending. A failing adapter regression reproduced the 403; adding `inboxId` fixed
it and the complete provider diagnostic then passed. This is an actual provider
observation, separate from the injected outage/response-loss tests.

The Sendmux infrastructure preflight returned HTTP 401, and no valid scoped
mailbox credential was available. Its live row is **blocked**, while its
deterministic adapter tests cover receive-only changes, ETag ownership and
preservation of unrelated rules.

## Remaining live evaluation

Automatic To/CC invitations, private admission and independent instruction
decisions, one lower-authority model execution, provider-delivered unauthorized
threads, stale approvals across daemon restart, and the backend-specific model
behavior probes still require the full linked runbooks. The explicit CLI
diagnostic must not be counted as a passing automatic-invitation scenario or as
a passing Compose/participant evaluation. Automatic success additionally needs
a supported exact-mailbox attribution contract.

During final offline validation, a manual-clock preemption test exposed a race
between computing a relative timer and advancing simulated time. The fixture
now waits for timer registration; the affected preemption and OpenMail tests
passed 100 race-enabled repetitions. A separate failing-first integration test
also exposed approval of new guest work on a historically granted provider
thread without a local session. Approved work now creates that session in the
same transaction, without turning the old invitation into executable work.

## Per-request answer recipients, 2026-09-12

Task answers now put the owner in To and copy active guests for the exact
pair/inbox/thread who appear in the original instruction's From, To or CC.
Owner continuations omitting guests stay private. Admission and approval
prompts remain private, and the first submission persists its recipient
envelope so retries cannot expand it.

Failing-first regressions cover shared owner answers, approved guest answers,
private continuations, recipient encoding and separate provider permission
directions. Additional tests cover revoked and unrelated guests, BCC exclusion,
restart/retry envelopes, private-prompt versus result receipts, SQLite migration
and uncertain outbound permission ownership. The complete offline Go suite,
race tests and vet passed, as did the host Nix checks. Installer prompt contract,
typecheck, full tests and runtime build passed. The existing Boolean proofs
are unchanged; they do not prove the new recipient selection or persistence.

Two disposable cross-provider diagnostics confirmed candidate reconciliation
created the AgentMail receive/reply/send entries and OpenMail inbound/outbound
entries. OpenMail-to-AgentMail guest requests arrived, but the reverse guest
request did not arrive within the diagnostic deadlines despite AgentMail
accepting it. Scoped OpenMail rejection audits returned no entries; the cause
was not established. These runs stopped before sending candidate task answers.
An OpenMail-only diagnostic then stopped at HTTP 429 while sending an
invitation. All created inboxes were deleted and absence verified. Consequently
provider-delivered shared answers and private continuations remain unverified
live; these diagnostics are not a completed participant/model evaluation.
