# Dear Machine, Architecture

## Purpose

Dear Machine, turns email delivered to a dedicated AgentMail inbox into managed
agent sessions on the device and sends the resulting answer back to the email
thread. It is one machine-level service documented by this entry point.

## Message Lifecycle

1. An authorized sender emails the DearMachine Client's AgentMail inbox.
2. DearMachine Client polls the inbox, orders unread messages, and records each
   message in its SQLite store.
3. Each new email thread maps to a distinct mct-agent session. A later message
   in that thread resumes the same session without replaying quoted history.
4. mct-agent runs in agent-managed mode with the configured backend priority
   and Agent Manager executable supplied by DearMachine Client.
5. Agent Manager health-checks approved backends and supervises the selected
   worker's ticket.
6. DearMachine Client sends the completed answer back to the original thread and
   marks the message processed.

## Configuration

The normal configuration is `~/.dearmachine/config/dearmachine.toml`:

```toml
version = 1
backends = ["codex", "forge"]
```

Backend order is priority order. At least one supported backend is required.
The provisioned mailbox address and other sensitive operational notes belong in
`.scratch/`, while API credentials and other secret values belong in
`.secrets/`. Bootstrap adds both directories to the repository's local
`.git/info/exclude`; neither directory is represented in tracked `.gitignore`.
The DearMachine Client process must load the AgentMail API credential from the local
secret store into its environment.

Important launch settings include:

- `--inbox-id`: dedicated AgentMail inbox.
- `--project`: working directory and mct-agent project used for email sessions;
  it defaults to the DearMachine Client launch directory.
- `--entry-point-repo`: repository used only for entry-point documentation
  sync; it defaults to `~/.dearmachine/entrypoint/main` and does not provide
  entry-point context to a different `--project`.
- `--config`: backend configuration file.
- `--agent-manager`: Agent Manager executable.
- `--mct-agent`: mct-agent executable.
- `--db` and `--pidfile`: local runtime state.
- `--poll-interval`: delay between completed polling cycles.

In a normal installation, both `--project` and `--entry-point-repo` must point
to this initialized entry-point repository. Different paths are reserved for
deliberate development, migration, or isolated testing arrangements.

## Source Pointers

These paths are relative to the separate DearMachine source repository, not
this entry-point repository:

- `cmd/dearmachine/main.go` — DearMachine Client entry point and flags.
- `internal/client/app.go` — polling and processing lifecycle.
- `internal/client/agentmail.go` — AgentMail integration.
- `internal/client/mct.go` — mct-agent invocation and session recovery.
- `internal/client/store.go` — SQLite persistence.
- `internal/backends/catalog.go` — supported backend catalog.
- `cmd/agent-manager/main.go` — Agent Manager command surface.

## Troubleshooting

- No polling: verify the process, PID file, inbox ID, credential, and logs.
- Backend unavailable: inspect approved backend order and run Agent Manager's
  backend health command.
- Message remains pending: inspect the mapped mct session and worker ticket;
  restarting DearMachine Client can resume recoverable work.
- Missing reply: verify the AgentMail thread and the final mct answer rather
  than creating a replacement thread.
