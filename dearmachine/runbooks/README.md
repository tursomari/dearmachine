# DearMachine Client Runbooks

Use these runbooks for human- or agent-guided DearMachine Client operations. They
are separate from [`../TESTING.md`](../TESTING.md), which covers the automated
Go test suite.

## Normal operation

- [`operate-entrypoint-client.md`](./operate-entrypoint-client.md) launches,
  monitors, stops, and restarts the normal installed client. It deliberately
  uses the machine entry point for both email sessions and maintenance.
- [`uninstall-reinstall.md`](./uninstall-reinstall.md) replaces the local
  installation through a recoverable backup-and-verify procedure.

## Isolated live testing

- [`testing/temporary-instance.md`](./testing/temporary-instance.md) defines
  the shared provisioning, isolation, observation, evidence, and teardown
  requirements for all live-test protocols. It is a reference, not a
  standalone test.
- [`testing/disposable-instance.md`](./testing/disposable-instance.md) tests
  the ordinary email lifecycle and local skip/unskip behavior.
- [`testing/concurrent-sessions.md`](./testing/concurrent-sessions.md) compares
  sequential and three-worker processing across simultaneous email threads.
- [`testing/live-backends.md`](./testing/live-backends.md) tests Forge, Codex,
  and ordered backend fallback.
- [`testing/update-sync.md`](./testing/update-sync.md) tests rolling session
  checkpoints and entry-point update sync.

Never point an isolated live-test protocol at the normal inbox, database,
entry point, Agent Manager state, or mct-agent project store.
