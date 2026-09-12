# Forward-to-session-fork LSE

Use the containerized production path in
[`temporary-instance.md`](./temporary-instance.md). This protocol requires two
isolated temporary inboxes and the authorization and cleanup controls in that
reference. Keep all identifiers and message content in its Git-excluded
evidence directory.

Build the OCI image from the exact DearMachine revision under test. Configure
the disposable machtiani project with an OpenRouter model alias whose exact
model ID is `z-ai/glm-5.3-flash` and whose provider parameters contain
`reasoning = { effort = "high" }`. Use the Forge backend through the packaged
Agent Manager. Inspect the effective configuration before sending mail; a
different model or missing high-reasoning parameter makes the evaluation
inconclusive.

Run these scenarios in order:

1. Send an ordinary request and retain its returned `dm1-...` footer and source
   session ID.
2. Start a new mail thread and inline-forward that reply with a new instruction.
   Prove the client sends a confirmation without starting a machtiani turn.
3. Repeat with a provider that groups the forward into the source thread and
   supplies reply ancestry. Prove the normalized HTML footer still triggers the
   confirmation, `Yes!` rebinds later replies to the fork, and the model receives
   only the newly authored instruction.
4. Send an ordinary reply whose quoted history contains a forwarding marker and
   session footer. Prove it continues normally without another confirmation.
5. Reply with invalid authored text. Prove the clearer prompt gives exact Yes,
   No, and Cancel examples and no machtiani turn starts.
6. Reply `Yes!`. Prove the saved instruction runs in a distinct session, the
   session reports the source as its fork ancestor, and neither confirmation
   email nor control reply appears in the model transcript.
7. Repeat with `No,`. Prove no fork is created and the model receives the full
   forwarded headers, body, footer, and attachment metadata.
8. Repeat with `Cancel.`. Prove no model turn or session is created for the
   request.
9. Forward two distinct local session footers. Select the second by number,
   confirm it, and prove the second session is the fork source.
10. Forward an older reply, add a newer source turn before confirming, and prove
   Yes forks the current committed source state.
11. Attach the source reply as `message/rfc822` or `.eml`; prove it follows the
   same confirmation path. Exercise a nested multipart alternative containing
   both text and HTML.
12. Stop the container after the initial prompt, restart it against the same
    isolated state, and complete the decision. Prove one fork and one model
    turn occur.
13. Run once with the default footer and once with `--minimal-footer`. Prove
    the minimal form contains only `session dm1-...` after the answer.

Capture the effective model configuration, sanitized transport events,
confirmation messages, machtiani session metadata, and transcript assertions.
Run `go test ./...` and the repository `nix flake check` against the tested
revision. Tear down only the recorded temporary inboxes, policy entries,
Compose project, runtime root, and disposable machtiani project.

Fixture tests cover Gmail, Apple Mail, Outlook, Thunderbird, Yahoo, Fastmail,
Proton Mail, attached EML, HTML blockquotes, conventional quote prefixes, and
header-cluster fallback. Label synthetic samples as synthetic. Add sanitized
real-client captures separately when Apple Mail or Outlook is available.
