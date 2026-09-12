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
delivery by the trusted provider inbox identity, not by merging or searching
untrusted recipient headers.

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
2. Provision a disposable AgentMail receiver and two disposable OpenMail
   senders. Register the first sender as the controller. Add only exact,
   inbox-scoped provider policy rules required by this run.
3. Start the predecessor image through the isolated production Compose path.
   Send one ordinary controller message and wait for one reply, thereby
   establishing the provider thread and durable conversation state.
4. Stop the predecessor cleanly. Start the candidate image with the same
   disposable provider inbox and persistent test state. Record the new image
   revision and container ID.
5. From the controller, continue the established thread with the receiver in
   `To` and the second OpenMail address in `CC`. Verify directly through
   AgentMail that the message is present, received, and unread. Record whether
   `labels=unread` omits it; omission is expected evidence, not a reason to
   alter provider state.
6. Require one durable claim, one completed agent turn, one sender-visible
   reply, and one successful acknowledgement. Restart the candidate and prove
   there is no duplicate claim, turn, or reply.
7. Repeat with the controller in `To` and the receiver in `CC`. If the sending
   provider supports BCC without exposing the recipient header, repeat with the
   receiver in `BCC`. Routing must still use the recorded provider inbox.
8. Have the second OpenMail address reply-all in the established thread. The
   participant admission request must go only to the controller, with empty
   CC/BCC and no quoted participant content. Complete admission and instruction
   approval independently and require exactly one lower-authority agent turn.

## Adapter matrix

Repeat the To/CC cases with an OpenMail receiver and, when scoped credentials
are available, a Sendmux receiver. Provider-specific list operations remain
fetch optimizations; assertions use the same normalized delivery contract:

- exact provider inbox identity and address;
- distinct To, CC, and BCC values;
- correct delivery role when observable;
- explicit unread state before processing and read/seen state afterward;
- one durable claim and reply after filtered and recovery views are unioned;
- no duplicate after a clean restart; and
- failure before pair delivery when the adapter reports a different inbox ID.

## Teardown

Stop the exact Compose project, remove its isolated secret, and prove no test
container remains. Delete only the inboxes and policy rules inventoried for the
run. Verify every deleted provider ID is absent or explicitly soft-deleted, and
confirm the normal service, protected inbox, registry, and databases are
unchanged.
