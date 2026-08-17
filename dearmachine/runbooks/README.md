# DearMachine Client Runbooks

Use these runbooks for human- or agent-guided DearMachine Client operations. They
are separate from [`../TESTING.md`](../TESTING.md), which covers the automated
Go test suite.

## Normal operation

- [`native-install.md`](./native-install.md) is the default portable install
  and foreground execution path. It packages DearMachine and Agent Manager
  while using `machtiani` and configured backends from the host `PATH`.
- [`host-install.md`](./host-install.md) is the optional Linux container-stack
  installation, secret-provisioning, upgrade/rollback, and systemd-user path.
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
  requirements for all live-test protocols. Live protocols use its
  containerized production path by default; direct native launches are only
  for explicitly scoped development diagnostics.
- [`testing/disposable-instance.md`](./testing/disposable-instance.md) tests
  the ordinary email lifecycle and local skip/unskip behavior.
- [`testing/openmail-transport.md`](./testing/openmail-transport.md) tests the
  OpenMail constructor, inspect-only default, and isolated poll/reply/ack
  lifecycle with temporary inboxes.
- [`testing/concurrent-sessions.md`](./testing/concurrent-sessions.md) compares
  sequential and three-worker processing across simultaneous email threads.
- [`testing/live-backends.md`](./testing/live-backends.md) tests Forge, Codex,
  and ordered backend fallback.
- [`testing/apple-mail-html-fallback.md`](./testing/apple-mail-html-fallback.md)
  proves the Apple Mail HTML-only body fallback through the live AgentMail API
  and the isolated production Podman path.
- [`testing/update-sync.md`](./testing/update-sync.md) tests rolling session
  checkpoints and entry-point update sync.

Never point an isolated live-test protocol at the normal inbox, database,
entry point, Agent Manager state, or machtiani project store.
