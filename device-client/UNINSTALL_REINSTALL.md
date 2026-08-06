# DearMachine Uninstall and Reinstall Runbook

Use this runbook to replace a local DearMachine installation with a fresh one
built from the current HEAD of the source repository's default branch. The
procedure is recoverable: it moves the old installation into a private backup
instead of deleting it, proves the new binaries came from the recorded source
commit, and does not copy the old database, tickets, configuration, or entry
point into the fresh installation.

This runbook may be followed by a human operator or given to a capable local
agent. Do not turn it into a blind cleanup script. Resolve and validate every
path before moving it.

## Scope and important boundaries

The normal local installation consists of:

- `~/.local/bin/device-client`;
- `~/.dearmachine`, including the Agent Manager binary, backend configuration,
  SQLite state, PID file, tickets, backups, and entry-point repository; and
- one UUID-named mct-agent store under `~/.machtiani` for the initialized entry
  point.

The following are outside the uninstall scope and must be preserved:

- the DearMachine source repository;
- `mct-agent` and unrelated UUID stores under `~/.machtiani`;
- installed backend tools such as Codex and Forge;
- project-local private files such as `.secrets` and `.scratch`;
- Git rewrite, quarantine, and deprecated-repository backups; and
- AgentMail inboxes, credentials, and allow-list entries, which are server-side
  resources rather than local installation state.

Deleting local files does not delete an AgentMail inbox. Decide separately
whether a reinstall will reuse the existing inbox or provision a new identity.

## Database disposition requires explicit instruction

An instruction to uninstall DearMachine, reinstall its binaries, update it, or
build the latest revision does **not** authorize resetting or deleting the
Device Client database. Before moving installation state, record exactly one of
these operator decisions:

- **Preserve**: reinstall the software but restore the existing database before
  the first start. This is the default when the operator has not explicitly
  addressed database state.
- **Reset**: keep the old database only in the private uninstall backup and let
  the fresh installation create an empty database. Do this only when the
  operator explicitly instructs the agent to reset the database or requests a
  completely fresh local state.
- **Delete**: permanently remove the old database and its SQLite sidecars. Do
  this only when the operator explicitly instructs the agent to delete the old
  database. A request to reset is not permission to delete the backup.

Resetting the database discards the active installation's pending-message
state, processed-message idempotency records, outbound reply receipts,
thread-to-session mappings, and sequence counters. If the same inbox is reused,
inspect its unread messages before starting the fresh client; otherwise an old
message may be treated as new. Prefer a newly provisioned inbox when the goal is
a completely fresh identity and lifecycle.

The normal database is `~/.dearmachine/state/device-client.db`, but the client
can be launched with a different `--db` path. Inspect the running process,
launcher, or operator configuration and resolve the effective absolute path.
Never assume the default when a custom database may be in use.

## Three-pass acceptance exercise

When validating this runbook itself, exercise the database dispositions in
this order: preserve, reset, then delete. One private safety copy may be kept so
the preserve pass can be repaired and retried. Do not delete that safety copy
until the final delete pass and fresh-install verification both succeed.

For each pass, uninstall and rebuild again rather than reusing binaries from the
prior pass. Record the source commit and installed checksums every time.

1. **Preserve pass:** restore the same database file, prove its checksum,
   integrity, pending count, processed count, and thread mappings match the
   preflight baseline, then complete one controlled empty poll and prove those
   values remain unchanged.
2. **Reset pass:** do not restore the old database. Prove the effective path is
   absent before first start, complete one controlled empty poll, and prove the
   client created a mode-`0600` database with integrity `ok` and no inherited
   pending, processed, or thread records.
3. **Delete pass:** uninstall the reset installation, retain the safety copy
   while building and verifying one final fresh installation, then permanently
   delete every superseded database and sidecar explicitly included in the
   exercise. Prove the final active database is the only remaining Device
   Client database in the approved search scope.

Use an authorized empty inbox or a loopback-only AgentMail-compatible test
endpoint for controlled empty polls. Pass the inbox, project, configuration,
manager, mct-agent, database, PID, and entry-point paths explicitly. A test
endpoint must never accept non-loopback connections or silently replace a live
email exercise when transport behavior is under test.

## What “latest HEAD on the default branch” means

For the current DearMachine repository, the default branch is `master` and the
authoritative reinstall source is the current local `master` HEAD. Record its
commit before building.

Do not automatically run `git fetch`, `git pull`, or copy remote-tracking refs
into a privacy-cleaned repository. A configured remote may still contain older
history that was intentionally excluded from the active repository. If the
operator wants a revision newer than local `master`, stop and first prepare and
audit a clean source repository or clean remote. Remote synchronization is a
separate operation requiring explicit authorization.

## Required inputs

Resolve or ask the operator for:

- the absolute DearMachine source directory;
- an absolute backup parent outside `~/.dearmachine`;
- the ordered backend list;
- the AgentMail credential location, without displaying its contents;
- the inbox ID to reuse, or authorization to create a new inbox; and
- the project directory the Device Client should operate on; and
- the explicit database disposition: preserve, reset, or delete.

Do not record an API key or a private inbox identity in this runbook, a command
transcript, or version-controlled files.

## Phase 1: Preflight

1. Record the source state:

   ```bash
   source_repo="$HOME/projects/DearMachine"
   default_branch=master

   git -C "$source_repo" status --short
   git -C "$source_repo" branch --show-current
   git -C "$source_repo" rev-parse --verify "$default_branch^{commit}"
   git -C "$source_repo" rev-parse --verify "$default_branch^{tree}"
   git -C "$source_repo" for-each-ref --format='%(refname)' \
     refs/heads refs/remotes
   ```

   Stop if the tracked worktree is dirty. Preserve unrelated untracked files.
   The checked-out branch must be `master`, and its commit becomes the recorded
   `source_commit` for the reinstall.

2. Find any running Device Client and its PID file. If a PID file exists, prove
   that the PID is numeric, belongs to the current user, and resolves to the
   expected Device Client executable before sending it a signal. Do not use a
   broad process-kill command.

3. Resolve the entry-point mct-agent store before moving `~/.dearmachine`:

   ```bash
   install_root="$HOME/.dearmachine"
   entry_repo="$install_root/entrypoint/main"
   entry_uuid_file="$entry_repo/.machtiani/project.uuid"

   if test -f "$entry_uuid_file"; then
     entry_uuid=$(tr -d '\r\n' < "$entry_uuid_file")
     entry_store="$HOME/.machtiani/$entry_uuid"
     printf 'entry UUID: %s\nentry store: %s\n' \
       "$entry_uuid" "$entry_store"
   fi
   ```

   Require the UUID to have the canonical UUID form, require the resolved store
   to be exactly one child of `~/.machtiani`, and use `mct-agent project show
   --json` from the entry-point repository to prove the mapping. Never move or
   delete the `.machtiani` parent directory.

4. Record, without printing secrets:

   - whether `~/.local/bin/device-client` exists and its checksum;
   - the checksum of `~/.dearmachine/agent-manager/agent-manager`;
   - the mode and checksum of the SQLite database;
   - backend configuration presence and mode;
   - entry-point HEAD and tracked status; and
   - the number of Agent Manager tickets.

5. Resolve the effective database path and record whether the corresponding
   `-wal` and `-shm` sidecars exist. Run SQLite's integrity check, record the
   database mode and checksum, and stop if integrity is not `ok`. Reconfirm the
   operator's database disposition. If it is reset or delete, quote the exact
   authorization in the execution record before proceeding.

6. Inventory legacy database locations as well as the configured path. Older
   Device Client revisions could place `device-client.db` in the project working
   directory. Search the configured project, DearMachine runtime and backup
   roots, prior test runtimes, and the user's approved home-directory scope for
   Device Client database names and SQLite sidecars. Classify every match by
   schema and path; never infer that an arbitrary `.db` file belongs to
   DearMachine.

## Phase 2: Stop and perform a recoverable uninstall

1. Stop the Device Client gracefully. Wait for shutdown, then confirm that the
   process and its children are gone and the PID file has been removed. If no
   Device Client is running, record that fact and continue.

2. Create a private, dated backup directory outside the installation root:

   ```bash
   uninstall_stamp=$(date +%Y%m%d-%H%M%S)
   uninstall_backup="$HOME/.local/state/dearmachine-uninstall-backups/$uninstall_stamp"
   mkdir -p "$uninstall_backup"
   chmod 0700 "$HOME/.local/state/dearmachine-uninstall-backups"
   chmod 0700 "$uninstall_backup"
   ```

3. Validate each source path. It must be owned by the current user, must not be
   a symlink, and must resolve to the exact expected path. Move existing items
   individually:

   ```bash
   mv "$HOME/.local/bin/device-client" \
     "$uninstall_backup/device-client"
   mv "$HOME/.dearmachine" \
     "$uninstall_backup/dearmachine-home"
   mv "$entry_store" \
     "$uninstall_backup/entrypoint-machtiani-store"
   ```

   Skip an item only when preflight proved it was absent. Never use a wildcard
   or recursively remove a home, project, `.machtiani`, or backup directory.

   Moving `~/.dearmachine` places the normal database and any sidecars under
   `$uninstall_backup/dearmachine-home/state/`; it does not authorize leaving
   them out of the reinstall. If the effective database used a custom path
   outside `~/.dearmachine`, move that exact database and its existing `-wal`
   and `-shm` files into a mode-`0700` database subdirectory in the same backup.

4. Verify the uninstall boundary:

   - no Device Client process remains;
   - `~/.local/bin/device-client` is absent;
   - `~/.dearmachine` is absent;
   - the exact entry-point UUID store is absent from `~/.machtiani`;
   - `mct-agent`, backend tools, source repositories, other Machtiani stores,
     project secrets, and quarantine backups remain; and
   - every moved item exists in the mode-`0700` backup.

Do not permanently delete the backup until the fresh installation and a live
exercise have passed and the operator separately approves deletion.

## Phase 3: Build from the default branch HEAD

1. Return to the source repository and verify the branch without contacting a
   remote:

   ```bash
   source_repo="$HOME/projects/DearMachine"
   default_branch=master

   git -C "$source_repo" switch "$default_branch"
   test -z "$(git -C "$source_repo" status --porcelain=v1)"
   source_commit=$(git -C "$source_repo" rev-parse HEAD)
   source_tree=$(git -C "$source_repo" rev-parse HEAD^{tree})
   printf 'building commit %s, tree %s\n' "$source_commit" "$source_tree"
   ```

   Confirm `source_commit` equals the commit recorded during preflight. If it
   changed, stop and have the operator approve the new revision.

2. Test and build both executables into a private temporary directory:

   ```bash
   build_root=$(mktemp -d -t dearmachine-reinstall.XXXXXXXX)
   chmod 0700 "$build_root"

   cd "$source_repo/device-client"
   go test -count=1 ./...
   go test -race -count=1 ./...
   go vet ./...
   go build -o "$build_root/device-client" ./cmd/device-client
   go build -o "$build_root/agent-manager" ./cmd/agent-manager
   ```

3. Install only the freshly built binaries:

   ```bash
   install -D -m 0755 "$build_root/device-client" \
     "$HOME/.local/bin/device-client"
   install -D -m 0755 "$build_root/agent-manager" \
     "$HOME/.dearmachine/agent-manager/agent-manager"
   chmod 0700 "$HOME/.dearmachine"
   chmod 0700 "$HOME/.dearmachine/agent-manager"
   ```

   Compare the installed checksums with the temporary build checksums before
   deleting the temporary build directory.

## Phase 4: Create fresh configuration and state

1. Configure backends in the operator-approved priority order. For example:

   ```bash
   device-client setup-agents \
     --backend codex \
     --backend forge
   ```

   The command displays detected tools and asks for confirmation before saving.
   Observe and answer that prompt; do not treat the quiet wait as a hang or pipe
   an answer unless the chosen automation makes the response explicit.

   Do not restore the former `device-client.toml` wholesale. Inspect and enter
   the intended configuration again.

2. Initialize a new entry-point repository:

   ```bash
   device-client init \
     --entry-point-repo "$HOME/.dearmachine/entrypoint/main" \
     --mct-agent "$HOME/.local/bin/mct-agent"
   ```

   Initialization is valid only when the entry-point repository is new. It
   must not overwrite an existing repository. Record the new Git HEAD, mct
   project UUID, and UUID-backed store. Initialization performs two mct-agent
   documentation syncs and may produce no terminal output for several minutes.
   During a quiet interval, poll the exact initialization process and its child
   `mct-agent sync --include-docs` process at an appropriate cadence. Continue
   while they are alive and the new project store is advancing; do not launch a
   duplicate initialization.

3. Choose the mail identity deliberately:

   - To reuse the existing inbox, verify the inbox and both directional
     allow-list requirements before starting the client.
   - For a fully fresh identity, create a new AgentMail inbox, configure its
     allow lists, verify it, and retain the old inbox until the new live test
     passes. Inbox deletion is permanent and requires separate authorization.

4. Apply the authorized database disposition before starting the Device Client:

   - **Preserve**: create `~/.dearmachine/state` with mode `0700`, move the old
     database and any existing `-wal` and `-shm` sidecars from the uninstall
     backup back to their exact effective paths, and require the database to be
     mode `0600`. Run the integrity check again. Do not start the client against
     a copied database while another copy could be active.
   - **Reset**: leave the old database and sidecars in the mode-`0700` uninstall
     backup. Confirm the new effective database path does not exist before the
     first start. Do not delete the backup.
   - **Delete**: initially follow the reset procedure and retain the old
     database through fresh-install verification. Permanent deletion happens
     only in Phase 6 after the new installation passes.

5. Load the AgentMail credential from its private location without printing it.
   Start the Device Client with explicit inbox, project, manager, mct-agent,
   database, PID, and entry-point paths. Under the reset or delete disposition,
   the first successful start must create a new database at the effective path
   with mode `0600`.

## Phase 5: Verify the fresh installation

Verify all of the following:

- installed binary checksums match binaries built from `source_commit`;
- the backend configuration contains only the approved ordered list;
- the Agent Manager lists the same backends and each intended backend passes a
  functional health check;
- the entry point has a new project identity and clean tracked state;
- the Device Client starts with a mode-`0600` SQLite database, completes its
  initial sync, writes the expected PID file, and polls without error;
- the old tickets, entry-point repository, and mct store remain in the
  uninstall backup;
- the old database remains in the uninstall backup when reset or deletion was
  authorized, or was restored only when preserve was selected;
- a preserved database has the preflight checksum before first start, or a
  reset database was newly created and contains no inherited message records;
- source, secrets, unrelated Machtiani stores, and Git quarantine backups are
  unchanged.

For an isolated live email-to-reply verification, follow
[`DISPOSABLE_INSTANCE.md`](./DISPOSABLE_INSTANCE.md). Do not use the normal
inbox for a disposable exercise. If validating the newly installed normal
identity, obtain explicit authorization before sending mail or modifying an
AgentMail allow list.

## Phase 6: Optional permanent database deletion

Skip this phase unless the recorded database disposition is **delete** and the
operator explicitly authorized permanent deletion. Reset authorization alone
is insufficient.

After every fresh-install verification has passed and the Device Client is
stopped, resolve the old database inside the uninstall backup. Prove that:

- it is the same exact file recorded during preflight, using its checksum;
- it is a regular file owned by the current user and is not a symlink;
- its parent is the recorded mode-`0700` uninstall backup;
- no process has the database, `-wal`, or `-shm` file open; and
- the fresh database has a different exact path outside the backup.

Compare a fresh filesystem inventory with the preflight inventory. Include the
active installation, the recorded uninstall backup, exercise runtime roots,
and any explicitly approved stale test-runtime paths. Classify every database
before deletion. Do not include unrelated application databases merely because
they use a `.db` suffix.

Delete only the validated old database and its existing SQLite sidecars:

```bash
old_database="$uninstall_backup/dearmachine-home/state/device-client.db"

rm -- "$old_database"
test ! -e "$old_database-wal" || rm -- "$old_database-wal"
test ! -e "$old_database-shm" || rm -- "$old_database-shm"
```

For a custom database, substitute the exact backup path recorded during
preflight. Historical Device Client snapshots may also exist under a
`backups/` directory, an old test runtime, or a legacy project root. Delete each
only after matching it to the preflight inventory and receiving explicit
authorization for that specific cleanup scope. Use exact validated paths, not
a wildcard or a broad recursive command.

Do not recursively delete the complete uninstall backup or other DearMachine or
Machtiani state unless the operator separately requested a completely fresh
runtime and the backup contains only items created or superseded by this
exercise. In that case, validate the exact backup root, prove it is private,
owned by the current user, not a symlink, and outside every active source and
runtime path before removing it. Report every permanent deletion and prove the
final active database is the only Device Client database left in the approved
search scope.

## Rollback

If verification fails, stop the fresh Device Client and preserve its diagnostic
state. Resolve the fresh entry-point UUID and move only that exact UUID store
aside. Move the fresh `~/.dearmachine` and Device Client binary into a separate
failed-install directory, then move the three original items from
`uninstall_backup` back to their exact original locations. Recheck ownership,
modes, checksums, entry-point mapping, database integrity, and process state
before restarting the old installation.

Do not merge fresh and old runtime trees. Roll back by restoring complete,
individually validated items.

## Final report

Report:

- old and new installation paths;
- the recoverable backup path;
- default branch, `source_commit`, and source tree;
- tests and installed-binary checksum comparison;
- whether the inbox was reused or newly provisioned;
- the explicit database disposition, old and new database paths, integrity
  results, and whether any deletion was permanent;
- backend order and health results;
- fresh entry-point identity and database checks;
- live exercise result, if authorized; and
- any retained backup, cloud inbox, or failed-install state requiring later
  cleanup.
