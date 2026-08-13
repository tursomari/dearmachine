# DearMachine Client Runbooks

Use these runbooks for human- or agent-guided DearMachine Client operations. They
are separate from [`../TESTING.md`](../TESTING.md), which covers the automated
Go test suite.

## Normal operation

- [`host-install.md`](./host-install.md) is the single supported installation,
  secret-provisioning, upgrade/rollback, and user-service path.
- [`operate-entrypoint-client.md`](./operate-entrypoint-client.md) operates and
  diagnoses the installed `dearmachine-stack.service`.
- [`migrate-live-state.md`](./migrate-live-state.md) first proves the migration
  against a read-only online-backup snapshot, then describes the separately
  authorized production cutover and rollback.
- [`container-spin-up.md`](./container-spin-up.md) exercises the same packaged
  image, wrapper, and lifecycle in disposable test mode.
- [`uninstall-reinstall.md`](./uninstall-reinstall.md) removes and reinstalls
  the service and immutable releases while preserving state by default.

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
