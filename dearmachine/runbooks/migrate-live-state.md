# Migrate live DearMachine state

Migration is explicit and separate from install or upgrade. It changes the
legacy database name `device-client.db` to `dearmachine.db` only after the old
client is stopped, its complete SQLite set is checkpointed and closed, and a
same-filesystem rollback copy exists. Install and upgrade never migrate or
delete state.

## Mandatory read-only rehearsal

Run this first from the repository root while the normal client remains
running:

```bash
nix run .#container-migrate -- --dry-run
```

Dry-run opens only
`/home/david/.dearmachine/state/device-client.db`, using a SQLite URI with
`mode=ro`, and copies a consistent view through SQLite's online backup API. It
does not stop a process, checkpoint the live database, rename a live file, or
operate directly on its `-wal`/`-shm` sidecars. The full checkpoint, rollback,
same-filesystem rename, integrity, and row-count flow then runs against the
snapshot beneath a private `/tmp/dearmachine-migrate-dry-run.*` directory,
which is removed after success.

Require all of these output lines:

- `PRAGMA integrity_check: ok`;
- identical snapshot/source/target counts for `pending_messages`,
  `processed_messages`, and `thread_sessions`; and
- `dry-run migration exercise passed`.

The observed production baseline when this migration was designed was 1
pending message, 185 processed messages, and 36 thread mappings. Pin those
expected values for that exact authorized snapshot when appropriate:

```bash
DEARMACHINE_EXPECT_PENDING=1 \
DEARMACHINE_EXPECT_PROCESSED=185 \
DEARMACHINE_EXPECT_THREADS=36 \
  nix run .#container-migrate -- --dry-run
```

The tool always reports the three counts and fails if source and migrated
counts differ. The optional expected values also make it fail if the snapshot
does not match the operator's recorded preflight baseline.

If the legacy path is absent, stop: do not substitute another live database
path merely because one exists. Resolve the deployment history and obtain
fresh authorization.

## Production cutover (separate authorization required)

Production cutover was not part of the Stage 3 validation run. Before a future
authorized cutover, require:

- the host service installed and configured but not polling the same inbox;
- `~/.dearmachine/state/device-client.db` present and
  `~/.dearmachine/state/dearmachine.db` absent, including target sidecars;
- the dry-run above passing against the then-current live database;
- enough time for a pending recovery and one clean AgentMail poll; and
- an operator-approved rollback window.

Then run exactly:

```bash
nix run .#container-migrate -- --real
```

The lifecycle stops both `dearmachine.service` and
`dearmachine-stack.service`, proves neither is active, rejects a live or stale
PID file, and uses `fuser` to reject any open database or sidecar. The helper
checkpoints WAL, closes SQLite, records integrity and counts, writes private
checksum metadata under `~/.local/state/dearmachine-migration`, copies the
closed set there for rollback, and renames the original set on the same
filesystem. It starts the stack only after target integrity and counts match.

The rollback directory remains after health succeeds. The migration is marked
clean only after the verbose container log contains an actual AgentMail poll;
a merely running PID is insufficient. If the start, metadata update, or poll
proof fails, the lifecycle stops the stack and retains both the migrated state
and rollback set for inspection.

To restore the checkpointed legacy set:

```bash
nix run .#dearmachine-container-lifecycle -- stop
nix run .#container-migrate -- --rollback
```

Rollback checkpoints the current migrated target and verifies its integrity.
It then keeps a checksum-recorded `post-migration*` copy and moves that current
database set back to the legacy name. This preserves pending recovery and any
messages processed after cutover instead of reverting to stale baseline row
counts. The original pre-migration backup also remains for forensic recovery.
Rollback does not restart either client; start only the explicitly selected
deployment after reviewing the restored counts.
