# Temporary DearMachine Test Instance Reference

This document contains the shared provisioning, isolation, observation, and
teardown procedure for live DearMachine test protocols. It is a reference for
a capable local testing agent, not a standalone test and not an executable
script. The protocol using it supplies the messages, assertions, and pass
criteria.

Current protocols include:

- [`disposable-instance.md`](./disposable-instance.md) for the ordinary email
  lifecycle and local skip/unskip exercise;
- [`concurrent-sessions.md`](./concurrent-sessions.md) for sequential and
  concurrent processing of simultaneous email threads;
- [`live-backends.md`](./live-backends.md) for Forge, Codex, and
  ordered fallback; and
- [`update-sync.md`](./update-sync.md) for rolling
  session checkpoints and internal-README update sync.

## Instructions for the testing agent

Provision a temporary DearMachine instance that cannot consume the normal
inbox or reuse normal runtime state. Do not change product code in response to
the exercise unless the tester separately authorizes diagnosis and repair.
Preserve all pre-existing tracked and untracked workspace state.

Never print, copy, or retain credential contents in commands, logs, prompts,
reports, or shell history. Live identifiers and private message content may be
recorded only in the protocol's Git-excluded evidence directory.

### Required inputs and authorization

Identify or ask the tester for:

- the DearMachine source directory and revision to test;
- the project arrangement required by the protocol;
- the ordered backend list;
- the AgentMail API credential location;
- one or more email accounts authorized to send the live messages;
- the mct-agent executable or source revision; and
- authorization to create and permanently delete a temporary AgentMail inbox
  and to send the protocol's messages; and
- authorization to create and delete exact temporary mail-list entries when an
  active AgentMail allow-list would otherwise reject the test exchange.

Prefer two temporary AgentMail inboxes, one sender and one receiver, when a
fully isolated test requires both delivery and reply proof. A normal AgentMail
inbox may already be polled by the normal Device Client, which could consume a
test reply and invalidate the isolation claim. Confirm the sender is accepted
by the temporary receiver before sending. Adding a sender after a rejected
message does not recover that message. Before the first protocol message,
inspect the applicable organization, pod, and inbox `send`, `receive`, and
`reply` allow-lists. When an active organization-level allow-list governs two
temporary inboxes, the complete exchange may require four run-created entries:
both destination addresses on `send`, the sender on `receive`, and the receiver
on `reply`. Record which entries the run created so teardown never removes a
pre-existing policy entry.

### Isolation contract

Before creating anything, record:

- the DearMachine revision and complete working-tree status;
- running Device Client processes and the inboxes they poll;
- the normal database, PID, configuration, and Agent Manager paths when known;
  and
- the target project's mct identity and session list when an existing project
  is intentionally used.

Apply these boundaries throughout the exercise:

1. Create a unique runtime root with `mktemp -d`, require mode `0700`, and keep
   its exact absolute path. Use a task-specific prefix such as
   `dearmachine-live-test.XXXXXXXX`.
2. Create a dedicated AgentMail receiver whose display name and metadata
   identify the protocol and run. Create a temporary sender too when needed for
   complete isolation. Never point a temporary client at the normal inbox or
   use an inbox polled by the normal client as the isolated sender.
3. Keep freshly built binaries, backend configuration, SQLite database, PID
   file, logs, Agent Manager home, evidence staging, and disposable project
   files beneath the runtime root unless the protocol names a private retained
   evidence directory.
4. Set `DEARMACHINE_HOME` beneath the runtime root. Pass every important path
   explicitly; do not rely on normal Device Client defaults.
5. Use a dedicated mct project when the test must leave no session data in a
   real project. Record its UUID and exact UUID-backed store for teardown.
6. Disable session-driven entry-point sync with `--entry-point-repo ""` unless
   it is the feature under test. A sync test must use a disposable entry-point
   repository for both `--project` and `--entry-point-repo`.
7. Do not use a generic cleanup command, wildcard, `/tmp`, home directory,
   repository root, or `.machtiani` parent as a recursive deletion target.

### Provision the instance

1. Create and secure the runtime root:

   ```bash
   runtime_root=$(mktemp -d -t dearmachine-live-test.XXXXXXXX)
   chmod 0700 "$runtime_root"
   ```

2. Build fresh binaries from the recorded DearMachine revision:

   ```bash
   cd <DearMachine-source>/device-client
   go build -o "$runtime_root/device-client" ./cmd/device-client
   go build -o "$runtime_root/agent-manager" ./cmd/agent-manager
   ```

   Resolve mct-agent to an absolute path and record its version or revision. If
   local mct changes are under test, build that revision beneath the runtime
   root too.

3. Create the project arrangement named by the protocol:

   - For an ordinary isolated project, create a temporary Git repository and
     run `mct-agent init --no-interactive --json` followed by
     `mct-agent project show --json`.
   - For entry-point initialization, run the freshly built Device Client's
     `init` command against an empty directory beneath the runtime root. Pass a
     snapshot directory when the protocol needs the initialization boundaries.

   Record the project root, UUID, store path, initial Git history, session
   list, and internal-README artifact location. Entry-point initialization
   performs two model-backed documentation syncs, so it may remain active for
   several polling intervals. Follow the bootstrap marker and the requested
   before/after snapshots to distinguish progress from a hang. The UUID-backed
   store normally resides under the user's `.machtiani` tree even when the
   project is temporary.

4. Write a version-1 configuration beneath the runtime root, preserving the
   requested backend priority. For example:

   ```toml
   version = 1
   backends = ["forge", "codex"]
   ```

5. Create the temporary inbox or inbox pair through the installed AgentMail
   SDK or API. Use descriptive disposable metadata and verify every returned
   inbox with a read operation. Do not invent or reuse an inbox ID.

> **Warning: MACHTIANI_SESSION_ID collision.** If this test runs from within
> an existing mct-agent session, the inherited `MACHTIANI_SESSION_ID`
> environment variable causes the temporary device-client to collide with
> the parent session lock. The client will fail with an error matching
> `session already active for .../session.lock`. Prevent this by either:
>
> - unsetting `MACHTIANI_SESSION_ID` before launching the device-client:
>   `unset MACHTIANI_SESSION_ID`
> - launching with a clean environment:
>   `env -i PATH="$PATH" HOME="$HOME" device-client ...`
>
> Confirm the variable is absent in the launch environment before
> proceeding.

6. Load the AgentMail credential without displaying it, export the isolated
   `DEARMACHINE_HOME`, and launch the freshly built client in an observable
   foreground terminal or a bounded service owned by the test. The effective
   command must explicitly provide:

   ```text
   device-client
     --inbox-id <temporary-inbox-id>
     --project <protocol-project>
     --config <runtime-root>/device-client.toml
     --agent-manager <runtime-root>/agent-manager
     --mct-agent <absolute-mct-agent-path>
     --db <runtime-root>/device-client.db
     --pidfile <runtime-root>/device-client.pid
     --entry-point-repo <empty-or-disposable-entry-point>
     --entry-point-prompt <protocol-prompt-when-enabled>
     --poll-interval 10s
     --verbose
   ```

   Preserve arguments as an array or carefully quoted invocation. An empty
   entry-point value is intentional for protocols not testing maintenance.
   For unattended runs, prefer a uniquely named transient user service whose
   command loads the credential from its file after the service starts. Never
   interpolate the key into the service command, environment properties,
   process arguments, or retained trajectory. After launch returns, verify the
   client is owned by the recorded service rather than by a wrapper shell stuck
   waiting for a background child. A live client beneath a `bash` process in a
   wait state means orchestration is blocked and must be corrected before mail
   is sent.

7. Confirm startup before sending mail:

   - the PID belongs to the expected temporary binary;
   - its command references only the recorded test paths and inbox;
   - the database and manager state exist beneath the runtime root;
   - no other process polls the temporary inbox; and
   - the first poll has no authentication or configuration error.

### Observe without brittle sleeps

Poll according to observed activity, using seconds while a transition is
expected and backing off when work is legitimately long-running. Use bounded
deadlines and retain the last state on timeout.

Depending on the protocol, inspect:

- Device Client process health and verbose logs;
- pending, processed, skipped, and thread-session rows in the disposable
  SQLite database;
- mct-agent session status, conversation data, and trajectories;
- Agent Manager health and ticket state through its CLI;
- source and artifact Git history, hashes, and status;
- session-sync checkpoint contents and modification times; and
- AgentMail thread membership and replies.

A managed implementation worker may be able to write the project contents but
not Git metadata in its sandbox, commonly surfacing as an inability to create
`.git/index.lock`. If it produced the intended bounded change, keep observing
the parent agent: it may verify the host checkout and delegate the commit to
the next configured backend. Treat that active fallback as progress. Do not
manually commit test output while the agent-guided workflow can still complete
it, because doing so would invalidate the maintenance result.

Record both sender-side and receiver-side thread identifiers. AgentMail may
assign different thread IDs at the two inboxes even though they represent the
same email exchange; the Device Client and mct session map to the receiver-side
thread.

Do not infer success from a reply or a single log message. Verify the protocol's
entire state transition and negative assertions. Avoid same-thread follow-ups
when session ordering is under test because a continuation changes that
session's `updated_at` value.

### Retain evidence safely

Before teardown, create the protocol's concise report and copy only the
evidence needed to support it. For update-sync sensitivity runs, use:

```text
<DearMachine-source>/.scratch/update-sync-evaluations/<UTC-run-timestamp>/
```

The repository's `.git/info/exclude` keeps `.scratch/` unversioned. Record the
source revisions, model/backend selection, safe prompt case IDs, timestamps,
state transitions, checkpoint snapshots, Git hashes, and outcome
classifications. Private session and message identifiers may remain in that
private report, but credentials must never be copied. Verify with `git status`
that no evidence entered version control.

### Teardown

Treat teardown as part of every pass or failure:

1. Stop the temporary Device Client gracefully. Confirm its PID and PID file
   are gone and no child mct-agent, Agent Manager, or backend process remains.
2. Preserve the approved evidence before deleting runtime data.
3. Delete only the exact mail-list entries created for the run, and confirm
   each is absent without disturbing pre-existing entries.
4. Retrieve and verify every temporary inbox's exact ID and disposable
   metadata, delete each through AgentMail, and confirm subsequent lookups
   report them absent. Inbox deletion is permanent.
5. Resolve the disposable project's UUID-backed store with
   `mct-agent project show --json`. Delete that exact store only after proving
   its project root equals the disposable project and no process uses it.
6. Validate that the runtime root is non-empty, owned by the current user, has
   the expected test basename, and is not a symlink. Remove only that exact
   directory.
7. Confirm the normal Device Client and the source repository's tracked and
   untracked baseline remain unchanged, apart from changes explicitly under
   test. Never remove unrelated files.
