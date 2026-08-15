# Agent-Guided Live Backend Test Protocol

This document is a prompt for a capable local agent, not an executable test
script. Give the section below to an agent that can inspect the DearMachine
workspace, run local commands, use an authorized email transport, and observe
`machtiani` session data.

The protocol exercises the real DearMachine Client, AgentMail inbox and reply path,
agent-managed shell session, backend health checks, delegated ticket, and
selected worker. It deliberately leaves timing and environment discovery to the
agent so the test does not depend on brittle sleeps, paths, or identifiers.

Use [`temporary-instance.md`](./temporary-instance.md) for the shared
provisioning, isolation, observation, evidence, and teardown procedure. Apply
it independently to each test below so every backend exercise has a dedicated
inbox and fresh runtime state.

## Prompt to give the testing agent

You are conducting a live, human-guided integration test of the DearMachine Client's
ordered backend selection. Run the three tests below in order:

1. Forge as the only approved backend.
2. Codex as the only approved backend.
3. Forge first and Codex second, with Forge deliberately logged out, so the
   live task must fall back to Codex.

Treat each test as an independent end-to-end exercise. Do not change product
code, documentation, prompts, or tests in response to anything you observe.
Do not stage or commit anything. Diagnose and report failures rather than fixing
them. Preserve all pre-existing tracked and untracked workspace state.

### Inputs and authorization

Before acting, identify or ask the tester for:

- the DearMachine project directory;
- the AgentMail inbox ID and API credential location;
- an email account authorized to send to that inbox;
- the Forge provider used on this machine; and
- permission to send the three live test emails and any strictly necessary
  same-thread continuation.

Never print or copy credential contents into the transcript, logs, requests, or
test report. Replace `<provider>` in the human instructions below with the real
Forge provider name. You may use `forge provider list` to discover available
provider names, but if more than one could be the active provider, ask the
tester which one to use.

### Safety and isolation

Before the first test:

1. Record the repository's tracked status and current revision so you can prove
   the test did not alter versioned files.
2. Build the Nix OCI image from the current source. Use its packaged DearMachine
   Client and Agent Manager; do not replace Agent Manager with a host binary.
3. Use temporary, explicitly named paths for the device configuration, SQLite
   database, PID file, logs, and `DEARMACHINE_HOME`. Do not overwrite the
   user's normal DearMachine Client configuration or Agent Manager state.
4. Check whether another DearMachine Client is polling the target inbox. Do not run
   competing pollers. If one is active and you cannot safely conduct the test
   through it, stop and ask the tester how to proceed.
5. Confirm that `forge`, `codex`, and `machtiani` are present. Do not pre-run the
   functional backend probes: each managed email trajectory must perform and
   record the health check for the backend under test. A binary merely being
   present on `PATH` is not evidence that its test passed.

Use bounded polling based on observed state changes rather than fixed long
sleeps. Poll DearMachine Client output, AgentMail state, `machtiani` session state,
and Agent Manager ticket status every few seconds while work is active, backing
off to a longer interval when appropriate. Set a reasonable deadline for each
test and report a timeout with the last observed state.

For every test, start a new email thread. Use an ordinary, meaningful,
read-only repository task that is narrow enough to finish promptly. Do not tell
the managed agent which backend is expected or that the email is testing
backend selection. Suitable tasks ask it to inspect one small behavior and
return a concise explanation with source paths; they must explicitly say not to
modify files.

If the first response merely acknowledges the request or asks for permission to
start, send a concise `Proceed` reply in that same thread and continue observing
the same session. Do not create a replacement thread for that continuation.

For each test, retain evidence of:

- the exact approved backend order passed to the managed session;
- the backend list and each health-check outcome in order;
- the Agent Manager ticket ID and its status transitions;
- the worker recorded for the closed ticket;
- the final email reply and its thread relationship;
- the relevant `machtiani` session and shell-agent trajectory; and
- the absence of tracked repository changes caused by the exercise.

Do not count a test as passed based only on a successful email reply. The
trajectory and closed-ticket metadata must prove which backend was selected.

### Test 1: Forge only

1. Write an isolated DearMachine Client configuration with:

   ```toml
   version = 1
   backends = ["forge"]
   ```

2. Start the isolated container with that configuration, the packaged Agent
   Manager, the test inbox, the project directory, and short verbose
   polling suitable for observation.
3. Send a new-thread email containing a meaningful read-only task.
4. Observe the entire lifecycle through the email reply.
5. Pass only if Forge's functional health check succeeds, the ticket worker is
   `forge`, the ticket closes successfully, and a substantive reply
   arrives in the original email thread.
6. Stop the isolated DearMachine Client cleanly before changing configuration.

### Test 2: Codex only

1. Write an isolated DearMachine Client configuration with:

   ```toml
   version = 1
   backends = ["codex"]
   ```

2. Start the isolated DearMachine Client again with fresh runtime state.
3. Send a different meaningful read-only task in a new email thread.
4. Observe the entire lifecycle through the email reply.
5. Pass only if Codex's functional health check succeeds, the ticket worker is
   `codex`, the ticket closes successfully, and a substantive reply arrives in
   the original email thread.
6. Stop the isolated DearMachine Client cleanly.

### Test 3: Forge-to-Codex fallback

This test has a mandatory human gate. Do not log the tester out yourself.

Before configuring or starting Test 3, stop and send this request to the tester,
substituting the actual provider name:

> Test 3 is ready. Please make Forge unavailable by running
> `forge provider logout <provider>`, then tell me when it is complete. I will
> wait for your confirmation before continuing.

Wait for explicit confirmation. Then:

1. Write an isolated DearMachine Client configuration with:

   ```toml
   version = 1
   backends = ["forge", "codex"]
   ```

2. Start the isolated DearMachine Client again with fresh runtime state.
3. Send a third meaningful read-only task in a new email thread.
4. Observe the ordered health checks, ticket lifecycle, and email reply.
5. Pass only if all of the following are proven:
   - the approved order is Forge followed by Codex;
   - Forge is tried first and its functional health check fails because it
     cannot perform the probe while logged out;
   - Codex is checked only after that failure and passes;
   - the ticket worker is `codex`;
   - the ticket closes successfully; and
   - a substantive reply arrives in the original email thread.
6. Stop the isolated DearMachine Client cleanly.

Whether Test 3 passes, fails, or is interrupted after logout, remind the tester
to restore Forge authentication. Once the test has stopped, send:

> The fallback exercise has stopped. Please restore Forge authentication by
> running `forge provider login <provider>`, then tell me whether it succeeds.

Do not run the login command for the tester. Wait for their response before
calling cleanup complete. If requested, verify authentication without starting
another email exercise.

### Final report and cleanup

After all three tests:

1. Ensure no isolated DearMachine Client process remains running.
2. Confirm no health-check probe file remains in the project.
3. Confirm the repository's tracked state matches the baseline. Do not remove
   unrelated untracked files.
4. Remove only the temporary runtime artifacts you created, when safe.
5. Report each test separately as pass, fail, or inconclusive. Include the
   configured order, observed health-check order and outcomes, selected worker,
   ticket result, email-reply result, and supporting session/ticket identifiers.
6. Clearly distinguish expected fallback from a silent substitution. In Test 3,
   Codex use is a pass only when Forge was visibly attempted first and
   failed its functional probe.
7. Mention whether the tester confirmed that Forge login was restored.
