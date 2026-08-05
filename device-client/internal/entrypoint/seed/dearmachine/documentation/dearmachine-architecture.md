# DearMachine Architecture

## Purpose

DearMachine turns email delivered to a dedicated AgentMail inbox into managed
agent sessions on the device and sends the resulting answer back to the email
thread. It is one machine-level service documented by this entry point.

## Message Lifecycle

1. An authorized sender emails the Device Client's AgentMail inbox.
2. Device Client polls the inbox, orders unread messages, and records each
   message in its SQLite store.
3. Each new email thread maps to a distinct mct-agent session. A later message
   in that thread resumes the same session without replaying quoted history.
4. mct-agent runs in agent-managed mode with the configured backend priority
   and Agent Manager executable supplied by Device Client.
5. Agent Manager health-checks approved backends and supervises the selected
   worker's ticket.
6. Device Client sends the completed answer back to the original thread and
   marks the message processed.

## Configuration

The normal configuration is `~/.dearmachine/config/device-client.toml`:

```toml
version = 1
backends = ["codex", "forgecode"]
```

Backend order is priority order. At least one supported backend is required.
The AgentMail API credential must be present in the Device Client environment.

Important launch settings include:

- `--inbox-id`: dedicated AgentMail inbox.
- `--project`: project used for mct-agent sessions.
- `--config`: backend configuration file.
- `--agent-manager`: Agent Manager executable.
- `--mct-agent`: mct-agent executable.
- `--db` and `--pidfile`: local runtime state.
- `--poll-interval`: delay between completed polling cycles.

## Source Pointers

These paths are relative to the separate DearMachine source repository, not
this entry-point repository:

- `cmd/device-client/main.go` — Device Client entry point and flags.
- `internal/deviceclient/app.go` — polling and processing lifecycle.
- `internal/deviceclient/agentmail.go` — AgentMail integration.
- `internal/deviceclient/mct.go` — mct-agent invocation and session recovery.
- `internal/deviceclient/store.go` — SQLite persistence.
- `internal/backends/catalog.go` — supported backend catalog.
- `cmd/agent-manager/main.go` — Agent Manager command surface.

## Troubleshooting

- No polling: verify the process, PID file, inbox ID, credential, and logs.
- Backend unavailable: inspect approved backend order and run Agent Manager's
  backend health command.
- Message remains pending: inspect the mapped mct session and worker ticket;
  restarting Device Client can resume recoverable work.
- Missing reply: verify the AgentMail thread and the final mct answer rather
  than creating a replacement thread.
