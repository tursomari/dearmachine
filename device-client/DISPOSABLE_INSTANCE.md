# Disposable Device Client Smoke-Test Runbook

This document is a prompt for a capable local agent, not an executable test
script. Give the section below to an agent that can build DearMachine, create
and delete an AgentMail inbox, use an authorized email transport, and inspect
local mct-agent session data.

Use this runbook for one isolated end-to-end email exercise. For the three-part
Forge, Codex, and fallback protocol, use
[`LIVE_BACKEND_TESTING.md`](./LIVE_BACKEND_TESTING.md) and apply this runbook's
provisioning and teardown rules to each disposable instance.

## Prompt to give the testing agent

You are provisioning a disposable Device Client instance for a live smoke test.
Exercise the real email-to-session-to-reply lifecycle without sharing the
normal Device Client's inbox, configuration, database, PID file, Agent Manager
state, or runtime directory.

Do not change product code in response to the exercise unless the tester
separately authorizes diagnosis and repair. Preserve all pre-existing tracked
and untracked workspace state. Never print, copy, or place credential contents
in commands that will be reported, logs, work requests, or the final report.

### Required inputs and authorization

Identify or ask the tester for:

- the DearMachine source directory;
- the project that should receive the test mct-agent session;
- the ordered backend list for this instance;
- the AgentMail API credential location;
- an email account authorized to send the live message;
- the mct-agent executable or source directory to build; and
- authorization to create and later permanently delete a temporary AgentMail
  inbox and to send the smoke-test email.

Confirm that the sender is already accepted by the relevant AgentMail allow
list before sending. Adding a sender after a rejected email does not recover
the earlier message; send a new message only after allowlisting is confirmed.
For two temporary AgentMail inboxes, verify the sender can send to the receiver,
the receiver can receive from the sender, and the receiver can send or reply
back to the sender. A receive or reply entry alone may not satisfy the send
allow list used by the reply endpoint.

### Isolation contract

Before creating anything, record:

- the DearMachine revision and tracked working-tree status;
- any running Device Client processes and the inboxes they poll;
- the normal Device Client database and PID paths, when known; and
- the target project's mct-agent project identity and current session list, if
  the test will use an existing project.

Apply these boundaries throughout the exercise:

1. Create a unique runtime root with `mktemp -d`, require mode `0700`, and
   retain its exact absolute path. Use a task-specific prefix such as
   `dearmachine-device-client-smoke.XXXXXXXX`.
2. Create a dedicated AgentMail inbox for this instance. Give it a display name
   and metadata that clearly identify it as a disposable smoke-test resource,
   and record the inbox ID returned by AgentMail.
3. Never point the disposable client at the normal inbox. A separate database
   is not enough to protect against two clients consuming the same messages.
4. Put the Device Client binary, Agent Manager binary, backend configuration,
   SQLite database, PID file, logs, and Agent Manager home under the runtime
   root.
5. Set `DEARMACHINE_HOME` to a directory under the runtime root so Agent Manager
   tickets and supervision state do not enter the user's normal
   `~/.dearmachine/agent-manager` tree.
6. Pass every important path explicitly. Do not rely on the Device Client's
   normal config, database, manager, entry-point, or PID defaults.
7. Use a dedicated mct-agent project when the test must leave no session data
   in a real project. Using an existing project is allowed only when the tester
   accepts that its UUID-backed store will retain the test session.
8. Disable entry-point session-driven sync unless it is specifically under
   test. An ordinary disposable email smoke test must pass an empty
   `--entry-point-repo` value.

Do not use a generic cleanup command, a wildcard, `/tmp`, a home directory, or
a repository root as a recursive deletion target.

### Provision the runtime

1. Create and secure the runtime root:

   ```bash
   runtime_root=$(mktemp -d -t dearmachine-device-client-smoke.XXXXXXXX)
   chmod 0700 "$runtime_root"
   ```

2. Build fresh binaries from the revisions being tested:

   ```bash
   cd <DearMachine-source>/device-client
   go build -o "$runtime_root/device-client" ./cmd/device-client
   go build -o "$runtime_root/agent-manager" ./cmd/agent-manager
   ```

   Use an explicit absolute mct-agent binary path. If the test covers local mct
   changes, build that revision into the runtime root too. Otherwise record the
   installed binary's version and resolved path.

3. Prefer a disposable clone of the target repository for a general smoke
   test. Initialize it with:

   ```bash
   cd "$test_project"
   "$mct_binary" init --no-interactive --json
   "$mct_binary" project show --json
   ```

   Record the returned UUID and store path. The project marker lives in the
   test project, but the UUID-backed store normally lives under the user's
   `.machtiani` directory and therefore requires separate, exact cleanup.

4. Write a version-1 backend configuration under the runtime root. Preserve the
   requested priority order. For example:

   ```toml
   version = 1
   backends = ["forge", "codex"]
   ```

5. Create the temporary inbox through the installed AgentMail SDK or its
   corresponding API. Use metadata such as `purpose=disposable-smoke-test`.
   Verify the returned inbox with a read operation before launching the client.
   Do not invent or reuse an inbox ID.

6. Load the AgentMail credential without displaying it, export
   `DEARMACHINE_HOME` under the runtime root, and launch the Device Client in a
   foreground terminal suitable for observation. The effective command should
   supply all of these values explicitly:

   ```text
   device-client
     --inbox-id <temporary-inbox-id>
     --project <test-project>
     --config <runtime-root>/device-client.toml
     --agent-manager <runtime-root>/agent-manager
     --mct-agent <absolute-mct-agent-path>
     --db <runtime-root>/device-client.db
     --pidfile <runtime-root>/device-client.pid
     --entry-point-repo ""
     --poll-interval 10s
     --verbose
   ```

   Keep the actual command as an argument array or carefully quoted shell
   invocation. The empty entry-point value is intentional. If entry-point sync
   is the feature under test, instead use a disposable entry-point repository
   for both `--project` and `--entry-point-repo`, and pass its update prompt
   through `--entry-point-prompt`.

7. Confirm startup before sending mail:

   - the PID file identifies the expected disposable binary;
   - the process command references only the recorded runtime and test paths;
   - the database was created under the runtime root;
   - no other process polls the temporary inbox; and
   - the first poll completes without an authentication or configuration
     error.

### Exercise and observe the instance

1. Send a new-thread email from the authorized account to the temporary inbox.
   Use an ordinary, meaningful task scoped to the test project. Do not mention
   the expected backend or describe the task as a backend test. Prefer a short,
   read-only inspection task unless repository modification is itself under
   test.
2. Record the sender-side message and thread identifiers and the AgentMail
   thread identifier.
3. Poll based on observed activity rather than one fixed long sleep. While work
   is active, check every few seconds or minutes as appropriate:
   - Device Client output and process liveness;
   - pending and processed message counts in the disposable SQLite database;
   - the mapped mct-agent session and its status;
   - the shell-agent trajectory;
   - Agent Manager backend health and ticket state through its CLI; and
   - the AgentMail thread for the reply.
4. If the response asks a legitimate clarification, answer in the same email
   thread and confirm that Device Client resumes the same mct-agent session.
   Do not create a replacement thread for a continuation.
5. Use a bounded deadline. On timeout, preserve the last observed state and
   report the stage at which progress stopped.

Do not infer success from a reply alone. For an agent-managed task, verify the
approved backend order, health-check outcomes, selected ticket worker, closed
ticket result, and same-thread reply.

### Optional local skip and unskip exercise

Use this exercise when validating Device Client's local inbox suppression. It
replaces the ordinary exercise above for the same disposable instance.

1. Keep the disposable Device Client stopped and send one ordinary, meaningful
   task to its temporary inbox. Record the exact inbound message and thread IDs.
2. Run `device-client inbox skip --current` with the temporary inbox, database,
   PID file, project, and mct-agent paths supplied explicitly. Confirm its local
   skip list contains exactly the recorded message.
3. Start the disposable client and observe at least two polls. Confirm the
   message remains unread in AgentMail, no mct-agent session or Agent Manager
   ticket is created, no backend starts, and no reply is sent.
4. Stop the client, then run `device-client inbox unskip <message-id>` with the
   same database and PID file. Confirm the local skip list is empty and
   AgentMail remains unchanged at that point.
5. Start the same disposable client again. Monitor the full lifecycle and
   confirm that the unskipped task creates exactly one session, executes once,
   sends exactly one same-thread reply, leaves no pending database row, and is
   marked processed normally.
6. Stop the client and perform the standard teardown below.

Do not perform this proof against the normal inbox. The temporary inbox is what
makes both the negative claim (nothing ran while skipped) and the positive
claim (one run after unskip) conclusive without risking real messages.

### Pass criteria

The disposable exercise passes only when:

- exactly one new email thread is consumed by the disposable client;
- the thread maps to one mct-agent session without replaying quoted history;
- any managed ticket closes successfully with the intended worker selection;
- a substantive reply arrives in the original AgentMail thread;
- the disposable database has no message left pending;
- no duplicate reply or duplicate session was created;
- the normal Device Client process, inbox, database, and Agent Manager state
  were not used or changed; and
- the product repository's tracked state still matches its baseline, unless
  modification was explicitly part of the test.

### Teardown

Treat teardown as part of the test, including after a failure:

1. Stop the disposable Device Client with an interrupt and wait for its
   graceful shutdown message. Escalate only against the exact recorded PID if
   it does not stop.
2. Confirm the PID is gone, its PID file is removed, and it has no child
   mct-agent, Agent Manager, or backend process.
3. Preserve the concise evidence needed for the test report before deleting
   runtime data.
4. Retrieve the temporary inbox and verify its display name, metadata, and
   exact ID. Delete that inbox through AgentMail, then verify that a subsequent
   lookup reports it absent. State clearly that inbox deletion is permanent.
5. If a disposable mct project was created, use `mct-agent project show --json`
   to resolve its UUID-backed store. Delete that exact store only after proving
   that its reported project root equals the disposable project and no process
   uses it. Never delete the `.machtiani` parent directory.
6. Validate that the recorded runtime root is non-empty, owned by the current
   user, has the expected task-specific basename, and is not a symlink. Remove
   only that exact directory.
7. Confirm the normal Device Client state and product repository status still
   match the baseline. Do not remove unrelated untracked files.

### Final report

Report:

- runtime isolation paths and the temporary inbox ID;
- source revisions and configured backend order;
- email, session, health-check, ticket, and reply outcomes;
- whether the test passed, failed, or was inconclusive;
- any lasting project session data the tester elected to retain;
- successful client shutdown, inbox deletion, project-store cleanup, and
  runtime-root cleanup; and
- proof that normal Device Client state and unrelated repository files were
  preserved.
