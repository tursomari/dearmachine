#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT=${PROJECT_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
MIGRATE=$PROJECT_ROOT/scripts/nix/migrate-state.py
TEST_ROOT=$(mktemp -d /tmp/dearmachine-migration.XXXXXXXX)
trap 'rm -rf "$TEST_ROOT"' EXIT
chmod 0700 "$TEST_ROOT"

make_fixture() {
  local database=$1
  mkdir -p "$(dirname "$database")"
  sqlite3 "$database" <<'SQL'
PRAGMA journal_mode=WAL;
.dbconfig no_ckpt_on_close on
CREATE TABLE thread_sessions (
  thread_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL UNIQUE,
  sequence INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE processed_messages (
  message_id TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL,
  outbound_message_id TEXT NOT NULL DEFAULT '',
  processed_at TEXT NOT NULL
);
CREATE TABLE pending_messages (
  message_id TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL,
  sequence INTEGER NOT NULL,
  state TEXT NOT NULL,
  prompt TEXT NOT NULL DEFAULT '',
  result_kind TEXT NOT NULL DEFAULT '',
  result_text TEXT NOT NULL DEFAULT '',
  checkpoint_session_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(thread_id, sequence)
);
WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x + 1 FROM n WHERE x < 36)
INSERT INTO thread_sessions
SELECT printf('thread-%d', x), printf('session-%d', x), x,
       'active', 'created', 'updated' FROM n;
WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x + 1 FROM n WHERE x < 185)
INSERT INTO processed_messages
SELECT printf('message-%d', x), printf('thread-%d', ((x - 1) % 36) + 1),
       printf('reply-%d', x), 'processed' FROM n;
INSERT INTO pending_messages VALUES
  ('message-pending', 'thread-1', 37, 'running', 'prompt', '', '', '', 'created', 'updated');
SQL
  chmod 0600 "$database" "$database-wal" "$database-shm"
}

dry_root=$TEST_ROOT/dry
dry_source=$dry_root/device-client.db
mkdir -p "$dry_root"
make_fixture "$dry_source"
before_db=$(sha256sum "$dry_source")
before_wal=$(sha256sum "$dry_source-wal")
before_shm=$(stat -c '%i %s %a' "$dry_source-shm")
dry_output=$(DEARMACHINE_MIGRATION_TEST_ROOT=$TEST_ROOT \
  DEARMACHINE_MIGRATION_SCRATCH_PARENT=$TEST_ROOT \
  DEARMACHINE_MIGRATION_SOURCE_DB=$dry_source \
  DEARMACHINE_EXPECT_PENDING=1 \
  DEARMACHINE_EXPECT_PROCESSED=185 \
  DEARMACHINE_EXPECT_THREADS=36 \
  python3 "$MIGRATE" --dry-run)
grep -F 'snapshot counts: pending=1 processed=185 threads=36' <<<"$dry_output"
grep -F 'target counts: pending=1 processed=185 threads=36' <<<"$dry_output"
grep -F 'live database and sidecars were not modified' <<<"$dry_output"
[[ $(sha256sum "$dry_source") == "$before_db" ]]
[[ $(sha256sum "$dry_source-wal") == "$before_wal" ]]
[[ $(stat -c '%i %s %a' "$dry_source-shm") == "$before_shm" ]]

real_root=$TEST_ROOT/real
source_db=$real_root/old/device-client.db
target_db=$real_root/new/dearmachine.db
metadata=$real_root/metadata
make_fixture "$source_db"
real_output=$(DEARMACHINE_MIGRATION_TEST_ROOT=$TEST_ROOT \
  DEARMACHINE_MIGRATION_SOURCE_DB=$source_db \
  DEARMACHINE_MIGRATION_TARGET_DB=$target_db \
  DEARMACHINE_MIGRATION_STATE_DIR=$metadata \
  DEARMACHINE_EXPECT_PENDING=1 \
  DEARMACHINE_EXPECT_PROCESSED=185 \
  DEARMACHINE_EXPECT_THREADS=36 \
  python3 "$MIGRATE" --real)
grep -F 'source counts: pending=1 processed=185 threads=36' <<<"$real_output"
grep -F 'target counts: pending=1 processed=185 threads=36' <<<"$real_output"
[[ ! -e $source_db ]]
[[ -f $target_db ]]
[[ -f $metadata/last.json ]]
rollback_dir=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["rollback_dir"])' "$metadata/last.json")
[[ -f $rollback_dir/device-client.db ]]

DEARMACHINE_MIGRATION_TEST_ROOT=$TEST_ROOT \
  DEARMACHINE_MIGRATION_SOURCE_DB=$source_db \
  DEARMACHINE_MIGRATION_TARGET_DB=$target_db \
  DEARMACHINE_MIGRATION_STATE_DIR=$metadata \
  python3 "$MIGRATE" --rollback >/dev/null
[[ -f $source_db ]]
[[ ! -e $target_db ]]
[[ $(sqlite3 "$source_db" 'SELECT COUNT(*) FROM pending_messages') == 1 ]]
[[ $(sqlite3 "$source_db" 'SELECT COUNT(*) FROM processed_messages') == 185 ]]
[[ $(sqlite3 "$source_db" 'SELECT COUNT(*) FROM thread_sessions') == 36 ]]

mismatch_root=$TEST_ROOT/mismatch
mismatch_source=$mismatch_root/old/device-client.db
mismatch_target=$mismatch_root/new/dearmachine.db
make_fixture "$mismatch_source"
if DEARMACHINE_MIGRATION_TEST_ROOT=$TEST_ROOT \
  DEARMACHINE_MIGRATION_SOURCE_DB=$mismatch_source \
  DEARMACHINE_MIGRATION_TARGET_DB=$mismatch_target \
  DEARMACHINE_MIGRATION_STATE_DIR=$mismatch_root/metadata \
  DEARMACHINE_EXPECT_PROCESSED=999 \
  python3 "$MIGRATE" --real >/dev/null 2>&1; then
  echo 'migration unexpectedly accepted a row-count mismatch' >&2
  exit 1
fi
[[ -f $mismatch_source ]]
[[ ! -e $mismatch_target ]]

printf 'state migration tests passed\n'
