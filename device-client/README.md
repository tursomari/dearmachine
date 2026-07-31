# Device Client Go Spike

This module proves the Device Client alpha happy path:

1. Poll an AgentMail inbox through the AgentMail Go SDK.
2. Map AgentMail thread IDs to durable mct-agent session IDs in SQLite.
3. Invoke mct-agent with a preallocated session ID for new threads or
   `--session-id` for existing threads.
4. Inspect `mct-agent session show --json`.
5. Send either the final answer or an AskUser clarification as an AgentMail
   reply.

The spike is intentionally single-threaded. It does not implement retries,
preemption, attachments, sandbox policy, or the production transport
abstraction.

## Run

The module uses the local SDK checkout at `../.state/agentmail-go` through a Go
module replacement. SQLite uses `go-sqlite3`, so source builds require CGO and
a C compiler; the resulting binary has no separate SQLite runtime dependency.

```bash
cd device-client
export AGENTMAIL_API_KEY=am_your_key

go run ./cmd/device-client \
  --inbox-id your-inbox-id \
  --project /path/to/mct/project \
  --db ./device-client.db \
  --pidfile ./run/device-client.pid
```

The daemon runs `mct-agent sync` before polling begins and again immediately
before each `mct-agent run`. Pass `--model your-model-alias` to override the
project's configured default model; when omitted, no model flag is forwarded.

The first poll runs immediately. Later polls start 60 seconds after the prior
poll completes. Override that with `--poll-interval`; use `--once` for a single
poll.

Long-running mode logs successful startup and graceful-shutdown counts to
stderr. Pass `--verbose` to also log the unread-message count for every poll;
idle polls remain silent by default.

Pass `--pidfile /path/to/device-client.pid` when a process supervisor needs a
PID file. Device Client creates missing parent directories after the initial
`mct-agent sync`, writes its current PID before polling, and removes the file
on controlled exit. Omit the flag for the previous foreground-without-pidfile
behavior. This flag does not self-daemonize the process; supervisors that
require a returning start command must provide a background wrapper.

Set `AGENTMAIL_BASE_URL` to point the SDK at a non-production endpoint when
needed.

## Verify

```bash
go test ./...
go build -o /tmp/device-client-spike ./cmd/device-client
```
