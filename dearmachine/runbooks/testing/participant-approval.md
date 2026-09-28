# Authenticated guest participation and per-message approval LSE

Use the containerized production path in
[`temporary-instance.md`](./temporary-instance.md). This protocol requires a
dedicated receiver plus separate temporary controlling-participant and guest
senders. It is opt-in: do not provision inboxes, change policy entries, send
mail, or call a model without the credentials and explicit authorizations in
that reference.

Use an AgentMail or Sendmux receiver. The sender domain must provide exact-domain DKIM
covering all present author/routing/correlation and MIME headers; the domain's
operator is trusted to enforce mailbox ownership. Check candidate authentication
before interpreting a provider delivery as accepted work. Sendmux verifies original MIME through read-only IMAP. OpenMail can be tested
only with its supported unencoded single-part plain-text evidence; HTML,
multipart and attachments remain rejected. Inspect the installed authentication
status before selecting the receiver.

Reserve capacity for three disposable identities before starting. They may use
different providers. For AgentMail, verify actual delivery with managed exact-address
policies enabled. Sendmux intentionally leaves SMTP filters unmanaged because
envelope senders differ from authenticated From addresses; verify its default
filter mode and local DKIM/grant enforcement. Do not widen a provider policy as
a diagnostic workaround and then report automatic synchronization as passing.

Confirm every outbound message in its destination inbox. An API queue receipt or
a Sent-folder copy does not prove delivery; inspect delivery logs and sending
limits when a reply is missing. Provider-added branding must not invalidate an
otherwise exact owner decision, and other extra text must remain invalid.

The owner establishes participation by visibly including Dear Machine and the
guest in To/CC. Verify the automatic exact pair/inbox/guest/thread grant and
service-created provider permissions where applicable; do not manually grant or pre-allow
the guest to make this row pass. Guests must Reply All visibly to Dear Machine
and the owner. BCC, provider delivery or thread knowledge cannot authorize work.

Every guest request requires an independent private `Yes`, `No` or `Other`
decision. There is no separate admission exchange or retained-trust bypass.
`Yes` authorizes only the held request, `No` rejects it, and `Other` requests a
newly authored owner replacement. Guest work remains lower authority. Shared
answers use the original request's visible recipients, never the approval's.
Owner continuations omitting the guest remain private without canceling pending
approvals or participation. Private instruction prompts and owner-only answers
include `REMOVE GUEST <code>`. Outbound previews retain the frozen shared draft
and add the pending header; do not require a removal footer in that draft.

Every paired shared reply now requires a separate outbound decision once its
payload is prepared. Expect a private To-owner preview with empty CC/BCC, exact frozen
plain text/HTML/files and the pending header. Only newly authored exact `yes`
or `no` (case insensitive, surrounding whitespace ignored) from the authenticated
owner, bound to an issued outbound preview and its revision, can resolve it.
Quoted history is not the decision. Outbound `Other` leaves the draft pending;
instruction Yes/No/Other remains a separate workflow. Final shared submission
contains the approved draft without the pending header.

This protocol describes required validation. The
[validation record](../../../verification/guest/VALIDATION.md) documents passing
scoped outbound live checks; it does not claim a completed run of this entire
protocol. The unchanged formal outbound contract does not prove Go refinement
or composition.

## Deterministic container gate

Before live mail, build the image from the exact revision under test and run
the participant and adapter tests in a credential-free container. Use only a
source snapshot, scratch caches, and `--network none` after dependencies are
available; never mount a checkout, normal home, production state, or session
store. The required tests are:

```text
TestGuest*
TestParticipant*
TestOutboundApproval*
TestSender*
TestAgentMailAuthentication*
TestUnsupportedProvider*
TestMailboxReplyMapsPrivateRecipientWithoutReplyAll
TestOpenMailReplyPreservesThreadWithPrivateRecipient
TestSendmuxReplyPreservesThreadWithPrivateRecipient
```

They are the privacy and persistence oracle. A model verdict cannot replace
them.

When the revision under test has intentional uncommitted changes, snapshot the
working tree rather than `HEAD`. From the repository root, copy only tracked
and non-ignored untracked files so the container sees the exact candidate and
does not receive `.git`, ignored evidence, caches, or credentials:

```bash
participant_source=$(mktemp -d "${TMPDIR:-/tmp}/participant-source.XXXXXX")
git ls-files -co --exclude-standard -z |
  tar --null -T - -cf - |
  tar -xf - -C "$participant_source"
```

### Immutable request recovery contract

Each provider message ID is a separate frozen request. Store only identifiers,
canonical addresses, request/decision states, timestamps, grant generation and a
normalized-content fingerprint before approval. Do not store guest bodies in
SQLite, logs, session files or agent prompts before approval. The private owner
approval email deliberately includes a quoted preview so the owner can review
what they approve. A fingerprint is not encryption; do not publish it as evidence.

After `Yes`, the provider remains the body source. Missing or changed content,
failed authentication and revoked/stale grants fail closed. `No` needs no body
fetch. `Other` executes only a correlated newly authored owner replacement.
Pre-upgrade guest requests without fingerprints require fresh messages and
approvals. Restart must retain decisions, fingerprints, removal tokens and
recipient generation snapshots without duplicating execution or prompts.

## Exact-model preflight and authority probe

The build-tagged `TestParticipantAuthorityLive` test is a paid, opt-in model
probe. OpenRouter remains the default. Its preflight requires the catalog to
contain exactly `z-ai/glm-5.3-flash`, advertise the `reasoning` parameter, and
expose `high` as a supported reasoning effort (or explicitly report that all
effort values are accepted). It also requires at least one currently available
provider endpoint for that exact model which advertises both `reasoning` and
`reasoning_effort`.
Every completion sends `reasoning.effort = "high"`, requires
parameter support, disables provider fallbacks, and rejects a response naming
any other model. Missing capability, unavailable routing, or a different model
is a failure; aliases, free/batch variants, and substitutions are forbidden.

Set `DEARMACHINE_PARTICIPANT_LIVE_PROVIDER=deepseek` to select the official
DeepSeek API instead. That path uses the documented OpenAI-compatible endpoint
`https://api.deepseek.com/chat/completions`, requests the exact model identifier
`deepseek-flash`, and sends both `thinking.type = "enabled"` and
`reasoning_effort = "high"`. Before any completion, it requires that exact ID
in `GET https://api.deepseek.com/models`; a compatibility alias is not
accepted. The current official documentation must also still list `high` as
supported before an operator starts the run. The response must name the
requested model exactly and contain non-empty `reasoning_content`, proving that
the requested thinking path was exercised without retaining that private
reasoning in test output.

Put only a spend-limited OpenRouter key in a mode-`0600` file. Export a clean
source snapshot and mount the snapshot and key read-only into a disposable
container. The test reads the key only into the process and never prints raw
provider output.

```bash
: "${PARTICIPANT_LIVE_KEY_FILE:?set an absolute path to a spend-limited key file}"
: "${PARTICIPANT_OPENROUTER_NETWORK:?set a network restricted to openrouter.ai:443}"
test "$(stat -c %a "$PARTICIPANT_LIVE_KEY_FILE")" = 600
participant_source=$(mktemp -d "${TMPDIR:-/tmp}/participant-source.XXXXXX")
trap 'rm -rf -- "$participant_source"' EXIT
git ls-files -co --exclude-standard -z |
  tar --null -T - -cf - |
  tar -xf - -C "$participant_source"
docker run --rm --network "$PARTICIPANT_OPENROUTER_NETWORK" \
  --user "$(id -u):$(id -g)" \
  --cap-drop ALL --security-opt no-new-privileges \
  --mount "type=bind,src=$participant_source,dst=/src,readonly" \
  --mount "type=bind,src=$PARTICIPANT_LIVE_KEY_FILE,dst=/run/secrets/openrouter-key,readonly" \
  -e HOME=/tmp/participant-home -e GOCACHE=/tmp/go-cache -e GOPATH=/tmp/go \
  -e GOFLAGS=-buildvcs=false \
  -e DEARMACHINE_PARTICIPANT_LIVE_KEY_FILE=/run/secrets/openrouter-key \
  -w /src/dearmachine golang:1.24-bookworm bash -c 'set -eu
    umask 022
    mkdir -p "$HOME"
    go test ./internal/client -run "^TestParticipant|^TestGuest|^TestOutboundApproval|^TestMailboxReplyMapsPrivateRecipientWithoutReplyAll$|^TestOpenMailReplyPreservesThreadWithPrivateRecipient$|^TestSendmuxReplyPreservesThreadWithPrivateRecipient$" -count=1
    go test -tags participant_live ./internal/client -run "^TestParticipantAuthorityLive$" -count=1 -v -timeout=60m'
```

The named network must enforce outbound allow-listing for only
`openrouter.ai:443`; an unrestricted default bridge does not satisfy this
protocol. DNS and proxy/firewall configuration is environment-specific, so
record the rule and a denied non-OpenRouter probe in the private evidence
before starting the paid test.

For the official DeepSeek path, mount a mode-`0600` DeepSeek key at
`/run/secrets/deepseek-key`, set the live key-file variable to that path, add
`-e DEARMACHINE_PARTICIPANT_LIVE_PROVIDER=deepseek`, and use an isolated
network that allows only `api.deepseek.com:443`. Record a denied non-DeepSeek
probe before mounting the credential. Do not route the exact-model check
through an OpenAI-compatible aggregator: DeepSeek compatibility aliases can be
accepted while serving a different model generation.

Each case runs three fixed attempts with a 4,096-token completion budget,
including reasoning, and a 30-second pause between attempts. The report
distinguishes an incorrect verdict from empty,
truncated, refused, incomplete or malformed output, and records only a bounded
finish classification and token usage. No raw response or private reasoning is
logged. All attempts must pass; do not selectively retry failures until success.
Run the credential-free classification fixtures with
`go test -tags participant_live ./internal/client -run '^TestParticipantAuthorityProbe'`.

The authority probe separately covers explicit override, implicit policy
weakening, false delegation, trusted-participant override, and private
clarification. Report it as passed, failed, or not run independently of the
deterministic and email-container results.

## Containerized email scenarios

Before this series, run the cross-version multi-recipient prelude in
[`recipient-delivery.md`](./recipient-delivery.md). Its final reply-all step is
the delivery prerequisite for scenarios 1 through 3 below; do not substitute a
synthetic message inserted directly into provider or local state.

Start the isolated production Compose stack only after its effective Machtiani
configuration proves that its OpenRouter model ID is exactly
`z-ai/glm-5.3-flash` and its provider parameters contain
`reasoning = { effort = "high" }`. Stop if either check is inconclusive. Use
synthetic marker bodies and keep them only in the Git-excluded evidence area.

Run these scenarios in order:

1. Send an authenticated owner instruction with Dear Machine in To and the guest
   in CC. Verify the exact automatic grant, every provider permission direction,
   and one private outbound preview. Verify zero guest copies, then send a new
   authenticated owner `yes` referencing that preview and verify the answer
   reaches owner and guest. No separate admission prompt may appear.
2. Send a guest Reply All. Before approval prove zero agent invocations and no
   guest marker body in application SQLite/WAL, logs or session files. Verify one
   private approval email To owner, empty CC/BCC, quoted request preview and
   copyable removal command. The guest must not receive this prompt.
3. Reply exact `Yes` to the instruction prompt. Require one lower-authority
   execution and a private outbound preview, with no guest copy. A Yes replay
   to the instruction prompt must not release the answer. Send a new owner
   `yes` referencing the outbound preview receipt; require one answer To owner,
   CC guest. Duplicate delivery/approval must not repeat execution or submission.
4. Send another guest request before deciding a third. Each must have a distinct
   approval; approving one cannot release the other. Replaying an earlier Yes,
   guest-authored decisions and wrong-thread decisions cannot authorize either.
5. Resolve one with `No`, proving no execution. Resolve another with `Other`,
   proving only a correlated newly authored owner replacement executes; quoted
   text and empty replacements cannot substitute for a new owner instruction.
   Keep replacement execution active across at least two inbox polls. Require
   one pending row and no processed row until completion, then exactly one
   owner-only answer and one sequence advance. Restart after saving the result
   and after provider delivery but before local completion; neither recovery may
   rerun the saved instruction or duplicate the delivered answer. Include an
   isolated legacy fixture with the old empty control record and verify recovery
   replaces it with the existing answer receipt. Never inject this fixture into
   the normal service database.
6. Send owner and guest trust commands. Prove neither enables an approval
   bypass or creates an agent turn for the control command. Every subsequent
   guest message still requires its own decision and retains lower authority.
7. Restart with pending and approved requests. Verify no duplicate prompts or
   execution and that content refetch/authentication and grant checks still apply.
8. While a guest decision is pending, send an ordinary owner instruction omitting
   the guest. Verify an owner-only answer, unchanged participation and a still
   pending guest decision. A later exact Yes may release that guest request
   for execution; sharing its result still requires outbound approval.
9. Reply with the private removal command while guest work is held/queued.
   Verify local revocation, no subsequent guest execution starts and eventual
   removal of owned, unneeded provider permissions. Restart and submit stale
   approval/removal commands; no old work may execute.
10. Send ordinary owner reply-all after removal. It must not restore the grant.
    Explicitly run guest allow using a qualifying owner invitation, then send
    and approve a new guest request. Old work remains invalid; an old removal
    code cannot revoke the new generation. Reinvitation must not copy the guest
    on an older unsubmitted answer.
11. On disposable resources, attempt unsigned/forged owner invitations, guest
    requests, owner instructions, approvals and removals. Distinguish provider
    rejection from local rejection; neither may execute or mutate authorization.
12. Exercise lower-authority explicit/implicit override attempts. Deterministic
    routing must never label guest content as owner work; report the separate
    exact-model authority probe independently. Already-started effects and
    submitted mail are outside the revocation guarantee.

## Outbound recovery and recipient-change extension

On disposable state, exercise both owner-origin and guest-origin shared answers,
including a shared error/status reply. Each reply requires its own preview and
decision. Verify these independently of model-behavior verdicts:

1. Restart after execution completion while outbound approval is pending. The
   outbox must retain exact text/HTML/file bytes after staging cleanup and must
   not rerun execution. A local `outbound-pending:` receipt is not a submitted or
   delivered answer. Reject with `no`; a later Yes cannot revive that proposal.
2. Send extra authored text, `Other`, unauthenticated/wrong-scope decisions,
   unknown references, and Yes to another request's preview. None releases the
   draft. A token allocated before uncertain issuance is insufficient; require
   its exact private receipt to reconcile before accepting the decision. A
   header-only yes/no with an unresolved reference during uncertain preview
   issuance must stay unprocessed until receipt recovery, without agent work.
3. Remove one of two guests while pending or approved. Require a new revision,
   reduced private preview and fresh Yes. Old Yes cannot release it. Remove all
   guests and verify the owner-only exemption. Revoke/reinvite and verify the
   new generation cannot receive the older request's answer.
4. Simulate lost preview and final-send responses with delayed receipt visibility
   in an isolated transport fixture. Record fault injection separately from live
   provider observations. Recovery may only reconcile the original send, never
   blindly resend it. Missing, ambiguous and mismatched receipts remain held.
   Match text conservatively with CRLF/LF normalization only, `RawHTML` exactly, and
   files by metadata and bytes. Do not relax comparisons for provider rewrites.
5. Inspect private pair DB `outbound_approvals` metadata: `State`, `HoldReason`,
   revision and presence of issued-preview/decision/submission evidence. Expected
   states are `prepared`, `preview_sending`, `pending`, `approved`, `sending`,
   `sent`, `rejected`, `superseded`. Uncertain sends retain a receipt reconciliation
   hold reason. Do not dump payloads, file bytes or tokens into evidence or edit
   state to force progress. Record provider submission and destination arrival
   separately; `sent` alone does not prove delivery.

Capture only sanitized recipient sets, provider thread/message identifiers,
state transitions, invocation counts, model/config preflight, and presence or
absence verdicts. Do not retain guest bodies or body-derived hashes. Teardown
only the recorded Compose project, scratch root, temporary inboxes, credentials,
and exact run-created policy entries.

### Recovery and feedback checks

- Inject a failure before a mutating send in an isolated transport fixture.
  Require receipt lookup, the same frozen request/key on retry, and a fresh
  guest grant check. Exercise preview and approved submission independently.
- Restart during backoff and after the retry budget expires. Require at most
  three attempts per phase/revision within the original 15-minute budget.
  Unknown acceptance must never be treated as local non-submission.
- Lose a send response and delay or corrupt its receipt. Require one private
  hold notice attempt per phase and revision, a visible state/reason in native
  text and JSON status, and no automatic resend. Losing the notice response must not loop notices.
- Fail recovery while a record is pending owner approval. Require status/log
  evidence without a hold notice or notice reservation. Then approve it and
  hold its submission: require one private submission notice.
- Hold a preview, observe its private notice, then reconcile the preview and
  approve the same revision. Hold the submission and require a separate private
  notice explaining delivery uncertainty. Replay and restart must not repeat
  either phase notice.
- Keep an independent agent task running, then approve a completed reply in
  another shared thread. Require submission before the independent task finishes,
  with the exact approved envelope and content. Repeat while maintenance is
  active and verify replay does not submit twice. Use a disposable fixture for
  blocked lanes; do not interrupt production work to create this condition.
- Keep a broken outbox record while polling an independent request and
  recovering another record. Confirm that both continue.
- Send malformed, stale, ambiguous and unmatched owner controls. Require one
  private guidance notice per consumed message, no approval or agent execution,
  and no notice duplicates after replay/restart. Uncertain-preview decisions
  remain unprocessed until receipt reconciliation resolves issuance.
- Inspect guest inboxes for absence of all feedback/hold notices and private
  control content. Label injected state/transport failures separately from
  actual provider faults in the validation record.
