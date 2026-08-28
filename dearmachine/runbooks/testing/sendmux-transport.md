# Sendmux Transport Live-Test Runbook

This protocol verifies the selectable Sendmux adapter without sharing the
normal Dear Machine database, PID file, project, Agent Manager state, or
mailbox transport. It proves one complete two-turn conversation:

`external send -> Dear Machine reply -> external reply -> Dear Machine reply`

Use the isolation requirements in
[`temporary-instance.md`](./temporary-instance.md). This is a native transport
diagnostic unless the test explicitly follows the containerized production
path from that shared runbook.

## Safety boundaries

- Use a mailbox-scoped Sendmux key. Keep it in a user-owned mode-`0600` file;
  never print it, put it in a command argument, or retain authorization headers
  in evidence.
- Keep the Sendmux mailbox and external correspondent addresses in a separate
  mode-`0600` operator environment file. Do not commit them, add them to test
  fixtures, or quote them in test reports.
- The external correspondent may be any specifically authorized mailbox. This
  one-off operational choice must not introduce provider-, organization-, or
  account-specific behavior into Dear Machine or this procedure.
- Configure the common `DEARMACHINE_ALLOW` paired-address list using only
  email addresses. A message addressed to, copied to, or sharing a thread with
  anyone else must remain invisible to the adapter.
- Sendmux is inspect-only unless both `DEARMACHINE_LIVE_SENDMUX=1` and
  `DEARMACHINE_LIVE_SENDMUX_APPLY=1` are set. Leave both unset during the
  initial credential and poll checks.
- Do not stop, reconfigure, or point this test at the normal Dear Machine
  service. Use a unique runtime root and database.

## Private operator configuration

Create an untracked file outside the repository, owned by the operator and
mode `0600`, with values appropriate to this run:

```bash
SENDMUX_MAILBOX_API_KEY_FILE=/absolute/path/to/sendmux-api-key
SENDMUX_SEND_API_KEY_FILE=/absolute/path/to/sendmux-send-api-key
SENDMUX_QSE_INBOX_ID=<mbx-mailbox-id>
SENDMUX_QSE_INBOX_ADDRESS=<exact-mailbox-address>
SENDMUX_QSE_CORRESPONDENT=<exact-external-address>
```

Load it without echoing its contents, then set the seam allow list:

```bash
set -a
. /absolute/path/to/sendmux-qse.env
set +a
export DEARMACHINE_ALLOW="$SENDMUX_QSE_CORRESPONDENT,$SENDMUX_QSE_INBOX_ADDRESS"
```

Never put the `mbx_...` mailbox ID in `DEARMACHINE_ALLOW`. The allow list
accepts email addresses only; using the ID fails with
`allow list must be a comma-separated email address list: mail: missing '@' or angle-addr`.

Before continuing, require that the credential file and operator environment
file are regular files owned by the current user with no group or other bits.

The operator environment file remains the canonical source for
`SENDMUX_QSE_INBOX_ID` and `SENDMUX_QSE_INBOX_ADDRESS`. Before the first poll,
cross-check both values through the mailbox-scoped key's self endpoint using
the Go mailbox SDK's `MailboxGetMe`: the granted mailbox ID and email must
agree with the configured pair. Do this without printing the key or any
addresses.

## Isolated inspect

1. Create a runtime root with `mktemp -d` and require mode `0700`. Put the
   disposable Git project, device configuration, SQLite database, PID file,
   logs, Agent Manager home, and Machtiani state below it. Disable entry-point
   maintenance with `--entry-point-repo ""`.
2. Build `dearmachine` and `agent-manager` from the revision under test. Use a
   dedicated trivial Git project and an isolated Machtiani session root.
3. With both mutation gates unset, verify the mailbox credential through the
   official mailbox API and run an empty poll:

   ```bash
   "$QSE_ROOT/dearmachine" \
     --transport sendmux \
     --inbox-id "$SENDMUX_QSE_INBOX_ID" \
     --once \
     --verbose \
     --db "$QSE_ROOT/inspect.db" \
     --pidfile "$QSE_ROOT/inspect.pid" \
     --project "$QSE_ROOT/project" \
     --config "$QSE_ROOT/dearmachine.toml" \
     --agent-manager "$QSE_ROOT/agent-manager" \
     --agent-bin '<absolute-machtiani-path>' \
     --entry-point-repo ""
   ```

The inspect step passes only if the key resolves the intended active mailbox,
the adapter polls successfully, and no provider state changes. An
authentication failure is a credential blocker, not evidence about delivery.

## Send, reply, send, reply

1. With the client stopped, create the fresh live database by locally skipping
   the exact current eligible snapshot with a run-specific reason:

   ```bash
   "$QSE_ROOT/dearmachine" inbox skip \
     --current \
     --transport sendmux \
     --inbox-id "$SENDMUX_QSE_INBOX_ID" \
     --db "$QSE_ROOT/live.db" \
     --pidfile "$QSE_ROOT/live.pid" \
     --project "$QSE_ROOT/project" \
     --agent-bin '<absolute-machtiani-path>' \
     --reason "sendmux-qse-<run-id>-baseline"
   ```

   Record the skipped message IDs privately as the baseline. Do not reuse an
   inspect database or a database from another run.
2. From the external correspondent, send a new message to the Sendmux mailbox
   with a unique subject and a small read-only task scoped to the disposable
   project. Record identifiers privately; do not copy addresses or message
   contents into a committed artifact.
3. Confirm through the Sendmux mailbox API that exactly that run-created
   inbound message is unread, has an RFC Message-ID, and is in a thread
   containing only the two authorized participants.
4. Immediately before every client start in this procedure, re-poll through
   the mailbox API. Abort if any eligible unread message ID is neither a
   recorded baseline ID nor a message created by this run. Then enable both
   mutation gates and run the isolated client against the live database:

   ```bash
   export DEARMACHINE_LIVE_SENDMUX=1
   export DEARMACHINE_LIVE_SENDMUX_APPLY=1
   ```

5. Wait for the first Dear Machine response. Reply to that response in the
   existing conversation and make the follow-up meaningfully depend on the
   first turn. For an automated follow-up, set `In-Reply-To` to the prior Dear
   Machine response's RFC Message-ID, not a Gmail resource ID, Sendmux ID, or
   thread ID. Extend `References` by appending that Message-ID exactly once to
   the prior `References`; supply the provider thread ID in the send request;
   use a `Re:` subject; and quote the full prior response in the body, including
   the Dear Machine footer. This exercises both the ancestry and footer-
   fallback paths on the receiving side.
6. Wait for the second run-created inbound message to become unread, repeat the
   pre-start re-poll guard, and rerun the same isolated client with the same
   live database and project. Identify the follow-up by its authenticated
   sender and reply ancestry or stable conversation reference; do not require
   exact subject equality because mail clients commonly add or normalize a
   `Re:` prefix.
7. Verify independently that:

   - exactly the two run-created, non-skipped inbound messages were claimed and
     processed;
   - both turns map to one Machtiani session and the second advances its
     sequence rather than creating a new session;
   - both Dear Machine replies contain the same valid stable conversation
     reference;
   - each outbound send has a unique durable idempotency key;
   - each outbound message has a provider-generated RFC Message-ID, and the
     follow-up references the answer it replied to;
   - the second prompt contains the new human contribution but not the footer
     or quoted history as new user text;
   - Sendmux reports both run-created inbound messages seen after successful
     processing;
   - no baseline ID caused a session, Agent Manager ticket, or reply;
   - the isolated database has exactly two processed run-created messages and
     no pending rows; and
   - a repeated poll creates neither a third agent turn nor a duplicate reply.

Sendmux's HTTP send API rejects caller-supplied RFC `In-Reply-To` and
`References` headers and has no distinct reply operation. A Dear Machine
Sendmux response may therefore start a new provider-local thread. Conversation
continuity is judged on the receiver side by the Gmail thread ID together with
the stable Dear Machine conversation reference in the reply, even when the
Sendmux provider-local thread changes.

## Teardown

Stop only the isolated client and confirm its PID file is gone. Preserve the
minimum redacted evidence needed for the result, then remove the exact temporary
runtime root using the shared runbook's teardown procedure. Do not delete the
Sendmux mailbox or alter an external correspondent unless those resources were
created by this run and deletion was separately authorized. Confirm the normal
Dear Machine service still has its original command, transport, database, and
PID file.
