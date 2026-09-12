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

## Deterministic container gate

Snapshot the exact candidate worktree as described in
[`participant-approval.md`](./participant-approval.md), build it in a clean
container, and run:

```console
go test ./internal/client -run 'TestMailboxPollRecoversUnreadMessageOmittedByLabelFilter|TestInboxRouter.*Delivery|Test(OpenMail|Sendmux)NormalizePreservesRecipientRolesAndDelivery|TestPolledMessageSeam' -count=1 -v
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
   reply, and one successful acknowledgement. Restart the candidate and prove
   there is no duplicate claim, turn, or reply.
7. Repeat with the controller in `To` and the receiver in `CC`. If the sending
   provider supports BCC without exposing the recipient header, repeat with the
   receiver in `BCC`. Authentication requires visible Dear Machine recipients,
   so this candidate must reject that BCC-only inbound work.
8. Verify the automatic grant from step 5 and service-created receive/reply/send
   entries without explicit guest allow or manual guest rules. Have the guest
   Reply All. Require one private approval prompt To owner with empty CC/BCC and
   a quoted guest preview. One exact Yes must release exactly that instruction
   for lower-authority execution and an answer To owner, CC guest. There is no
   separate admission prompt.

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

AgentMail is the current supported inbound authentication path. OpenMail and
Sendmux receivers must report unsupported evidence and reject inbound work.
Their normalization, scoped permission changes and outbound envelope encoding
remain covered by adapter tests. Record any direct provider diagnostic separately;
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

1. Guest Reply All, one private instruction approval, then an answer
   with the owner in To and the guest in CC.
2. Owner request with an active guest in To or CC, then an answer to both.
3. Owner continuation omitting the guest, then an owner-only answer. Verify the
   guest inbox received no copy using a distinct synthetic result marker.
4. BCC-only, revoked and ungranted recipients never enter answer CC.
5. Lost send response and client restart retain the original answer envelope;
   the private approval prompt cannot be mistaken for the final answer.
6. Verify AgentMail receive/reply/send and OpenMail inbound/outbound entries
   individually. Revoke one of two grants, then the last; inspect preservation of
   permanent/pre-existing rules and removal of every owned unneeded direction.

Use the recipient transport diagnostic separately from model behavior probes.
A synthetic answer verifies addressing and delivery, not model execution.
