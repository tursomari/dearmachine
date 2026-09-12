# Thread-scoped guests

A guest can participate only in a specifically authorized provider conversation.
The grant belongs to one permanent pair, its provider inbox, the guest's exact
canonical email address, and the provider thread ID. It persists until revoked;
there are no leases, expiry jobs, or automatic historical grant backfills.

## Invite, inspect, revoke

```console
dearmachine guest allow guest@example.test --pair owner@example.test --message-id invitation-id
dearmachine guest list --pair owner@example.test
dearmachine guest revoke guest@example.test --pair owner@example.test --thread-id provider-thread-id
dearmachine guest revoke guest@example.test --pair owner@example.test --all
```

Select a pair by its unambiguous email or UUID. `allow` retrieves the specified
message through that pair's inbox. Its outer From must match the controller, and
both Dear Machine and the guest must appear visibly in To or CC. BCC, quoted
headers, forwarded inner messages and body addresses do not qualify. Another
registered controller cannot become a guest, including when that controller's
worker is not selected. Explicitly selecting an older qualifying message is
supported; no mailbox scan grants historical access automatically.

`allow` is the local operator's authorization of those visible invitation facts.
It does not prove that From was authenticated. Automatic granting additionally
requires a transport to attribute the exact controller mailbox to the message.
Current AgentMail, OpenMail and Sendmux adapters cannot establish that contract,
so **automatic invitations are disabled**. The daemon and native guest help/list
surface this limitation. Generic SPF/DKIM/DMARC success authenticates a domain,
not the exact mailbox owner; raw From matching is never substituted for that
missing automatic-grant evidence.

Concierge can run these commands for an explicit user request. It must inspect
native `guest --help` before using them on an older installed version. A grant
creates no permanent pair and changes no existing pair's privileges.

## Participation gates

Guest replies must visibly include Dear Machine and the pair's controller in
To or CC: use Reply All. A provider allowlist entry or knowledge of a thread ID
is insufficient. A guest cannot start an unrelated conversation, invite another
guest, or become a controller. Thread aliases and forwarded-session mappings do
not expand the exact provider-thread grant.

Delivery authorization permits the existing private admission flow. Admission
and approval of the held instruction are separate controller decisions. Retained
trust can bypass routine instruction confirmation only with valid delivery
authorization and admission. Guest work remains lower authority; approved guest
results are sent privately to the controller. This uses the permanent pair's
existing reply/send permissions while adding only guest receive permission.

## Revocation and provider synchronization

All guest commands may run while the daemon is active. SQLite orders local
revocation against the actual subprocess-start boundary. Revocation prevents
subsequent guest execution starts, including queued work, held decisions and
recovered work. Already-started work can still have effects; revocation does not
roll them back. Admission and trust remain stored but cannot bypass revocation.

Replayed invitations do not resurrect revoked grants. A new qualifying automatic
invitation (when supported) or explicit validated `allow` can restore access.
Grant generations and durable message correlation keep prior held/queued work
invalid after reauthorization. Send a new guest instruction for the new grant.

Receive permissions are shared across every applicable grant and permanent pair
in the provider inbox. Only a final unneeded entry with established DearMachine
ownership may be removed. Pre-existing entries, uncertain additions after lost
responses, and externally changed entries are preserved. Sendmux removal uses
its filter ETag; a changed revision conservatively loses ownership. No guest
operation changes a provider's global policy mode or unrelated rules.

Local changes commit before remote reconciliation. `list` shows active/revoked
grants and permission records with `Pending`, `Owned`, and `LastError`. A nonzero
exit after local success says synchronization is pending. Local revocation still
applies while credentials or the provider are unavailable. The daemon retries
pending intents on subsequent polls; an explicit command also reconciles them.

The [verification guide](../verification/guest/README.md) defines the trusted
boundaries and finite proof scope. The [testing entrypoint](../TESTING.md) owns
unit, race, container, formal and live evaluation commands.
