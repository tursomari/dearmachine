# Dear Machine,

## Your computer has an inbox.

`Dear Machine,` gives your computer a private email address.

Install it once, then email your computer from anywhere. `Dear Machine,` uses the capable agents already installed on your device—such as Codex, Claude Code, or Pi—to complete the work locally and reply in the same email thread.

Your files, credentials, applications, and agent sessions stay on your computer.

No bots. No dashboards. No new messaging app to learn.

---

## Quick Start

### 1. Install

```bash
curl -fsSL https://dearmachine.to/install.sh | bash
```

### 2. Bring your computer online

```bash
machinemail up --email <your>@gmail.com
```

`Dear Machine,` will:

- discover supported agents already installed on your computer
- create a private device identity
- pair `<your>@gmail.com` as the authorized sender
- start the local `Dear Machine,` service
- give your computer an email address
- open your subscription link if needed

You will see something like:

```text
✓ Found Codex
✓ Found Claude Code
✓ `Dear Machine,` is running

Your computer's address:

    my-laptop-k7vx9m@dearmachine.to

Authorized sender:

    <your>@gmail.com

Email it from that address anywhere.
```

That is the entire setup.

---

## Optional End-to-End Encryption with OpenPGP

`Dear Machine,` can use OpenPGP so the hosted `Dear Machine,` mail service relays encrypted messages without being able to read their contents.

Provide the authorized user's public key when bringing the device online:

```bash
machinemail up \
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

    ~/.config/machinemail/device-public.asc
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
machinemail openpgp fingerprint
machinemail openpgp export-public
machinemail openpgp rotate
machinemail openpgp revoke
```

---

## Basic Usage

Send an email to your `Dear Machine,` address.

```text
To: my-laptop-k7vx9m@dearmachine.to
Subject: Check my project

Tell me what branch I am on in ~/projects/machinemail and whether there
are any uncommitted changes.
```

`Dear Machine,` will run the task on your computer and reply in the same thread.

### Continue the conversation

Reply to the latest `Dear Machine,` message:

```text
Run the tests and tell me what fails.
```

Replies continue the same conversation and local agent session.

`Dear Machine,` only replies to the latest message in the thread.

### Ask for coding work

```text
Subject: Fix reconnect bug

Use ~/projects/machinemail.

Find the reconnect bug, fix it, run the relevant tests, and show me what
changed. Do not commit.
```

### Ask about local files

```text
Subject: Vacation story

Find something funny in my ~/Vacations folder and tell me the story.
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
Directory: ~/projects/payments
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
machinemail up --email <your>@gmail.com
```

`Dear Machine,` pairs that email address with the device and prints and emails a unique subscription link. After setup, `machinemail up` simply starts the daemon.

Until the subscription is active, running `machinemail` will show that link again.

Self-hosting the open-source router does not require a hosted `Dear Machine,` subscription.

---

## Common Commands

Check the local service:

```bash
machinemail status
```

Start the local service:

```bash
machinemail up
```

On first run, `Dear Machine,` opens setup and asks which email address to pair. You can also provide it directly:

```bash
machinemail up --email <your>@gmail.com
```

Enable OpenPGP using the authorized user's public key:

```bash
machinemail up --email <your>@proton.me --pgp ~/<your>-public-key.asc
```

Stop the local service:

```bash
machinemail down
```

Show the device address:

```bash
machinemail address
```

Inspect discovered agents:

```bash
machinemail agents
```

Get help:

```bash
machinemail help
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

- [Device Client](device-client/README.md) describes the current Go alpha architecture and runtime.
- [Testing](device-client/TESTING.md) explains how to run and extend the automated suite.
- [Roadmap](ROADMAP.md) outlines the next engineering tracks and alpha milestones.

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
curl -fsSL https://dearmachine.to/install.sh | bash
machinemail up --email <your>@gmail.com
```

Or enable end-to-end encryption:

```bash
machinemail up --email <your>@proton.me --pgp ~/<your>-public-key.asc
```

Your computer gets an address.

Email it.

It works.
