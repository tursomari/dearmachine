# Connect a custom backend agent

Dear Machine can use an agent that is not in its built-in catalogue when the
agent has a predictable, noninteractive command-line mode. The connection is
described in `~/.dearmachine/config/custom-backends.toml`; no Dear Machine
source change or local Agent Manager build is required.

## Check the agent before configuring it

Start with the agent's installed executable and official help or documentation.
Identify the exact invocation that:

- runs without a terminal interface or interactive questions;
- accepts the delegated request on standard input;
- exits after completing one request; and
- writes either plain final text or Codex-style JSON Lines to standard output.

Run a small, time-bounded, non-mutating prompt in a disposable directory before
writing configuration. Do not guess automation flags or launch the interactive
interface to discover them. Authentication should use the agent's normal
private credential or login mechanism; never put a secret in a command line,
tracked file, or installation transcript.

If the agent needs a wrapper, give the wrapper a stable path and make it part of
the maintained local integration. Do not hard-code temporary directories or
Nix store hashes. A wrapper may translate input or output, but it does not need
to write Dear Machine's completion file when it can return a nonempty native
final answer.

## Register the backend

Create or edit `~/.dearmachine/config/custom-backends.toml`. Each table name is
the backend ID used by Dear Machine:

```toml
[my-agent]
name = "My Agent"
executable = "/home/me/.local/bin/my-agent"
arguments = ["--non-interactive"]
output_format = "plain"
install_help = "Install My Agent and make its executable available."
```

The fields are:

| Field | Required | Meaning |
| --- | --- | --- |
| `executable` | yes | An absolute path, or a command resolvable on `PATH`. |
| `output_format` | no | `plain` (the default) or `json-stream`. |
| `name` | no | A display name; defaults to the backend ID. |
| `arguments` | no | Invocation arguments. `$WRITABLE_DIR` expands to the task's writable directory. |
| `environment` | no | Non-secret environment additions required by the command. |
| `install_help` | no | A short diagnostic shown when the executable is unavailable. |

Use `plain` when standard output is the final response. Use `json-stream` only
when the command emits Codex-style JSON Lines whose final response is an
`item.completed` event containing an `agent_message` item.

Keep credentials out of `custom-backends.toml`. Arrange for the backend to read
them from its normal private files or inherited environment.

Then approve only the intended backend in
`~/.dearmachine/config/dearmachine.toml`:

```toml
version = 1
backends = ["my-agent"]
response_tier = "formatted"
```

Custom backends are configured directly. `dearmachine setup-agents` discovers
and selects built-in backends, so do not run it after registering a custom
backend.

## The request and completion contract

Dear Machine sends the complete ticket envelope to the backend on standard
input while running the command in the selected project directory. The envelope
contains the delegated request and a compatibility header:

```text
# Close-Path: /absolute/path/to/ticket-close.md
```

The preferred completion path is a nonempty final response through the
backend's configured standard-output format. Agent Manager validates and
atomically writes that response to `ticket-close.md` with private permissions
and a bounded size.

For compatibility, a backend may instead write the response directly to the
exact Close-Path. Agent Manager preserves a valid existing close file rather
than overwriting it. The close artifact is control-plane bookkeeping, so a
request such as `read-only`, `reply only`, or `do not modify files` still permits
that one write; those constraints continue to govern the task workspace and
every other path.

A successful process exit without either a nonempty native final response or a
valid close file produces an `incomplete` ticket, not a successful one. A
nonzero exit or invalid output produces a failed ticket.

## Verify the connection

Use the installed `agent-manager`; do not compile another copy from the source
tree. Run health from a disposable repository because the probe briefly asks
the backend to create one temporary file and then removes it:

```bash
probe_dir=$(mktemp -d)
git -C "$probe_dir" init --quiet
(
  cd "$probe_dir"
  agent-manager backend health my-agent
)
```

A successful check ends with `result=ok`. Remove the disposable repository
after inspecting a failure or completing the test.

Next, dispatch a small synthetic ticket from another disposable repository:

```bash
ticket_dir=$(mktemp -d)
git -C "$ticket_dir" init --quiet
printf '%s\n' 'Say hello in French using one short phrase.' >"$ticket_dir/request.md"

agent-manager ticket send \
  --backend my-agent \
  --file "$ticket_dir/request.md" \
  --cwd "$ticket_dir"
```

The command prints a ticket ID. After it reaches a terminal state, inspect the
result with:

```bash
agent-manager ticket view <ticket-id>
```

Do not start a live email flow until both the health probe and synthetic ticket
pass. Restart a running Dear Machine client after changing backend
configuration.

## Guidance for an installation agent

When a person chooses a custom backend, keep the explanation simple: say that
you will check whether the agent can work with Dear Machine and set it up. Do
not describe internal built-in-backend defaults or narrate how the custom path
differs from another backend's procedure.

Work in this order:

1. Check whether the requested agent is already installed.
2. If it is missing, install it only after the person explicitly asks.
3. Determine and live-test its documented noninteractive invocation.
4. Create the smallest stable configuration or wrapper needed for that
   invocation.
5. Run the installed Agent Manager health check and a synthetic ticket.
6. Continue with Dear Machine using that verified backend.

The request to set up a custom backend includes creating a small adapter or
wrapper when one is needed. Do not add another conceptual permission question.
Ask the person only when credentials, external authentication, destructive
changes, or a significant machine-level change genuinely requires their
involvement.

If testing inside an existing Machtiani session, unset
`MACHTIANI_SESSION_ID` for the disposable Dear Machine commands so they do not
collide with the parent session lock.

## More information

- [Dear Machine Client and Agent Manager](../dearmachine/README.md)
- [Native installation](../dearmachine/runbooks/native-install.md)
