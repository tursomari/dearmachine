# Uninstall and reinstall the DearMachine user stack

The lifecycle distinguishes immutable release removal from mutable-state or
credential deletion. Default uninstall is recoverable: it stops and disables
`dearmachine-stack.service`, removes its unit, unloads the tagged image, and
removes current/rollback image archives and runtime links. It preserves
`~/.dearmachine`, `~/.machtiani`, the selected project, private client home,
Podman storage, `stack.env`, and the Podman secret.

## Record and uninstall

```bash
nix run .#dearmachine-host-lifecycle -- status || true
current_archive=$(readlink \
  "$HOME/.local/share/dearmachine/image-archive/current")
rollback_archive=$(readlink \
  "$HOME/.local/share/dearmachine/image-archive/rollback" 2>/dev/null || true)
printf 'current=%s rollback=%s\n' "$current_archive" "$rollback_archive"

nix run .#host-uninstall
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
nix run .#host-secrets -- status
nix run .#host-install
nix run .#dearmachine-host-lifecycle -- health
nix run .#dearmachine-host-lifecycle -- logs --tail 100
```

Reinstall reuses preserved state. If a legacy `device-client.db` still needs
renaming, do not start two clients or rename it manually; follow
[`migrate-live-state.md`](./migrate-live-state.md), beginning with dry-run.

Permanent reset or deletion is outside the host-lifecycle command. It requires
a separately authorized, checksum-recorded backup, exact-path review, proof
that no process has the database set open, and a clean replacement poll before
any old copy can be deleted.
