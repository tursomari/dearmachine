# Uninstall and reinstall the optional container stack

The lifecycle distinguishes immutable release removal from mutable-state or
credential deletion. Default uninstall is recoverable: it stops and disables
`dearmachine-stack.service`, removes its unit, unloads the tagged image, and
removes current/rollback image archives and runtime links. It preserves
`~/.dearmachine`, `~/.machtiani`, the selected project, private client home,
Podman storage, `stack.env`, and the Podman secret.

## Record and uninstall

```bash
nix run .#dearmachine-container-lifecycle -- status || true
current_archive=$(readlink \
  "$HOME/.local/share/dearmachine/image-archive/current")
rollback_archive=$(readlink \
  "$HOME/.local/share/dearmachine/image-archive/rollback" 2>/dev/null || true)
printf 'current=%s rollback=%s\n' "$current_archive" "$rollback_archive"

nix run .#container-uninstall
```

Verify the immutable host boundary is gone and mutable state remains:

```bash
test ! -e "$HOME/.config/systemd/user/dearmachine-stack.service"
test ! -e "$HOME/.local/share/dearmachine/image-archive"
test ! -e "$HOME/.local/share/dearmachine/runtime"
systemctl --user list-unit-files dearmachine-stack.service --no-legend |
  grep -q . && exit 1 || true
test -d "$HOME/.dearmachine/state"
test -f "$HOME/.config/dearmachine/stack.env"
```

Do not use uninstall as permission to remove databases, SQLite sidecars,
inboxes, allow lists, repositories, `.machtiani`, credentials, or backend
state. Preserve, reset, and delete require distinct operator decisions. This
lifecycle intentionally has no implicit purge option.

## Reinstall

Re-run the prerequisite and configuration checks in
[`host-install.md`](./host-install.md), verify the production Podman secret is
present, then install the exact flake-locked release:

```bash
nix flake check
nix run .#container-secrets -- status
nix run .#container-install
nix run .#dearmachine-container-lifecycle -- health
nix run .#dearmachine-container-lifecycle -- logs --tail 100
```

Reinstall reuses the preserved pair registry and pair databases. This release
has no legacy-state import path; begin with `dearmachine up --create` when
starting from an older layout.

Permanent reset or deletion is outside the host-lifecycle command. It requires
a separately authorized, checksum-recorded backup, exact-path review, proof
that no process has the database set open, and a clean replacement poll before
any old copy can be deleted.

## Full reset or destructive reinstall

Do not interpret “reinstall” as permission to reset or delete state. Record
exactly one disposition before proceeding:

- **Preserve:** reinstall immutable releases and reuse the existing database.
- **Reset:** retain the old database in a private backup and let the replacement
  create an empty database.
- **Delete:** follow reset first; permanent deletion requires a second explicit
  authorization after the replacement passes verification.

Before reset or delete, record the source commit, image/runtime links, selected
project and entry-point identity, tool checksums, pair database paths/modes/integrity,
SQLite sidecar inventory, row counts, pending session, and backend ticket
count. Resolve each database from the pair registry rather than guessing paths.

Stop and prove the service, container, PID, and database handles are gone. Then
checkpoint and back up the exact database set:

```bash
database="$HOME/.dearmachine/state/dearmachine.db"
stamp=$(date -u +%Y%m%dT%H%M%SZ)
backup_root="$HOME/.local/state/dearmachine-reinstall-backups/$stamp"

nix run .#dearmachine-container-lifecycle -- stop
test -z "$(nix run .#dearmachine-container-lifecycle -- \
  containers --format '{{.ID}}')"
test ! -e "$HOME/.dearmachine/run/dearmachine.pid"
for member in "$database" "$database-wal" "$database-shm"; do
  test ! -e "$member" || ! fuser "$member"
done

install -d -m 0700 "$backup_root" "$backup_root/original-set"
test "$(sqlite3 "$database" \
  'PRAGMA wal_checkpoint(TRUNCATE); PRAGMA integrity_check;' | tail -n 1)" = ok
sqlite3 "$database" ".backup '$backup_root/dearmachine.db'"
chmod 0600 "$backup_root/dearmachine.db"
sha256sum "$backup_root/dearmachine.db" >"$backup_root/SHA256SUMS"
sqlite3 -readonly "$backup_root/dearmachine.db" '
  SELECT COUNT(*) FROM pending_messages;
  SELECT COUNT(*) FROM processed_messages;
  SELECT COUNT(*) FROM thread_sessions;
' >"$backup_root/counts.txt"
chmod 0600 "$backup_root/SHA256SUMS" "$backup_root/counts.txt"
```

For **preserve**, do not move the active state; use the normal uninstall and
reinstall commands. For an explicitly authorized **reset**, move only the exact
checkpointed database and any remaining sidecars into the private backup, then
install and start the replacement. Never move or delete the whole
`~/.dearmachine`, `~/.machtiani`, project, entry-point store, credentials, or
AgentMail identity as a shortcut.

```bash
# Reset only, after the stop/open-file/checksum checks above.
for member in "$database" "$database-wal" "$database-shm"; do
  if test -e "$member"; then
    test -f "$member" && test ! -L "$member"
    test "$(stat -c %u "$member")" = "$(id -u)"
    mv -- "$member" "$backup_root/original-set/$(basename "$member")"
  fi
done
sha256sum "$backup_root/original-set/dearmachine.db" |
  cut -d' ' -f1 >"$backup_root/original-db.sha256"
chmod 0600 "$backup_root/original-db.sha256"

nix run .#container-uninstall
nix run .#container-install
nix run .#dearmachine-container-lifecycle -- health
```

Verify a reset replacement has a new mode-`0600` database with integrity `ok`,
no inherited pending/processed/thread rows before its first poll, healthy
configured backends, the expected project/session identity, and a successful
disposable-inbox poll/reply. If verification fails, stop it, retain its database
for diagnosis, restore the checksum-verified backup to the original exact path,
and re-check integrity and counts before restarting the former deployment.

```bash
# Failure rollback for an authorized reset.
nix run .#dearmachine-container-lifecycle -- stop
failed_root="$backup_root/failed-replacement"
install -d -m 0700 "$failed_root"
for member in "$database" "$database-wal" "$database-shm"; do
  test ! -e "$member" || mv -- "$member" "$failed_root/$(basename "$member")"
done
for member in \
  "$backup_root/original-set/dearmachine.db" \
  "$backup_root/original-set/dearmachine.db-wal" \
  "$backup_root/original-set/dearmachine.db-shm"; do
  test ! -e "$member" || mv -- "$member" "$(dirname "$database")/$(basename "$member")"
done
test "$(sqlite3 -readonly "$database" 'PRAGMA integrity_check;')" = ok
test "$(sha256sum "$database" | cut -d' ' -f1)" = \
  "$(cat "$backup_root/original-db.sha256")"
```

Permanent deletion is allowed only after all replacement checks pass and the
operator separately identifies the exact backup database by path and checksum.
Delete only that file and its exact sidecars; never recursively delete the
backup root or search-and-delete arbitrary `*.db` files.
