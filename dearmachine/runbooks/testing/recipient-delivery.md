# Recipient delivery and polling recovery LSE

Use the containerized production path in
[`temporary-instance.md`](./temporary-instance.md). This protocol verifies the
transport-neutral delivery seam with real providers. It requires one disposable
DearMachine receiver, a controlling sender, and a second recipient. Never point
it at the normal service or its inbox.

## Contract under test

Adapters preserve `To`, `CC`, and `BCC` separately. They attach the exact
provider inbox used for retrieval, the provider inbox address when available,
the delivery role, and an explicit read state. The shared polling seam performs
final unread selection and message-ID deduplication. The inbox router authorizes
paired delivery using authenticated sender/headers and trusted provider inbox
identity. Guest delivery additionally
requires an active exact grant and visible Reply All recipients. To/CC visibility
is distinct from provider inbox attribution; BCC never qualifies as an invitation.

AgentMail needs an additional recovery assertion. Its `labels=unread` listing
may omit a received message with multiple recipients even though an unfiltered
listing returns the message with the `unread` label. DearMachine unions the
filtered listing with an incremental unfiltered listing and must process the
message exactly once.

All paired shared replies pass through the implemented durable outbound approval
gate. First verify the owner-only preview's exact text/HTML/files plus pending
header and zero guest copies. Then send a new authenticated owner exact `yes`
referencing the issued preview; only the approved draft without the header may
be submitted to guests. Instruction Yes/No/Other does not authorize outbound
sending. Outbound accepts only exact newly authored yes/no after case/whitespace
normalization and quoted-history exclusion. Owner-only replies are exempt.

The [validation record](../../../verification/guest/VALIDATION.md) documents
passing scoped outbound live checks and their limits. The steps below are a
protocol, not a record that every delivery check has passed.

## Deterministic container gate

Snapshot the exact candidate worktree as described in
[`participant-approval.md`](./participant-approval.md), build it in a clean
container, and run:

```console
go test ./internal/client -run 'TestOutboundApproval|TestMailboxPollRecoversUnreadMessageOmittedByLabelFilter|TestInboxRouter.*Delivery|Test(OpenMail|Sendmux)NormalizePreservesRecipientRolesAndDelivery|TestPolledMessageSeam' -count=1 -v
go test ./...
```

The gate must prove that an omitted AgentMail message is recovered, recipient
roles remain distinct for all adapters, an explicit wrong provider inbox fails
closed, and a duplicate candidate is emitted once.

## Live cross-version scenario

1. Record the candidate revision and its immediate pre-participant predecessor.
   Build separate OCI images from both exact source snapshots.
2. Provision a disposable AgentMail receiver and two disposable authenticated
   senders whose signatures meet the candidate header-coverage policy. Register the first sender as the controller. Add only exact,
   inbox-scoped provider policy rules required by this run.
3. Start the predecessor image through the isolated production Compose path.
   Send one ordinary controller message and wait for one reply, thereby
   establishing the provider thread and durable conversation state.
4. Stop the predecessor cleanly. Start the candidate image with the same
   disposable provider inbox and persistent test state. Record the new image
   revision and container ID.
5. From the controller, continue the established thread with the receiver in
   `To` and the guest address in `CC`. Verify directly through
   AgentMail that the message is present, received, and unread. Record whether
   `labels=unread` omits it; omission is expected evidence, not a reason to
   alter provider state.
6. Require one durable claim, one completed agent turn, one sender-visible
   private outbound preview, and one successful acknowledgement. Confirm no
   guest copy before a new owner Yes to that preview. Require one approved
   shared answer and check each destination inbox. Restart the candidate and
   prove there is no duplicate claim, turn, preview, or shared answer.
7. Repeat with the controller in `To` and the receiver in `CC`. If the sending
   provider supports BCC without exposing the recipient header, repeat with the
   receiver in `BCC`. Authentication requires visible Dear Machine recipients,
   so this candidate must reject that BCC-only inbound work.
8. Verify the automatic grant from step 5 and service-created receive/reply/send
   entries without explicit guest allow or manual guest rules. Have the guest
   Reply All. Require one private approval prompt To owner with empty CC/BCC and
   a quoted guest preview. One exact Yes must release exactly that instruction
   for lower-authority execution and a private outbound preview. A separate
   authenticated owner Yes referencing that issued preview must release the
   answer To owner, CC guest. There is no separate admission prompt.

## Persistent guest lifecycle extension

Run the exact source candidate through the production OCI/Compose path. Record
provider receipt separately from local acceptance. An automatic-grant success
requires the documented sender-authentication contract; never treat explicit allow as
passing that row. Do not alter any existing production inbox or normal service.

1. While guest receive permission is open, send a new guest thread and a guest
   message into another known thread without a grant. Prove provider delivery
   and zero local prompts/executions for both.
2. Attempt guest-authored invitations and BCC-only invitations. Neither may
   create a grant. Assert only BCC metadata genuinely observable to the receiver.
3. Restart and verify durable grants and no duplicate prompts/executions. Run
   explicit allow against a historical visible invitation.
4. Grant a second thread, revoke the first, and verify receive permission stays
   open while the first thread is locally rejected. Test stale approvals.
5. Revoke the final grant. Verify removal only of the exact owned entry. Replay
   the old invitation, verify no automatic resurrection, then explicitly
   reauthorize. Previously held work must remain invalid.
6. Verify permanent-pair and pre-existing-entry protection in disposable state.
   Separate injected provider failures/recovery from actual provider observations.

## Adapter matrix

AgentMail and Sendmux support local exact-domain DKIM verification of original
messages. OpenMail supports the restricted unencoded single-part plain-text
subset documented in [guest authorization](../../../docs/guest-authorization.md);
unsupported or invalid evidence must reject inbound work. Normalization, scoped
permission changes and outbound envelope encoding remain covered by adapter tests. Record any direct provider diagnostic separately;
it cannot pass an authenticated application workflow row. Never weaken sender
checks to make a transport row pass. Preserve backend requirements from
`participant-approval.md` and report model behavior probes separately.

## Teardown

Stop the exact Compose project, remove its isolated secret, and prove no test
container remains. Delete only the inboxes and policy rules inventoried for the
run. Verify every deleted provider ID is absent or explicitly soft-deleted, and
confirm the normal service, protected inbox, registry, and databases are
unchanged.


## Answer recipients after an authenticated automatic grant

For each supported receiver, automatically grant a signed visible invitation and exercise:

1. Guest Reply All, one private instruction approval, then a private outbound
   preview. Only a separate Yes to that preview releases the answer with owner
   in To and guest in CC.
2. Owner request with an active guest in To or CC, then a private outbound
   preview and separate Yes before an answer to both.
3. Owner continuation omitting the guest, then an owner-only answer. Verify the
   guest inbox received no copy using a distinct synthetic result marker.
4. BCC-only, revoked and ineligible recipients never enter answer CC. An
   authenticated owner's visible To/CC can create a new grant. Removing some
   recipients requires a new preview/Yes if guests remain; removing all uses
   the private exemption. Reinvitation cannot add a new generation to old work.
5. Lost send response and client restart retain the frozen payload/envelope;
   recovery only reconciles a unique exact receipt, never blindly resends.
   Missing/ambiguous/mismatched receipts remain held. Plain text permits only
   CRLF/LF normalization and HTML must match adapter-retained `RawHTML` exactly;
   file metadata/bytes must match.
   Neither an instruction prompt nor a pending preview is a final-answer receipt.
   Inspect pair DB state/hold reason privately. Completed execution and local
   `outbound-pending:` receipts establish neither submission nor delivery.
6. Verify AgentMail receive/reply/send and OpenMail inbound/outbound entries
   individually. Revoke one of two grants, then the last; inspect preservation of
   permanent/pre-existing rules and removal of every owned unneeded direction.

Use the recipient transport diagnostic separately from model behavior probes.
A synthetic answer verifies addressing and delivery, not model execution.
