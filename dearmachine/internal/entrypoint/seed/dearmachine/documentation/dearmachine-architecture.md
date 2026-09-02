# Dear Machine, Architecture

## Purpose

Dear Machine, turns email delivered to a dedicated AgentMail inbox into managed
agent sessions on the device and sends the resulting answer back to the email
thread. It is one machine-level service documented by this entry point.

## Message Lifecycle

1. An authorized sender emails the DearMachine Client's AgentMail inbox.
2. DearMachine Client polls the inbox, orders unread messages, and records each
   message in its SQLite store.
3. Each new email thread maps to a distinct machtiani session. A later message
   in that thread resumes the same session without replaying quoted history.
4. machtiani runs in agent-managed mode with the configured backend priority
   and Agent Manager executable supplied by DearMachine Client.
5. Agent Manager health-checks approved backends and supervises the selected
   worker's ticket.
6. DearMachine Client sends the completed answer back to the original thread and
   marks the message processed.

## Configuration

The normal configuration is `~/.dearmachine/config/dearmachine.toml`:

```toml
version = 1
backends = ["codex", "forge", "omp"]
```

Backend order is priority order. At least one supported backend is required.
The machine-global, secret-free launch profile is
`~/.dearmachine/config/runtime.toml`. `up --create` records the effective
project, entry-point, executable, and polling settings there. Later plain
`dearmachine up` launches reload that profile, so they do not depend on the
shell's current directory. All registered pairs share it because one daemon
serves them together; explicitly supplied run flags override it for that
invocation.
The provisioned mailbox address and other sensitive operational notes belong in
`.scratch/`, while API credentials and other secret values belong in
`.secrets/`. Bootstrap adds both directories to the repository's local
`.git/info/exclude`; neither directory is represented in tracked `.gitignore`.
The DearMachine Client process must load the AgentMail API credential from the local
secret store into its environment.

Important launch settings include:

- `up --create --email ...`: register the authorized correspondent.
- `--new-inbox --transport ...` or `--inbox ...`: deliberately provision or
  share/adopt an inbox.
- `--pair`: optionally narrow one invocation by pair email or UUID.
- `--project`: working directory and machtiani project used for email sessions;
  before first pair creation it defaults to the DearMachine Client launch
  directory, then its absolute path is saved for later plain launches.
- `--entry-point-repo`: repository used only for entry-point documentation
  sync; it defaults to `~/.dearmachine/entrypoint/main` and does not provide
  entry-point context to a different `--project`.
- `--config`: backend configuration file.
- `--agent-manager`: Agent Manager executable.
- `--agent-bin`: machtiani executable.
- `up --foreground`: remain attached for systemd and container supervision.
- `--poll-interval`: delay between completed polling cycles.

In a normal installation, both `--project` and `--entry-point-repo` must point
to this initialized entry-point repository. Different paths are reserved for
deliberate development or isolated testing arrangements.

## Source Pointers

These paths are relative to the separate DearMachine source repository, not
this entry-point repository:

- `cmd/dearmachine/main.go` — DearMachine Client entry point and flags.
- `internal/client/app.go` — polling and processing lifecycle.
- `internal/client/agentmail.go` — AgentMail integration.
- `internal/client/agent.go` — machtiani invocation and session recovery.
- `internal/client/store.go` — SQLite persistence.
- `internal/backends/catalog.go` — supported backend catalog.
- `cmd/agent-manager/main.go` — Agent Manager command surface.

## Troubleshooting

- No polling: verify the process, PID file, inbox ID, credential, and logs.
- Backend unavailable: inspect approved backend order and run Agent Manager's
  backend health command.
- Message remains pending: inspect the mapped agent session and worker ticket;
  restarting DearMachine Client can resume recoverable work.
- Missing reply: verify the AgentMail thread and the final agent answer rather
  than creating a replacement thread.
