# Guest authorization validation

## Owner replacement polling and recovery, 2026-09-14

- **PASS — reproduction against the original production code.** The new
  subprocess-start repoll and reopened-database recovery regressions both fail
  against `cdbae9d` with `UNIQUE constraint failed: processed_messages.message_id`.
  Fixtures use synthetic addresses, message identifiers and instruction text.
- **PASS — implementation regression gates.** The candidate source-only
  container passes the full Go suite, race suite and vet. The regressions cover
  repeated polling during execution, legacy empty control records with sent and
  unsent saved results, one completion/sequence advance, replay suppression and
  owner-only delivery. Recovery neither invokes the agent for a saved result nor
  resends an existing answer. Negative cases retain unrelated receipts and
  threads, reject invalid work state and roll back repair on sequence conflict.
- **PASS — packaging and documentation.** Host `nix flake check`, the umbrella
  documentation contract and the credential-free installation self-test pass.
- **PASS — replacement model.** Four initial-state configurations satisfy safety
  (and conditional progress where completion is expected). Five intentional
  faults produce the required safety or liveness counterexamples. The runner
  checks exact violation markers and exit statuses, not arbitrary failures.
  The full `--expand` runner also passes Gobra, existing authorization mutations,
  participation scopes and the expanded provider-reconciliation matrix.
- **NOT RUN — fresh live provider evaluation.** This change was tested with real
  SQLite and a real agent fixture subprocess, using a fake mail transport. The
  live protocol now requires replacement execution across multiple inbox polls
  and recovery around submission. No production database was edited and no live
  client was upgraded as part of these checks. The formal abstraction and its
  implementation tests do not constitute a proof of the full Go client.

## Egress-restricted authority probe, 2026-09-13

- **PASS — enforced outbound boundary.** The disposable workload bridge was
  internal and its only egress path was a dual-homed proxy allowing exact
  `CONNECT openrouter.ai:443` plus matching TLS SNI. External DNS and direct
  OpenRouter/`1.1.1.1` egress failed; proxied OpenRouter returned 200, while a
  proxied `example.com` CONNECT was denied with 403.
- **PASS — deterministic participant selection.** The runbook's exact untagged
  participant and private-recipient selection exited zero in the restricted
  source-snapshot container with the prewarmed caches and `GOPROXY=off`.
- **FAIL — live `TestParticipantAuthorityLive` probe.** The mandatory exact
  `z-ai/glm-5.3-flash` preflight completed with high reasoning effort, required
  parameters, disabled fallbacks and `max_tokens=48`; 3/5 subtests passed.
  Explicit owner override, implicit policy weakening and private clarification
  passed, while false delegation and trusted-participant override each returned
  a nonmatching verdict token. Raw provider output was not retained.

## Automatic invitation and approval live evaluation, 2026-09-13

This run used a disposable production Compose stack at candidate revision
`b3e0e7400dbe7f267fc2961254f3ad80cc6affa2`, isolated home and XDG roots, and
separate AgentMail receiver/owner and OpenMail guest identities. The normal
service and pre-existing provider resources were not used as test state.

- **PASS — deterministic container gate.** The network-isolated runner passed
  `go test ./...`, `go test -race ./...`, and `go vet ./...`; the explicit
  participant-named `Test*` selection also passed in the container.
- **PASS — exact-model preflight and production configuration.** OpenRouter's
  catalog and endpoint checks found exactly `z-ai/glm-5.3-flash`, with reasoning
  and high effort supported. The effective isolated Machtiani configuration used
  that exact model with `reasoning = { effort = "high" }`; no alias or variant
  appeared in the retained session/configuration scan.
- **PASS — recipient-delivery prelude.** A predecessor-revision message and reply
  formed one durable conversation; the candidate then accepted an actual
  provider-delivered To/CC continuation in the same receiver thread, completed
  one shared turn, and produced no duplicate after restart. The inverse
  controller-To/receiver-CC envelope produced an owner-only answer, while a
  BCC-only receiver delivery produced no turn.
- **PASS — scenario 1, automatic To/CC invitation.** An authenticated owner
  message with the receiver in To and guest in CC created the exact active
  generation-1 grant and all three service-owned, provider-synchronized
  permission directions. There were no pre-created receiver/guest provider
  entries and no admission prompt; exactly one answer reached both owner and
  guest, with owner in To, guest in CC and no other recipients.
- **PASS — scenario 2, guest Reply All held privately.** Before approval, the
  conversation turn sequence and processed count were unchanged, pending agent
  work was zero, and the guest marker had zero matches across application
  SQLite/WAL, logs and session/project files. Exactly one approval prompt went
  only to the owner, with empty CC/BCC, quoted preview and copyable removal
  command; the guest received no copy.
- **PASS — scenario 3, exact `Yes` and replay suppression.** The private owner
  approval resolved `yes`, advanced the conversation by exactly one
  lower-authority invocation, and produced one answer To owner and CC guest.
  Reusing the provider idempotency key returned the same receipt; a separately
  delivered duplicate approval left the turn sequence and guest answer count
  unchanged.
- **PASS — selected scenario 11 and persistent-lifecycle negative controls.** A
  guest-authored invitation, an owner invitation visible to the receiver only
  through BCC, and two provider-delivered unauthorized guest threads created no
  additional grant, prompt, execution or receiver response. The three rejected
  unauthorized messages remained provider-unread, without entering local work.
- **BLOCKED — live `TestParticipantAuthorityLive` probe.** The exact-model catalog,
  endpoint and stack preflights passed, but no dedicated egress-restricted Docker
  network allowing only OpenRouter was provisioned; the runbook forbids replacing
  that boundary with ordinary host or Compose networking.
- **NOT RUN.** Participant-approval scenarios 4–10 and 12, plus scenario 11
  branches beyond the selected guest-authored, BCC-only and provider-delivered
  unauthorized controls, were not run. Sendmux live rows remained blocked on a
  scoped mailbox credential and an available two-recipient hourly allowance.

AgentMail's explicit Reply endpoint stripped CC and ancestry on one preliminary
delivery, so it was excluded; the prelude used a real provider send carrying
signed correlation headers, never injected provider or local state. The account
also had only two disposable AgentMail slots, so the permitted mixed-provider
identity layout was used. No product defect was exposed. Retained evidence has
only sanitized counts, recipient-set facts, state transitions and revisions—no
credential values, addresses, guest bodies or body-derived hashes.

- **PASS — teardown.** The exact Compose project was stopped, its isolated
  secret and image were removed, all four run-created inboxes and run-created
  sender policies were deleted and verified absent, and the validated scratch
  runtime root was removed. Pre-existing provider resources and the normal
  service were untouched.

## Participant-approval continuation live evaluation, 2026-09-13

This continuation used exact candidate `b88d58e942722fac1c372501c45a9aee535296f6`
and predecessor `3ca9fd16302fc3afca18093d6e6304d6fe63f7a3` source snapshots. The
credential-free network-isolated Go/race/vet gate and the explicit
participant-named selection passed. OpenRouter preflight found one exact
`z-ai/glm-5.3-flash` catalog match and 26 eligible endpoints with the required
reasoning parameters; the isolated startup sync used high reasoning. The
disposable AgentMail receiver/owner and OpenMail guest stack was provisioned,
and the recipient-delivery prelude plus scenarios 1--3 were recreated only as
prerequisite state; their results are not re-recorded here.

- **PASS — scenario 4, independent decisions.** Two guest messages produced two
  distinct held requests and private prompts. An old `Yes` replay, a
  guest-authored `Yes`, and a wrong-thread owner `Yes` released neither; an
  exact approval resolved only its target and advanced the turn sequence once.
- **PASS — scenario 5, `No` and `Other`.** `No` resolved its request with zero
  turn change. `Other` entered replacement state; quoted-only replacement text
  left it there with zero turn change, while one newly authored owner
  replacement resolved it and advanced the turn sequence exactly once.
- **PASS — scenario 6, trust controls do not bypass approval.** Owner and guest
  trust commands created no agent turn or trust row in the per-message guest
  flow. The next guest message still produced its own prompt, and its exact
  approval produced one turn with the lower-authority marker present.
- **PASS — scenario 7, restart durability.** A forced candidate-container
  restart retained one pending request and three earlier `resolved_yes`
  requests. After a fresh poll, prompt rows, processed rows and turn sequence
  all had zero delta; no prompt or execution was duplicated.
- **BLOCKED — scenario 8, private owner continuation.** AgentMail rejected the
  owner send before delivery with HTTP 429 `rate_limit_exceeded` and
  `Retry-After: 48340`; the exact prerequisite is reset availability for the
  disposable owner's send allowance.
- **BLOCKED — scenario 9, removal while work is held.** The authenticated owner
  removal command could not be sent under the same AgentMail allowance window.
  The exact prerequisite is the same allowance reset; no substitute identity
  or policy widening was used.
- **BLOCKED — scenario 10, reinvitation and generation isolation.** This row
  depends on scenario 9's authenticated removal and further owner sends, all
  unavailable before the recorded allowance reset.
- **PASS — scenario 11, forged owner invitation provider rejection.** OpenMail
  rejected the attempted forged invitation with HTTP 403; it was not delivered
  and produced no authorization mutation or invocation.
- **PASS — scenario 11, forged owner instruction local rejection.** OpenMail
  accepted the send but preserved its authenticated guest identity. The
  provider-delivered message remained unread at the receiver, with no grant,
  participant request or turn change.
- **PASS — scenario 11, forged approval local rejection.** A provider-delivered
  guest-authored exact `Yes` resolved neither of the two target requests and
  caused no invocation; it was held as separate guest control-plane work.
- **PASS — scenario 11, forged removal local rejection.** A provider-delivered
  guest-authored command carrying a valid generation-1 removal token left one
  active generation-1 grant, participant requests and turn sequence unchanged.
- **BLOCKED — scenario 11, forged guest request.** OpenMail normalized the
  claimed author to its authenticated sender and delivered ordinary signed
  guest work. Local state held it with zero invocation, but AgentMail's same
  429 window prevented the private prompt, so this did not exercise forged
  sender evidence end to end.
- **BLOCKED — scenario 11, raw unsigned variants.** The managed disposable
  sender APIs do not expose controlled unsigned RFC822 injection. The exact
  prerequisite for unsigned owner invitations, guest requests, owner
  instructions, approvals and removals is a scoped disposable raw-send path
  that preserves provider-delivery attribution; none was available this run.
- **BLOCKED — scenario 12, lower-authority override attempts.** The exact-model
  attempts require private owner approvals, which could not be sent before the
  same AgentMail allowance reset. Deterministic routing remained covered by the
  passing container gate, but it does not substitute for this live row.
- **BLOCKED — live `TestParticipantAuthorityLive` probe (not run).** The exact
  prerequisite remains a dedicated egress-restricted network allowing only
  `openrouter.ai:443`, with DNS and a denied non-OpenRouter probe recorded, plus
  a spend-limited key. An unrestricted default bridge is not acceptable.
- **BLOCKED — Sendmux live rows.** The exact prerequisites remain a scoped
  Sendmux mailbox credential and availability of the shared account's
  two-recipient hourly allowance.

The decisive anomaly was the account-wide AgentMail send window: the first 429
occurred on the scenario-8 owner send, and a later held guest request could not
receive its private receiver-sent prompt. No product defect was established.
Retained evidence contains only counts, states, recipient-shape verdicts,
provider status/identifiers and configuration facts, with no addresses,
message bodies, credential values or body-derived hashes.

- **PASS — teardown.** The Compose project, isolated secret, containers, image
  and validated runtime root were removed. Fourteen exact run-created policy
  entries and the two AgentMail plus one OpenMail disposable inboxes were
  deleted and verified absent; pre-existing provider ID sets and normal state
  were unchanged.

## Sendmux submission and filtering follow-up, 2026-09-13

Sendmux now uses local DKIM and owner/guest authorization without managing SMTP
sender filters. Native JMAP submissions preserve reply ancestry and retain
recoverable Sent records. Private approval references compensate for the relay's
rewritten Message-ID, while preserving authenticated owner, inbox, thread and
pending-request checks. Deterministic tests cover forged preview references,
concurrent and lost-response recovery with stale reads across restart, changed recipient envelopes and delivery
status that does not equate SMTP acceptance with arrival.

A disposable production Compose run with separate AgentMail owner and guest
inboxes verified automatic owner-CC participation and the shared owner answer
arriving in both destination inboxes. A subsequent authenticated guest reply was
held for its own private approval, with no guest instruction entering local
application or agent storage. The approval submission was accepted, but remained
pending in the relay with zero attempts after the account's two-recipient hourly
allowance was consumed. The complete Sendmux guest approval conversation is
therefore **inconclusive**, not passed. Its shared sending account cannot have
its hourly quota changed through the provider update API; the shared limit-request
endpoint concerns reviewed daily increases.

These observations do not replace the full participant LSE. The earlier sections
below describe their named revisions and may have since-superseded provider
limitations; use the operational contract for current capabilities.

## Implemented participation workflow, 2026-09-12

The current implementation enables authenticated AgentMail owner To/CC
invitations, independent private approvals, shared guest answers, private owner
continuations, automatic provider synchronization and scoped removal commands.
OpenMail and Sendmux fail closed for inbound work because supported authentication
evidence is unavailable. Exact-domain DKIM relies on the domain operator to
enforce mailbox ownership; see the [trusted boundary](README.md#trusted-boundary-and-implementation-evidence).

Gobra verified all four production Boolean bodies with zero errors. TLC exhausted:

| Configuration | Distinct states | Generated states | Maximum depth |
| --- | ---: | ---: | ---: |
| Guest: one scope | 484 | 3,684 | 9 |
| Guest: shared provider entry | 24,004 | 328,576 | 15 |
| Guest: sparse scope matrix | 3,748,096 | 53,248,000 | 17 |
| Participation: repeated guest requests | 13,576 | 95,479 | 16 |
| Participation: two scopes | 2,119,936 | 27,503,841 | 23 |

All four reconciliation mutations and seventeen participation mutations produced
the required Safety counterexample. All three successful-workflow reachability
checks produced their expected counterexamples. These are bounded safety and
reachability results, not complete Go/SQLite refinement or provider proofs.

The deterministic tests cover automatic grants and every permission direction,
per-request approval without admission/trust shortcuts, owner privacy with a
pending guest decision, removal and stale approvals, explicit reinvitation,
request-content substitution and historical recipient exclusion. Authentication
tests use real ephemeral signatures and the AgentMail SDK over loopback TLS;
forged author fields, altered bodies, unsigned routing/correlation fields and
fabricated verdicts are rejected. Failing-first regressions also reject added
unsigned MIME disposition/length headers; every present Content-* field must be
signed, including extensions. Two existing received messages were checked
read-only with the candidate verifier and passed; their private content and
identifiers are excluded from version control. No mail was sent by that check.

A fresh production-container guest exchange and backend authority evaluation
have not been run for this implementation. The read-only signature samples and
offline tests do not substitute for those live evaluations.

## Historical validation before automatic participation

The remaining sections record earlier revisions and their limitations; their
automatic-invitation/admission statements do not describe the implementation above.

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

## Proposed participation protocol, 2026-09-12

`Participation.tla` adds design verification for authenticated owner To/CC
invitations, independent approval of every guest message, private owner
continuations, exact revocation tokens, and explicit reinvitation after
revocation. This change does not implement that protocol in the daemon or
establish real provider sender authentication. The existing production Gobra
contracts and admission-flow model remain separate and unchanged.

The pinned TLC runner exhausted these additional configurations with `Safety`,
`TypeOK` and `AnswerPrivacy` intact:

| Configuration | Distinct states | Generated states | Maximum depth |
| --- | ---: | ---: | ---: |
| One scope, two guest messages, one owner instruction | 5,820 | 26,499 | 16 |
| Two scopes, one guest message and owner instruction each | 518,400 | 3,970,081 | 23 |

All 17 deliberately weakened workflow configurations produced the required
`Safety` counterexample and invariant-failure exit status. Three separate
reachability checks found repeated shared guest answers, private owner answers
while the guest remained authorized, and an approved answer after explicit
reinvitation. They exclude a vacuous result from simply disabling participation.
The runner retains model sources, exact workflow configurations and logs.

Gobra again verified the three current production Boolean policy bodies with
zero errors. The host `nix flake check`, installation documentation contract and
credential-free Installation Procedure self-test passed. No live email,
credentials or running service state were used by these checks.

The [verification guide](README.md#proposed-participation-workflow) records the
finite bounds, trusted authentication/visibility facts, separate provider model,
and implementation obligations. The results do not prove transport evidence,
parser behavior, database refinement, delivery or unbounded progress.
