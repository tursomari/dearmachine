# ADR: Keep Gmail out of the default mail-transport catalog

## Status

Accepted

## Context

DearMachine’s product is not “connect the user’s existing mailbox.” The README
states the promise directly: “Your computer has an inbox.” / “Install it once,
then email your computer from anywhere.” / “No bots. No dashboards. No new
messaging app to learn.” Staging and branding copy is sharper still: “Email
your computer. It writes back.” The intended motion is
`dearmachine up --email <you>@gmail.com` — pair the *user’s* address, print a
*device* address. The user is not supposed to create a mailbox, password,
forwarding rule, DNS record, webhook, or mail-client integration.

The transport seam (`Transport`, `Message`, `AttachmentRef` in
`dearmachine/internal/client/transport.go`) is provider-neutral. Construction
on current main is still hardcoded AgentMail:
`defaultDependencies().newTransport` always calls
`client.NewAgentMailTransport`. A static metadata catalog plus factory,
selected by `--transport` and defaulting to `agentmail`, is the intended
construction seam. That catalog — documented as unimplemented future work in
`docs/email-transport-alternatives.md` — is the product surface for “which
inboxes this computer can be.” Putting a provider in it is an adoption
decision, not just an adapter exercise.

Two generations of email API are in play:

- **AgentMail-generation:** hosted, programmable agent inboxes. API key, mint
  an inbox, poll/thread/reply/ack. Peers include OpenMail, Dead Simple Email,
  AGmail, AgenticEmail, Lumbox, Sendmux, Nylas Agent Accounts, and others
  surveyed in `docs/email-transport-alternatives.md`. Custom-domain /
  enterprise inbox hosting is common. Competition is the escape hatch if a
  vendor becomes abusive.
- **Human-mailbox generation:** Gmail API, IMAP/SMTP, Microsoft Graph. These
  wrap a pre-existing human inbox. Auth is OAuth (consent, refresh, token
  files), not “create key, create inbox.”

Gmail was implemented on a side branch as a second adapter to pressure-test
the seam. It proved the *interface* is reusable and the *wiring* was the part
that was never drop-in. It also produced a generation mismatch the catalog
should not normalize:

- OAuth token files and an operator-configured home vs `AGENTMAIL_API_KEY`
- `UNREAD` / `INBOX` vs unread → read
- draft+send vs `Messages.Reply`
- adapter-owned `X-DearMachine-Idempotency-Key` because Gmail has no request
  idempotency header
- overloaded `--inbox-id` (AgentMail inbox ID vs email address)
- sender allowlist bolted onto the adapter, not onto `Transport`

`docs/email-transport-alternatives.md` already says no surveyed alternative is
a wire-level drop-in. Restricting the catalog to AgentMail-generation APIs
therefore simplifies **adapters and the user path**. It does **not**
drastically simplify the seam: factory, selection, per-provider env, and
pairing still exist among peers.

Gmail as the **user’s** mail client is the intended pairing source. Gmail as
the **device** inbox fights the product. A default catalog that offers both
teaches the wrong setup story: “bring a human mailbox and an OAuth home”
instead of “your computer already has an address.”

## Decision

Keep Gmail out of the default mail-transport catalog.

1. The default catalog is AgentMail-generation providers only — programmable
   hosted agent inboxes that can be minted by API. AgentMail is the current
   default (today it is the only wired transport; `--transport` should default
   to `agentmail` when a catalog exists). Future peers (OpenMail and others
   that fit poll/thread/reply/ack) may be added as catalog IDs when they earn
   an adapter. They are interchangeable enough to leave a vendor, not
   identical enough to collapse the seam.

2. Gmail is not a device-inbox peer and must not be a default selectable
   catalog entry. It must not appear in `dearmachine up` / first-run setup,
   default help as a peer of AgentMail, or any path that implies “connect
   Gmail and the computer is the inbox.”

3. Gmail remains valid only as the *user’s* address to pair against a
   provisioned device inbox. Pairing policy is “who may write the computer,”
   not “the computer is a Gmail account.”

4. The existing Gmail adapter is seam evidence, not product direction. It is
   not merged. It does not justify a TOML `transport =` key, a DeviceConfig
   bump, or generalizing auth in the factory.

5. This decision does not change `Transport`, `client.New`, or AgentMail’s
   constructor. Construction stays a metadata catalog that does not hold
   credentials.

## Consequences

**Positive**

- Setup can aim at the documented end state: create account / third-party
  login, and the computer already has an address. AgentMail-class APIs can
  mint that inbox; `dearmachine up` can hide the vendor.
- Enterprise inboxes under our domain, and vendor competition as an escape
  hatch, stay available without teaching users to OAuth a human mailbox into
  the device role.
- Default adapters stay in one generation: API key, inbox ID,
  poll/thread/reply/ack. Gmail’s OAuth files, UNREAD/INBOX, draft+send, and
  adapter-owned idempotency do not become implicit catalog requirements.
- The seam stays honest. We do not pretend a human mailbox is a drop-in
  AgentMail.

**Negative**

- Operators who wanted “just use my Gmail” as the device inbox will not get
  that in the default product. That is the point; it is the wrong generation
  for this promise.
- A working Gmail adapter, if left in some other branch, will keep tempting
  catalog inclusion. The default list must stay a product decision, not a
  completeness score.
- Same-generation peers are still not wire-compatible (unread vs labels vs
  `is_read`, `Idempotency-Key` vs `client_id`). Leaving Gmail out does not
  remove factory, `--transport`, or per-provider env.

**Neutral**

- `--inbox-id` remains one overloaded string until hosted provisioning lands.
  That is a later `dearmachine up` problem, not a reason to keep Gmail in the
  default catalog.
- DeviceConfig v1 still has no `transport =` key. This ADR does not add one.

## References

- `README.md` — “Your computer has an inbox.”
- `docs/email-transport-alternatives.md` — peer survey; unimplemented catalog
  / `--transport` selection layer; no surveyed alternative is a wire-level
  drop-in
- `ROADMAP.md` §5 (transport vs orchestration) and §6 (`dearmachine up` /
  `status` / `down`)
- `dearmachine/internal/client/transport.go` — provider-neutral `Transport`,
  `Message`, `AttachmentRef`
- `dearmachine/cmd/dearmachine/main.go` — current construction still
  hardcodes `NewAgentMailTransport`
