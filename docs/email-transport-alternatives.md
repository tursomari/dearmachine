# Dear Machine, Email Transport Alternatives

- **Revision date:** 2026-08-19
- **Status:** Living document; re-verify capabilities, terms, availability, and pricing before adopting any option.

## Purpose

This document records the transport abstraction now used by the DearMachine
Client, the implemented OpenMail and Sendmux adapters, and other potential
alternatives to AgentMail. The remaining provider comparisons are a technical
survey, not an adoption decision.

## Current transport

The DearMachine Client defaults to AgentMail through `agentmail-go` v0.16.0 and
also provides selectable OpenMail and Sendmux adapters. All implement a small
but stateful mailbox surface:

- Poll unread inbound messages, including pagination and deterministic timestamp
  ordering.
- Fetch each full message with its thread ID, sender, timestamp, and usable text
  body. Current body extraction prefers extracted text, then text, then preview.
- Fetch thread history and individual messages.
- Send a reply to a specific message with an idempotency key and retain the
  outbound message ID.
- Detect a prior reply receipt by finding an outbound thread message whose
  `In-Reply-To` points to the inbound message.
- Mark a message processed by moving it from `unread` to `read` state.
- Authenticate with an API credential and address a configured inbox ID.

The default application loop polls every 60 seconds. For each accepted message,
it completes orchestration and reply handling before marking the message as
processed. A replacement therefore needs equivalent observable behavior, even
when the provider names or implements these operations differently.

Polling, thread history, send/reply, reply-receipt detection, processed state,
and attachment fetches now sit behind an adapter so provider-specific REST
behavior does not alter the application state machine.

## Alternative providers

These ratings measure likely adapter fit, not overall product quality. **High**
means the published summary exposes most required primitives; **Medium** means
the mailbox is plausible but important semantics need verification; **Low**
means a larger behavior gap is likely; and **Build-yourself** means the project
would own material mailbox or workflow infrastructure. None implies byte-level
compatibility with the current AgentMail SDK.

### Summary

| Alternative | Category | Drop-in fit | One-line description |
|---|---|---:|---|
| [OpenMail](https://openmail.sh/) | Closest | High | Agent-focused provisioned inboxes with send/receive, threads, real-time events, and custom domains. |
| [Dead Simple Email](https://deadsimple.email/) | Closest | High | Dedicated agent inboxes with send/receive, webhooks, a dashboard, and MCP. |
| [AGmail](https://agmail.ai/) | Closest | High | Agent inboxes with send, read, reply, search, custom domains, APIs, and skills. |
| [AgenticEmail](https://agenticemail.dev/) | Closest | High | Runtime-created inboxes with REST, threads, webhooks, WebSockets, SDKs, and hosted MCP. |
| [Lumbox](https://lumbox.co/) | Closest | Medium | Agent inboxes oriented around replies, OTPs, login, verification, and MCP workflows. |
| [Sendmux](https://sendmux.ai/) | Closest | High | Agent mailbox API with send/receive, threads, attachments, webhook/SSE events, and outbound-provider choices. |
| [Xobni.ai](https://www.xobni.ai/) | Closest | High | Agent inboxes with MCP, REST, webhooks, attachments, semantic search, storage, and calendar features. |
| [AgenticMailbox](https://agenticmailbox.com/) | Closest | Medium | Agent addresses exposed by API, webhooks, and MCP with inbound AI analysis. |
| [agentsbase](https://agentsbase.net/) | Closest | Medium | API-created mailboxes with send/receive, attachments, search, OTP extraction, webhooks, and MCP. |
| [EmailAgent.dev](https://emailagent.dev/) | Closest | Medium | Provisioned inbox identities with send/receive, custom domains, scoped keys, and TypeScript/Python SDKs. |
| [EmailForAgent](https://emailforagent.com/) | Closest | Medium | Dedicated agent addresses with API-driven send/receive and a human dashboard. |
| [DevInbox](https://devinbox.io/) | Direct variant | Medium | Persistent REST/MCP inboxes aimed especially at OTP, browser-agent, and automated-test workflows. |
| [Mailgent](https://mailgent.dev/) | Direct variant | Low | A real mailbox combined with credential, TOTP, calendar, decentralized-identity, and wallet services. |
| [Daimon.email](https://daimon.email/) | Direct variant | Medium | Agent inboxes with API integrations, webhooks, spam controls, and paid custom domains. |
| [ActionLayer](https://www.actionlayer.dev/) | Direct variant | Medium | Business agent inboxes with API/MCP, threading, a unified inbox, and human approvals. |
| [AgenticMail.com](https://agenticmail.com/) | Direct variant | High | Dedicated inboxes with send/reply/forward APIs, webhooks, CLI, SDKs, and MCP. |
| [Crustacean Email](https://crustacean.email/) | Direct variant | Medium | API-only dedicated mailboxes that intentionally do not expose IMAP/SMTP credentials. |
| [agentinbox](https://agentinbox.site/) | Direct variant | High | Hosted or self-hosted inboxes with send/receive, threads, webhooks, OTP extraction, and a dashboard. |
| [AI-Agent.email](https://www.ai-agent.email/) | Direct variant | Medium | Controlled mailboxes with API-readable inbound mail and policy-gated replies. |
| [Mail4AI](https://www.castelis.com/en/insights-ressources/ai-email-agent/) | Direct variant | Medium | French-hosted per-agent mailboxes with MCP/API and inbound/outbound allowlists. |
| [Nylas Agent Accounts](https://www.nylas.com/products/agent-accounts/) | Larger vendor | High | Hosted agent accounts with addresses, inboxes, threads, folders, drafts, attachments, IMAP/SMTP, and calendar. |
| [Hostinger Agentic Mail](https://www.hostinger.com/agentic-mail) | Larger vendor | Medium | Isolated agent inboxes with send/receive API, webhooks, MCP, policies, and custom-domain support. |
| [Bavimail](https://bavimail.com/) | Hybrid | Medium | Per-agent two-way inboxes within a broader transactional and marketing email platform. |
| [Inbound](https://inbound.new/) | Hybrid | Medium | Send/receive/reply APIs with automatic threading and webhook routing for many domain addresses. |
| [Cloudflare Email Service / Workers](https://cli.nylas.com/guides/agentmail-vs-nylas-vs-cloudflare-email) | Build it yourself | Build-yourself | Email primitives that can underpin agent mail when paired with custom storage, state, threading, and workflows. |
| [Open-source AgenticMail](https://github.com/agenticmail/agenticmail) | Self-hosted | Build-yourself | A Stalwart-based project providing isolated mailboxes, REST, and MCP under operator control. |

### Closest hosted alternatives

#### OpenMail — High

OpenMail is implemented through its REST API at
`https://api.openmail.sh/v1`. The public OpenAPI document confirms Bearer API
key authentication, paginated inbox messages, unread-thread filtering, thread
history, native `Idempotency-Key` sends, thread-level read updates, and
message-ID/filename attachment fetches. No maintained Go SDK was needed.

The impedance is real and remains visible in the adapter:

- OpenMail read state belongs to a thread, not a message. `Poll` therefore
  returns only the newest inbound message from each unread thread, and
  `MarkProcessed` marks the complete thread read. A thread containing any
  non-allow-listed correspondent is ignored entirely so it is never mutated.
- OpenMail API paths require an inbox ID. DearMachine accepts either the ID or
  address; an address is resolved through the paginated inbox list, while an ID
  is verified with the inbox endpoint. The resolved address is also checked on
  every message.
- Sends have native 24-hour idempotency. Live responses expose signed Internet
  Message-IDs and reply headers separately from opaque API IDs. Approval
  correlation resolves those IDs within the same inbox and thread.
- Sender authentication supports only reconstructable, unencoded single-part
  plain text through `raw.message-headers` and local DKIM verification. HTML,
  multipart messages and attachments cannot currently be authenticated. See
  [the authentication contract](guest-authorization.md#sender-authentication).
- There is no single-message get endpoint. Recovery scans the paginated inbox
  message list for the requested ID.
- Attachments are addressed by message ID and filename. The adapter exposes an
  opaque transport attachment ID, limits bytes before returning content, and
  refuses redirects outside the configured OpenMail API origin.
- Correspondent authorization is established by `dearmachine up --create` and
  enforced centrally by the pair router. A message is available only when its
  canonical sender is the pair email and its recipient is the registered inbox.
  Other correspondents are not surfaced, replied to, fetched, or marked
  processed.

#### Dead Simple Email — High

Dead Simple Email provides dedicated agent inboxes, send/receive, webhooks, MCP, and a management dashboard. The core mailbox and event primitives look close to the DearMachine Client's needs, while polling, thread lookup, read state, idempotency, and Go client details remain to be confirmed.

#### AGmail — High

AGmail exposes agent inbox operations including send, read, reply, and search, plus custom domains, API access, and skills. Read/reply operations suggest a small adapter, but exact unread filtering, thread metadata, idempotency, receipt detection, and SDK maturity require an integration spike.

#### AgenticEmail — High

AgenticEmail supports runtime-created inboxes, REST, threading, webhooks, WebSockets, scoped keys, SDKs, and hosted MCP. It maps well to polling, thread history, and replies, subject to verifying message-state mutation, idempotency, `In-Reply-To` fidelity, and whether an appropriate Go SDK exists.

#### Lumbox — Medium

Lumbox provides agent inboxes for waiting on replies, extracting OTPs, and handling login or verification workflows through MCP. Its workflow orientation is useful, but the available summary does not establish general REST polling, thread history, labels, idempotent sends, or Go support.

#### Sendmux — High

Sendmux is implemented with the official `sendmux.ai/go/mailbox` client. The
mailbox API exposes cursor-paginated unread messages, message and thread
history, selected RFC headers, flags, native `Idempotency-Key` sends, and
presigned attachment URLs. Adding it required no application-state-machine or
SQLite changes: the provider-specific work remains in one adapter plus catalog
and factory registration.

The adapter keeps these provider deltas explicit:

- a mailbox ID or address resolves through the credential-scoped mailbox
  endpoint and must identify an active mailbox;
- inbound, outbound, and whole-thread exact-address authorization is local and
  fail-closed, matching the existing OpenMail safety boundary;
- the HTTP API exposes ordinary sends and X-headers, but no reply operation or
  caller-supplied RFC `In-Reply-To` / `References`; the stable Dear Machine
  footer therefore carries continuity when the outbound answer and next human
  reply receive a new provider-local thread ID;
- native idempotency is passed on every send, while SDK retries are currently
  disabled until Dear Machine adopts one deliberate operational retry policy;
- processed state maps to the message's `seen` flag; and
- attachment metadata provides a short-lived presigned URL, which is fetched
  over HTTPS without forwarding the mailbox credential and with the shared
  byte limit enforced.

The remaining live proof is the isolated two-turn LSE. It requires a valid
mailbox credential and a separately authorized external correspondent; neither
participant address belongs in code, fixtures, documentation, or evidence.

#### Xobni.ai — High

Xobni.ai gives agents dedicated inboxes with REST, MCP, webhooks, attachments, semantic search, and adjacent storage/calendar features. REST and webhooks offer good integration paths, but exact threading, unread labels, idempotent replies, receipt fields, and Go SDK support are not confirmed by the source summary.

#### AgenticMailbox — Medium

AgenticMailbox combines agent email addresses with API, webhooks, MCP, and built-in analysis of incoming messages. Send/receive automation appears viable, but the summary does not confirm polling pagination, threads, message labels, idempotency, reply receipts, or language-specific SDKs.

#### agentsbase — Medium

agentsbase creates mailboxes by API and supports send/receive, attachments, search, OTP extraction, webhooks, and MCP. The main primitives are present, but thread identity, read/unread state, idempotent send, `In-Reply-To` fidelity, and Go support need to be checked.

#### EmailAgent.dev — Medium

EmailAgent.dev offers provisioned identities, send/receive, custom domains, scoped keys, and TypeScript/Python SDKs. Its known SDKs do not include Go, and the source does not establish polling, threading, labels, idempotency, or reply receipt semantics.

#### EmailForAgent — Medium

EmailForAgent provides a dedicated email address per agent, API-driven send/receive, and a human dashboard. This satisfies the basic transport shape, but thread history, unread processing, idempotency, reply correlation, event delivery, and SDK availability are unspecified.

### Direct variants

#### DevInbox — Medium

DevInbox offers persistent programmable inboxes with send/receive over REST and MCP, with an emphasis on OTPs, browser agents, and testing. Persistent mailboxes fit the identity model, but production threading, labels, idempotency, reply receipts, authorization, and SDK maturity need verification.

#### Mailgent — Low

Mailgent pairs a real mailbox with credential vaulting, TOTP, calendar, decentralized identity, and wallet capabilities. The mailbox may be adaptable, but the source does not confirm the specific polling, threading, reply, idempotency, processed-state, or SDK contracts the DearMachine Client requires.

#### Daimon.email — Medium

Daimon.email supplies agent inboxes, API integrations, webhooks, outbound-spam controls, and custom domains on paid plans. API and webhooks support message flow, while polling, threads, labels, idempotency, reply receipts, and policy effects on automated sending need confirmation.

#### ActionLayer — Medium

ActionLayer provides business inbox identities with API/MCP, threading, a unified inbox, and strong human-in-the-loop approvals. Threading maps directly, but mandatory approval policies and unknown polling, label, idempotency, receipt, and Go SDK semantics could require more than a thin adapter.

#### AgenticMail.com — High

AgenticMail.com exposes dedicated addresses plus send, reply, and forward APIs, webhooks, CLI tooling, SDKs, and MCP. Explicit reply support makes it promising, although poll/list behavior, thread history, unread state, idempotency, receipt correlation, and Go SDK availability must be re-verified.

#### Crustacean Email — Medium

Crustacean Email offers API-only dedicated mailboxes and deliberately withholds IMAP/SMTP credentials. API-only access is compatible with an adapter, but the source does not establish threads, polling filters, processed state, idempotency, reply receipts, or SDK maturity.

#### agentinbox — High

agentinbox includes send/receive, threads, webhooks, OTP extraction, a dashboard, and hosted or self-hosted deployment. The thread model and deployment choice are strong fits; label semantics, polling, idempotent sends, receipt fidelity, and the maintenance burden of self-hosting still need evaluation.

#### AI-Agent.email — Medium

AI-Agent.email exposes inbound messages through an API and allows policy-gated replies from controlled agent mailboxes. Its governance may benefit deployment, but policy gates plus unspecified polling, threads, state labels, idempotency, receipt correlation, and SDKs can change drop-in behavior.

#### Mail4AI — Medium

Mail4AI provides a dedicated mailbox per agent or process, MCP/API access, inbound/outbound allowlists, and French hosting. It has the right mailbox shape, but allowlists and unverified polling, threading, labels, idempotency, receipt, and Go SDK behavior need an adapter proof of concept.

### Larger-vendor alternatives

#### Nylas Agent Accounts — High

Nylas Agent Accounts provisions hosted agent email addresses with inboxes, threading, folders, drafts, attachments, IMAP/SMTP, and calendar support. Its rich mailbox model covers most data needs, but API polling, unread transitions, idempotency, reply receipt fidelity, credential scope, and Go support must be tested against the current contract.

#### Hostinger Agentic Mail — Medium

Hostinger Agentic Mail offers isolated inboxes, send/receive API, webhooks, MCP, allow/block policies, and custom-domain mailboxes. Core delivery is available, while thread history, pull polling, labels, idempotency, reply receipts, SDKs, and policy interactions are not confirmed by the source.

### Hybrid alternatives

#### Bavimail — Medium

Bavimail supports per-agent inboxes and two-way email inside a broader transactional and marketing platform. It may cover basic send/receive, but the mailbox-object model, polling, threads, read state, idempotency, receipts, and SDK availability require confirmation.

#### Inbound — Medium

Inbound supports send/receive/reply APIs, automatic threading, and many addresses on a domain, with inbound delivery oriented around webhook routing. Threading and reply primitives fit, but the DearMachine Client may need its own durable queue/state layer to turn push events into polling and processed acknowledgments.

### Build-it-yourself and self-hosted alternatives

#### Cloudflare Email Service / Workers — Build-yourself

Cloudflare email tooling can be assembled into programmable agent-owned email infrastructure. The DearMachine Client would need to implement or operate mailbox storage, polling queues, threading, read state, idempotency, reply correlation, and likely outbound authorization rather than only writing a provider adapter.

#### Open-source AgenticMail — Build-yourself

The open-source AgenticMail project uses Stalwart to provide isolated mailboxes, REST, and MCP for self-hosted agent email. Its primitives could support the interface, but the DearMachine Client project would assume deployment, upgrades, availability, security, storage, delivery reputation, and contract-verification work.

## Suitability of an agnostic email transport

### Required abstraction surface

A narrow production interface can preserve the current state machine:

```go
type Transport interface {
	Poll(ctx context.Context) ([]Message, error)
	Thread(ctx context.Context, threadID string) ([]Message, error)
	Message(ctx context.Context, messageID string) (Message, error)
	Reply(ctx context.Context, messageID string, payload ReplyPayload, idempotencyKey string) (string, error)
	ReplyReceipt(ctx context.Context, message Message) (string, bool, error)
	MarkProcessed(ctx context.Context, messageID string) error
	FetchAttachment(ctx context.Context, attachmentID string, maxBytes int64) ([]byte, error)
}
```

The normalized `Message` retains provider IDs, thread ID, sender, timestamp,
body text, labels or direction, reply-reference metadata where the provider
offers it, and attachment references. Adapter construction keeps
provider-specific credentials and inbox addressing outside the interface.

The broader scratch section 9 relay surface—transport capabilities,
checkpoint/page-token polling, `observe_send`, `finalize_inbound`, and delivery
state—is deferred to relay work and is intentionally outside this interface.

### Key risks and provider deltas

- **Processed state:** `read`, `unread`, `sent`, folders, labels, flags, and
  acknowledgment tokens are not interchangeable. Some push-first services may
  require client-owned checkpoints instead of remote label mutation.
- **Idempotency:** providers may accept an idempotency header, a request key, a
  client message ID, or nothing. Where native support is absent, the adapter
  needs durable deduplication and safe retry rules.
- **Push versus pull:** webhooks, WebSockets, and SSE reduce polling latency but
  do not automatically implement paginated unread polling. A durable event
  buffer can translate push delivery into the current pull contract.
- **Threading and reply fidelity:** thread identifiers may be provider-local,
  reconstructed from RFC headers, or absent. `Message-ID`, `In-Reply-To`, and
  `References` must survive normalization for reliable reply-receipt detection.
- **Body extraction:** providers expose different parsed-text, plain-text, HTML,
  preview, MIME, and attachment fields. Every adapter needs a documented
  extraction order and timestamp fallback.
- **Address authorization:** provisioning, custom-domain verification, allowed
  senders/recipients, approval gates, spam controls, and outbound reputation can
  alter whether an automated reply is permitted.
- **SDK maturity:** many alternatives advertise REST, MCP, or TypeScript/Python
  SDKs but do not establish a maintained Go SDK. A generated or hand-written
  REST client may be necessary, with provider error and pagination mapping.
- **Operational stability:** availability, retention, rate limits, terms of
  service, pricing, preview status, and product maturity can change quickly.
  Re-verify all of them before adoption.

### Finding

Yes: a behavioral drop-in replacement is feasible once transport operations sit
behind the interface above. No surveyed alternative is byte-compatible with the
current AgentMail client today, but several expose equivalent primitives through
REST plus webhooks or MCP, including threads and labels or read state. Idempotent
sends must be confirmed provider by provider or supplied through adapter-owned
durable deduplication. A contract test suite should decide fit: list and order
unread messages, fetch full bodies, preserve thread/reply headers, retry a send
without duplication, find its receipt after restart, and mark work complete.

## Selecting a transport

Transport selection is implemented as a runtime seam. `--transport` accepts
`agentmail` (the default) or `openmail` on the main command and `inbox skip`.
The static `internal/transports` catalog contains metadata only; its separate
factory dispatches raw provider constructors. Authentication remains
constructor-local, so the AgentMail credential prelude is not run for OpenMail.

Selection intentionally does not add a `transport` key to the version-1 device
TOML or bump its schema. The shipped construction seam proved sufficient
without mixing provider credentials or options into agent-backend
configuration.

For OpenMail, set `OPENMAIL_API_KEY` or `OPENMAIL_API_KEY_FILE` (whose optional
default is `$HOME/.config/dearmachine/openmail-api-key`) and register the pair
with `dearmachine up --create`. The adapter is inspect-only unless both
`DEARMACHINE_LIVE_OPENMAIL=1` and `DEARMACHINE_LIVE_OPENMAIL_APPLY=1` are set;
the second gate enables replies and thread-read mutations. The API host is not
configurable in the production constructor.

## Sources

The comparison began as a maintainer research note titled “AgentMail-Style
Competitor Landscape,” prepared 2026-08-06. That local note is not a repository
dependency. Its product claims are a point-in-time summary and should be checked
against the following provider pages before an implementation or purchasing
decision:

- OpenMail: <https://openmail.sh/>
- OpenMail API reference: <https://docs.openmail.sh/api-reference/introduction>
- OpenMail OpenAPI document: <https://docs.openmail.sh/api-reference/openapi.json>
- Dead Simple Email: <https://deadsimple.email/>
- AGmail: <https://agmail.ai/>
- AgenticEmail: <https://agenticemail.dev/>
- Lumbox: <https://lumbox.co/>
- Sendmux: <https://sendmux.ai/>
- Xobni.ai: <https://www.xobni.ai/>
- AgenticMailbox: <https://agenticmailbox.com/>
- agentsbase: <https://agentsbase.net/>
- EmailAgent.dev: <https://emailagent.dev/>
- EmailForAgent: <https://emailforagent.com/>
- DevInbox: <https://devinbox.io/>
- Mailgent: <https://mailgent.dev/>
- Daimon.email: <https://daimon.email/>
- ActionLayer: <https://www.actionlayer.dev/>
- AgenticMail.com: <https://agenticmail.com/>
- Crustacean Email: <https://crustacean.email/>
- agentinbox: <https://agentinbox.site/>
- AI-Agent.email: <https://www.ai-agent.email/>
- Mail4AI: <https://www.castelis.com/en/insights-ressources/ai-email-agent/>
- Nylas Agent Accounts: <https://www.nylas.com/products/agent-accounts/>
- Hostinger Agentic Mail: <https://www.hostinger.com/agentic-mail>
- Bavimail: <https://bavimail.com/>
- Inbound: <https://inbound.new/>
- Cloudflare Email Service / Workers reference:
  <https://cli.nylas.com/guides/agentmail-vs-nylas-vs-cloudflare-email>
- Open-source AgenticMail: <https://github.com/agenticmail/agenticmail>
