# Apple Mail HTML-only fallback LSE

Use this protocol to prove the regression fix for PM issue `b8992b7`: an
HTML-only Apple Mail message whose AgentMail plain-text fields are empty must
reach the agent as readable plain text. This protocol defines only its messages
and assertions. Follow every provisioning, credential, isolation, observation,
evidence, and teardown requirement in
[`temporary-instance.md`](./temporary-instance.md).

## Required execution path

Run this protocol with the **Default containerized production path** in
`temporary-instance.md`. The native diagnostic exception and development-mode
Compose overlay are not permitted because this LSE claims coverage of the
Nix-built OCI image, production secret boundary, rootless Podman Compose stack,
packaged Agent Manager, mounted Machtiani/backend tools, and DearMachine client.

Use `nix run .#dearmachine-stack -- status`, `health`, `logs`, and `containers`
from the shell holding the run's isolated environment. Do not use
`dearmachine-container-lifecycle`; it inspects the separately installed stack.

The user must authorize creation and permanent deletion of two temporary
AgentMail inboxes, delivery of both messages, and creation/removal of the exact
allow-list entries needed by the run. Journal every created resource before
the first message is sent. Install trap-guarded teardown before provisioning.

## Baseline and setup

1. Record the DearMachine and Machtiani revisions, complete source status, the
   normal client's PID/start time/inbox, normal database metadata, and existing
   Podman containers for the unique LSE project.
2. Create a private evidence directory under the source repository's ignored
   `.scratch/apple-mail-html-fallback/<UTC-timestamp>/` path. Store no secret
   values there. Write the live inbox/allow-list resource journal there before
   relying on `/tmp`; a reboot may erase the runtime before a trap can run.
3. Provision a disposable AgentMail sender and receiver. Verify both by exact
   ID, inspect all governing send/receive/reply allow lists, and add only the
   missing entries required for their exchange. Record whether each entry was
   pre-existing or run-created.
4. Configure exactly one healthy backend in the isolated client home. Mount the
   tested Machtiani executable and backend executable through
   `DEARMACHINE_TOOLS_DIR`; provision only their necessary configuration and
   credentials inside the isolated homes described by `temporary-instance.md`.
5. Pre-synchronize the disposable Machtiani project/store as required by the
   shared production path. Then start the production Podman stack and record
   `dearmachine-stack image`,
   `status`, `health`, and the container ID. Do not send mail until health is
   `healthy` and the first poll has no authentication or configuration error.

## Messages

Send two new messages from the temporary sender to the temporary receiver.
Keep their subjects unique to the run so each creates an independent thread.

### HTML-only Apple Mail case

Send HTML without a plain-text alternative and include an `X-Mailer` header
identifying Apple Mail or iPhone Mail. The HTML must contain:

```html
<html>
  <head>
    <style>.hidden { display: none; }</style>
    <script>document.write("must-not-appear")</script>
  </head>
  <body>
    <p>Apple Mail LSE body.</p>
    <p>Second line &amp; more.</p>
    <ul><li>item one</li><li>item two</li></ul>
    <p><a href="https://example.com/apple-mail-lse">verification link</a></p>
  </body>
</html>
```

Before judging DearMachine, read the received message back through AgentMail
and record field lengths. The live trigger is reproduced only when trimmed
`text`, `extracted_text`, and `preview` are empty while `html` or
`extracted_html` is non-empty. If AgentMail does not produce that signature,
stop and report the reproduction as blocked; do not claim the fallback passed.

### Plain-text control

Send this plain-text body without depending on HTML:

```text
Plain-text Apple Mail LSE control. Preserve this sentence unchanged.
```

Read it back through AgentMail and confirm the `text` field contains that exact
sentence before evaluating the client.

## Required evidence and assertions

Wait with bounded polling rather than a fixed long sleep. For each receiver-side
thread, record the pending-to-processed transition, selected backend health,
mapped and completed Machtiani session, outbound reply, and the user message
persisted in the session conversation. Capture Agent Manager ticket files when
that backend path retains them; the persisted conversation remains the required
body evidence.

The LSE passes only if all of these assertions hold:

1. The HTML-only live trigger signature is present in the AgentMail response.
2. The persisted agent-visible user message contains `Apple Mail LSE body.`,
   `Second line & more.`, `item one`, `item two`, and
   `verification link (https://example.com/apple-mail-lse)`.
3. That user message contains no HTML tags, `document.write`,
   `must-not-appear`, or CSS content.
4. The plain-text control sentence appears unchanged in its persisted
   agent-visible user message.
5. DearMachine processes each inbound message exactly once and emits exactly
   one reply per thread.
6. The container ID and image digest prove the tested client ran inside the
   isolated production Podman stack. Record the source revision and Nix image
   archive/derivation too; a dirty local flake may legitimately label the OCI
   revision as `unknown`.
7. The normal client process and normal database/configuration baselines remain
   unchanged apart from unrelated work observed and identified during the run.

A response email alone is not proof of normalization. The persisted session
conversation is the authoritative agent-visible body evidence.

## Teardown

Preserve the concise evidence report first. Then follow the shared teardown in
`temporary-instance.md`: bring down the exact Compose project, remove its
isolated Podman secret, prove its container list is empty, delete only
run-created allow-list entries and inboxes, verify both inboxes are absent,
unload the isolated image, remove only the validated disposable Machtiani store
and runtime root, and
confirm the normal client and source baselines. Teardown is required after both
passes and failures.
