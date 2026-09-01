# Dear Machine,

## Your computer has an inbox.

`Dear Machine,` gives your computer a private email address.

Install it once, then email your computer from anywhere. `Dear Machine,` uses the capable agents already installed on your device—such as Codex, Claude Code, or Pi—to complete the work locally and reply in the same email thread.

Your files, credentials, applications, and agent sessions stay on your computer.

No bots. No dashboards. No new messaging app to learn.

---

## Current Alpha Quick Start

Nix packages the DearMachine Client and Agent Manager together. Install
`machtiani` and your chosen backend separately so they are available on `PATH`.

### 1. Install the native package

```bash
nix profile install .#dearmachine
command -v dearmachine agent-manager machtiani
```

### 2. Configure an installed backend

```bash
dearmachine setup-agents --backend codex
```

The backend command itself—`codex` in this example—is discovered from the host
`PATH` and uses its normal host credentials.

### 3. Choose an email transport and start

DearMachine uses the same pair-and-reply workflow with AgentMail, OpenMail, or
Sendmux. The `--transport` flag chooses the adapter, and each adapter reads only
its own credential. There is no DearMachine-specific allow-list setting: pair
creation registers the exact `--email` address locally and asks the selected
adapter to apply any provider-side correspondent policy it supports.

Every adapter can provision a new inbox as part of pair creation. This example
uses AgentMail:

```bash
export AGENTMAIL_API_KEY_FILE="$HOME/.config/dearmachine/agentmail-api-key"

dearmachine up --create \
  --email you@example.test \
  --new-inbox \
  --transport agentmail \
  --magnifica-humanitas
```

OpenMail and Sendmux are drop-in replacements. Change only the credential and
transport value:

| Adapter | Credential file variable | New-inbox selection |
| --- | --- | --- |
| AgentMail | `AGENTMAIL_API_KEY_FILE` | `--new-inbox --transport agentmail` |
| OpenMail | `OPENMAIL_API_KEY_FILE` | `--new-inbox --transport openmail` |
| Sendmux | `SENDMUX_API_KEY_FILE` | `--new-inbox --transport sendmux` |

To share or adopt an existing inbox with any adapter, replace `--new-inbox`
with `--inbox '<exact-inbox-id-or-address>'`. Sendmux uses its Infrastructure
key for creation and pair policy, then stores the returned mailbox-scoped
credential privately for receiving and replying. A separate Sending key is not
required.

Provider-specific credential and inbox details are documented in the
[transport reference](docs/email-transport-alternatives.md).

On the first run, `up --create` also initializes the selected entry-point
repository if it is absent. With no flag, that is the default repository at
`~/.dearmachine/entrypoint/main`. It then records one isolated pair database and
starts all registered pairs in one background client. Inspect or stop it with:

```bash
dearmachine status
dearmachine down
```

Plain `dearmachine up` starts all registered pairs again. Service managers and
containers use `dearmachine up --foreground` so they supervise the real client
process.

### 4. Add another pair

Stop the client, then explicitly choose whether the new pair gets a new inbox
or shares an existing registered inbox. To share the first inbox:

```bash
dearmachine down
dearmachine up --create \
  --email another@example.test \
  --inbox '<inbox-address-or-uuid>' \
  --magnifica-humanitas
```

The inbox selector is shown by `dearmachine status`. Sharing is never inferred;
use `--new-inbox --transport <transport>` instead when the pair should have its
own inbox. Pair email addresses and pair UUIDs are also the selectors accepted
by `dearmachine up --pair` and `dearmachine inbox ... --pair`.

DearMachine runs natively by default and does not require systemd. The optional
native systemd launcher and Nix/OCI testing/deployment helpers are documented in
[`dearmachine/runbooks/host-install.md`](dearmachine/runbooks/host-install.md).
Live integration tests use isolated container deployments by default.

`dearmachine` supersedes the obsolete `machinemail` command. Migrate any
legacy `~/.config/machinemail` configuration into `~/.dearmachine/config`;
new installations must not create or use the old path.

---

## Optional End-to-End Encryption with OpenPGP

`Dear Machine,` can use OpenPGP so the hosted `Dear Machine,` mail service relays encrypted messages without being able to read their contents.

Provide the authorized user's public key when bringing the device online:

```bash
dearmachine up \
  --email <your>@proton.me \
  --pgp ~/<your>-public-key.asc
```

In OpenPGP mode, `Dear Machine,`:

- imports and pins the user's public key locally
- generates a device-specific OpenPGP keypair on the computer
- keeps the device private key on the computer
- decrypts incoming tasks only after they reach the computer
- encrypts replies to the user's public key before sending them
- signs messages so both sides can verify who sent them

The `--pgp` file must contain the public key belonging to the authorized email address. It must never contain the user's private key.

After setup, `Dear Machine,` displays the device key fingerprint and exports its public key:

```text
✓ Generated device OpenPGP identity
✓ Imported and pinned the alpha user's public key
✓ End-to-end encrypted mail is enabled

Device address:

    my-laptop-k7vx9m@dearmachine.to

Device OpenPGP fingerprint:

    7A91 2F81 C6E4 ... 10B7

Device public key:

    ~/.dearmachine/config/device-public.asc
```

Import the device public key into the email client used to contact the computer. Proton Mail and other OpenPGP-capable clients can then encrypt messages to the device address, preferably using PGP/MIME.

### Encryption flow

When the user sends a task:

```text
User's email client
    ↓ signs with the user's private key
    ↓ encrypts to the device public key
`Dear Machine,` hosted relay
    ↓ sees encrypted content only
Local `Dear Machine,` client
    ↓ decrypts with the device private key
Local agent executes the task
```

When the computer sends a reply:

```text
Local `Dear Machine,` client
    ↓ signs with the device private key
    ↓ encrypts to the user's public key
`Dear Machine,` hosted relay
    ↓ sees encrypted content only
User's email client
    ↓ decrypts with the user's private key
```

Both directions must be encrypted for the hosted relay to remain unable to read the conversation.

### Verify keys before trusting them

Public keys must be verified through a trusted channel. Do not rely exclusively on a key delivered through the same hosted relay it is intended to protect.

Recommended setup:

1. Generate the device key locally.
2. Display its fingerprint in the terminal.
3. Import the device public key directly into the user's email client.
4. Compare or scan the fingerprint locally.
5. Import and pin the user's verified public key on the computer.
6. Require valid signatures in both directions.

This prevents a compromised relay from silently replacing a public key and intercepting future messages.

### What the relay can still see

OpenPGP protects message contents, replies, and attachments, but normal email delivery still exposes some metadata to the relay, including:

- sender and recipient addresses
- delivery times
- message size
- SMTP routing information
- the device email address
- some thread and transport headers
- the outer subject line, unless a generic subject is used

`Dear Machine,` can use a generic outer subject such as `Encrypted `Dear Machine,` message` and place the real conversation title inside the encrypted body.

The relay may still delay, duplicate, drop, or block encrypted messages. It cannot silently read or modify a message that passes signature and replay checks.

### Replay and thread protection

Encrypted messages should include signed protocol metadata such as a thread identifier, sequence number, timestamp, nonce, and hash of the previous message. The local client can then reject replayed, reordered, cross-thread, or modified commands.

Useful OpenPGP commands may include:

```bash
dearmachine openpgp fingerprint
dearmachine openpgp export-public
dearmachine openpgp rotate
dearmachine openpgp revoke
```

---

## Basic Usage

Send an email to your `Dear Machine,` address.

```text
To: my-laptop-k7vx9m@dearmachine.to
Subject: Check my project

Tell me what branch I am on in /path/to/dearmachine and whether there
are any uncommitted changes.
```

`Dear Machine,` will run the task on your computer and reply in the same thread.

### Continue the conversation

Reply to the latest `Dear Machine,` message:

```text
Run the tests and tell me what fails.
```

Replies continue the same conversation and local agent session. Every Dear
Machine response carries a stable opaque conversation reference in its visible
footer. The local client removes that footer before invoking the agent and can
use the reference to preserve continuity when a mail provider reports the
reply under a different thread identifier.

When a newer same-thread message is waiting, a long-running earlier turn is
gracefully stopped after one inbox poll interval and the same session continues
with the newer message. A turn that finishes inside that short grace may still
reply. Every message remains part of the conversation context.

### Ask for coding work

```text
Subject: Fix reconnect bug

Use /path/to/dearmachine.

Find the reconnect bug, fix it, run the relevant tests, and show me what
changed. Do not commit.
```

### Ask about local files

```text
Subject: Vacation story

Find something funny in my /path/to/vacation-photos folder and tell me the story.
```

### Ask for a status update

```text
Subject: Agent status

Give me an update on all coding-agent work currently running on this computer.
```

### Ask for help

Send:

```text
Subject: help

How do I use another project directory?
```

`Dear Machine,` will inspect the local installation and explain what to do.

---

## Plain English First

You do not need to remember agent commands, session IDs, model flags, or configuration syntax.

Write naturally:

```text
Use the payments repo, think carefully, fix the failing retry test, and do not
push anything.
```

`Dear Machine,` chooses the best available local agent and handles the details.

You can still be explicit when useful:

```text
Directory: /path/to/payments
Agent: codex
Model: default
Reasoning: high

Review the authentication flow and fix anything serious.
```

---

## Supported Agents

`Dear Machine,` automatically discovers supported local agents.

Initial support:

- Codex CLI
- Claude Code
- Pi

More agents can be added through open-source plugins.

You do not need every agent installed. `Dear Machine,` uses what is available.

---

## How Conversations Work

- Start a new email thread for a new conversation.
- Reply to continue the existing conversation.
- A stable Dear Machine footer reference preserves continuity across provider
  thread changes; it is transport metadata and is not sent to the local agent.
- The email subject is the conversation name.
- `Dear Machine,` replies only inside user-initiated threads.
- Your email account remains the visible conversation history.
- `Dear Machine,` does not require a separate chat application.

When OpenPGP mode is enabled, the visible outer subject may be generic while the real conversation name remains inside the encrypted message.

---

## Privacy

`Dear Machine,` is designed as a relay, not a conversation archive.

- Work runs on your computer.
- Your model and application credentials stay on your computer.
- Your files do not move to `Dear Machine,` servers just because an agent reads them.
- `Dear Machine,` does not retain your conversation history.
- The hosted service routes messages between your email and your device.
- With OpenPGP enabled in both directions, message bodies and attachments remain encrypted while passing through the hosted relay.

The complete client and router are open source. You can inspect them, modify them, or operate your own router.

---

## Subscription

The hosted `Dear Machine,` service is billed per device email address.

When you first run:

```bash
dearmachine up --email <your>@gmail.com
```

`Dear Machine,` pairs that email address with the device and prints and emails a unique subscription link. After setup, `dearmachine up` simply starts the daemon.

Until the subscription is active, running `dearmachine` will show that link again.

Self-hosting the open-source router does not require a hosted `Dear Machine,` subscription.

---

## Common Commands

Check the local service:

```bash
dearmachine status
```

Start the local service:

```bash
dearmachine up
```

On first run, `Dear Machine,` opens setup and asks which email address to pair. You can also provide it directly:

```bash
dearmachine up --email <your>@gmail.com
```

Enable OpenPGP using the authorized user's public key:

```bash
dearmachine up --email <your>@proton.me --pgp ~/<your>-public-key.asc
```

Stop the local service:

```bash
dearmachine down
```

Show the device address:

```bash
dearmachine address
```

Inspect discovered agents:

```bash
dearmachine agents
```

Get help:

```bash
dearmachine help
```

---

## Why Email?

`Dear Machine,` is built for asynchronous work.

Real agent jobs often take minutes or hours. Email already provides:

- delivery from nearly any device
- notifications
- threading
- attachments
- offline composition
- search and history
- no additional account or app setup

The interface is intentionally ordinary:

```text
To
Subject
Message
Reply
```

You already know how to use `Dear Machine,`.

---

## Development

- [DearMachine Client](dearmachine/README.md) describes the current Go alpha architecture and runtime.
- [Native installation](dearmachine/runbooks/native-install.md) is the default portable deployment path.
- [Optional container installation](dearmachine/runbooks/host-install.md) describes the Nix OCI, rootless Podman, and current Linux lifecycle helper.
- [DearMachine Client operations](dearmachine/runbooks/operate-entrypoint-client.md) covers launching, monitoring, and stopping the normal entry-point client.
- [Runbooks](dearmachine/runbooks/README.md) indexes normal operations, maintenance, and isolated live-test procedures.
- [Testing](dearmachine/TESTING.md) explains how to run and extend the automated suite.
- [Roadmap](ROADMAP.md) outlines the next engineering tracks and alpha milestones.
- [Custom Backend Guide](docs/custom-backend-guide.md) explains how to register your own backend agent.

---

## Open Source

`Dear Machine,` is open source so users can trust the software that operates on their computers.

You can:

- inspect the client and router
- inspect the OpenPGP implementation and key handling
- create agent plugins
- run your own router
- operate a specialized public router
- contribute improvements upstream

The hosted service is the easiest way to get started. The protocol remains open.

---

## The Promise

```bash
nix profile install .#dearmachine
dearmachine setup-agents --backend codex
```

Then run it natively with an AgentMail inbox, or choose the optional OCI
deployment when an explicit container boundary is useful.

The intended hosted `dearmachine up` onboarding and OpenPGP commands described
above remain product direction rather than commands implemented by the current
alpha client.
