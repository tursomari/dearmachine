# Runbook: Configure DearMachine Device Client

## Purpose

Change Device Client's approved backend order and verify that the service can
use the resulting configuration.

## Guardrails

- Plan a Device Client restart before changing its active configuration.
- Verify each backend executable before adding it.
- Keep at least one supported backend.
- Never place the AgentMail credential in a versioned file or command output.

## Steps

1. Read `~/.dearmachine/config/device-client.toml`.
2. Check the desired executables, such as `codex` and `forge`, on `PATH`.
3. Set `backends` in the intended priority order:

   ```toml
   version = 1
   backends = ["codex", "forge"]
   ```

4. Stop the running Device Client gracefully.
5. Restart it with its normal explicit inbox, project, database, PID, manager,
   and mct-agent paths.
6. Confirm startup and at least one successful poll.
7. For a live lifecycle check, follow the disposable-instance runbook in the
   DearMachine source repository rather than sharing the normal inbox or state.

## Verification

- Agent Manager lists backends in the configured order.
- The first healthy backend can complete its health probe.
- Device Client logs successful polling without configuration errors.
- A separately authorized disposable exercise can process and reply to one
  ordinary email thread without touching normal runtime state.

## Update This Runbook When

- supported backends or configuration format changes;
- launch flags or default paths change; or
- backend health and fallback behavior changes.
