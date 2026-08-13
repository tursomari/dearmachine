#!/usr/bin/env python3
"""WAL-safe legacy DearMachine state migration.

Dry-run is intentionally special: it opens the configured live source with a
read-only SQLite URI, copies it with Connection.backup(), and performs every
write/checkpoint/rename operation only on the snapshot beneath /tmp.
"""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import sys
import tempfile
import time
from typing import Any


SIDECAR_SUFFIXES = ("-wal", "-shm")
COUNT_TABLES = (
    ("pending", "pending_messages"),
    ("processed", "processed_messages"),
    ("threads", "thread_sessions"),
)


class MigrationError(RuntimeError):
    pass


def fail(message: str) -> None:
    raise MigrationError(message)


def path_from_env(name: str, default: Path) -> Path:
    return Path(os.environ.get(name, str(default))).expanduser().resolve()


def paths() -> tuple[Path, Path, Path]:
    home = Path.home()
    state_home = Path(os.environ.get("XDG_STATE_HOME", home / ".local/state"))
    source = path_from_env(
        "DEARMACHINE_MIGRATION_SOURCE_DB",
        home / ".dearmachine/state/device-client.db",
    )
    target = path_from_env(
        "DEARMACHINE_MIGRATION_TARGET_DB",
        home / ".dearmachine/state/dearmachine.db",
    )
    metadata = path_from_env(
        "DEARMACHINE_MIGRATION_STATE_DIR",
        state_home / "dearmachine-migration",
    )
    return source, target, metadata


def ensure_private_directory(path: Path) -> None:
    path.mkdir(parents=True, exist_ok=True, mode=0o700)
    if path.is_symlink() or not path.is_dir():
        fail(f"not a private directory: {path}")
    if path.stat().st_uid != os.getuid():
        fail(f"directory is not owned by the current user: {path}")
    path.chmod(0o700)


def ensure_source(path: Path) -> None:
    if path.name != "device-client.db":
        fail(f"legacy source must be named device-client.db: {path}")
    if path.is_symlink() or not path.is_file():
        fail(f"legacy database is not a regular file: {path}")
    if path.stat().st_uid != os.getuid():
        fail(f"legacy database is not owned by the current user: {path}")


def ensure_target_shape(path: Path) -> None:
    if path.name != "dearmachine.db":
        fail(f"migration target must be named dearmachine.db: {path}")


def readonly_connection(path: Path) -> sqlite3.Connection:
    uri = f"{path.as_uri()}?mode=ro"
    connection = sqlite3.connect(uri, uri=True, timeout=5)
    connection.execute("PRAGMA query_only=ON")
    connection.execute("PRAGMA busy_timeout=5000")
    return connection


def online_backup_readonly(source: Path, destination: Path) -> None:
    """Use only SQLite's online backup API against the live read-only source."""
    ensure_source(source)
    destination.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if destination.exists():
        fail(f"snapshot destination already exists: {destination}")
    source_connection = readonly_connection(source)
    try:
        destination_connection = sqlite3.connect(destination)
        try:
            source_connection.backup(destination_connection, pages=256, sleep=0.05)
        finally:
            destination_connection.close()
    finally:
        source_connection.close()
    destination.chmod(0o600)


def integrity_and_counts(path: Path) -> dict[str, int]:
    connection = readonly_connection(path)
    try:
        integrity_rows = [row[0] for row in connection.execute("PRAGMA integrity_check")]
        if integrity_rows != ["ok"]:
            fail(f"PRAGMA integrity_check failed for {path}: {integrity_rows}")
        counts: dict[str, int] = {}
        for label, table in COUNT_TABLES:
            try:
                row = connection.execute(f'SELECT COUNT(*) FROM "{table}"').fetchone()
            except sqlite3.DatabaseError as error:
                fail(f"required table {table} is unavailable in {path}: {error}")
            if row is None:
                fail(f"could not count {table} in {path}")
            counts[label] = int(row[0])
        return counts
    finally:
        connection.close()


def print_counts(label: str, counts: dict[str, int]) -> None:
    print(
        f"{label} counts: pending={counts['pending']} "
        f"processed={counts['processed']} threads={counts['threads']}"
    )


def check_expected(counts: dict[str, int]) -> None:
    names = {
        "pending": "DEARMACHINE_EXPECT_PENDING",
        "processed": "DEARMACHINE_EXPECT_PROCESSED",
        "threads": "DEARMACHINE_EXPECT_THREADS",
    }
    for label, environment_name in names.items():
        raw = os.environ.get(environment_name)
        if raw is None:
            continue
        try:
            expected = int(raw)
        except ValueError:
            fail(f"{environment_name} must be an integer")
        if counts[label] != expected:
            fail(
                f"count mismatch for {label}: observed {counts[label]}, "
                f"expected {expected}"
            )


def checkpoint_and_close(path: Path) -> tuple[dict[str, int], list[Path]]:
    inventory = [path]
    inventory.extend(
        candidate for suffix in SIDECAR_SUFFIXES if (candidate := Path(f"{path}{suffix}")).exists()
    )
    print("checkpoint inventory: " + ", ".join(item.name for item in inventory))
    connection = sqlite3.connect(path, timeout=5)
    try:
        connection.execute("PRAGMA busy_timeout=5000")
        row = connection.execute("PRAGMA wal_checkpoint(TRUNCATE)").fetchone()
        if row is not None and int(row[0]) != 0:
            fail(f"WAL checkpoint remained busy for {path}: {row}")
    finally:
        connection.close()
    counts = integrity_and_counts(path)
    check_expected(counts)
    closed_inventory = [path]
    closed_inventory.extend(
        candidate for suffix in SIDECAR_SUFFIXES if (candidate := Path(f"{path}{suffix}")).exists()
    )
    return counts, closed_inventory


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def atomic_json(path: Path, value: dict[str, Any]) -> None:
    temporary = path.with_name(f".{path.name}.new")
    with temporary.open("w", encoding="utf-8") as handle:
        json.dump(value, handle, indent=2, sort_keys=True)
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
    temporary.chmod(0o600)
    os.replace(temporary, path)


def sidecar_destination(source_file: Path, source: Path, target: Path) -> Path:
    suffix = source_file.name.removeprefix(source.name)
    return Path(f"{target}{suffix}")


def restore_failed_migration(
    source: Path,
    target: Path,
    backup_dir: Path,
    source_files: list[Path],
) -> None:
    for suffix in ("", *SIDECAR_SUFFIXES):
        target_file = Path(f"{target}{suffix}")
        if target_file.exists():
            failed_file = backup_dir / f"failed-target{suffix}"
            os.replace(target_file, failed_file)
    for original in source_files:
        backup = backup_dir / original.name
        if backup.exists() and not original.exists():
            shutil.copy2(backup, original)
            original.chmod(0o600)


def migrate_files(source: Path, target: Path, metadata_dir: Path) -> dict[str, Any]:
    ensure_source(source)
    ensure_target_shape(target)
    if source == target:
        fail("source and target database paths are identical")
    if target.exists() or Path(f"{target}-wal").exists() or Path(f"{target}-shm").exists():
        fail(f"migration target or a target sidecar already exists: {target}")

    ensure_private_directory(target.parent)
    ensure_private_directory(metadata_dir)
    if source.stat().st_dev != target.parent.stat().st_dev:
        fail("source and target are not on the same filesystem")
    if source.stat().st_dev != metadata_dir.stat().st_dev:
        fail("migration rollback directory is not on the source filesystem")

    before_counts, source_files = checkpoint_and_close(source)
    print_counts("source", before_counts)
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    backup_dir = metadata_dir / f"rollback-{stamp}-{os.getpid()}"
    ensure_private_directory(backup_dir)

    backups: list[dict[str, str]] = []
    for original in source_files:
        if original.is_symlink() or not original.is_file():
            fail(f"database set member is not a regular file: {original}")
        if original.stat().st_uid != os.getuid():
            fail(f"database set member is not owned by the current user: {original}")
        backup = backup_dir / original.name
        shutil.copy2(original, backup)
        backup.chmod(0o600)
        backups.append(
            {
                "original": str(original),
                "backup": str(backup),
                "sha256": sha256(backup),
            }
        )

    moved: list[dict[str, str]] = []
    try:
        for original in source_files:
            destination = sidecar_destination(original, source, target)
            os.replace(original, destination)
            destination.chmod(0o600)
            moved.append({"source": str(original), "target": str(destination)})
        after_counts = integrity_and_counts(target)
        print_counts("target", after_counts)
        if after_counts != before_counts:
            fail(f"row-count mismatch after migration: source={before_counts}, target={after_counts}")
    except Exception:
        restore_failed_migration(source, target, backup_dir, source_files)
        raise

    manifest: dict[str, Any] = {
        "version": 1,
        "status": "migrated-awaiting-clean-poll",
        "created_at": stamp,
        "source": str(source),
        "target": str(target),
        "rollback_dir": str(backup_dir),
        "counts": before_counts,
        "backups": backups,
        "moved": moved,
    }
    atomic_json(backup_dir / "manifest.json", manifest)
    atomic_json(metadata_dir / "last.json", manifest)
    print(f"PRAGMA integrity_check: ok")
    print(f"rollback retained: {backup_dir}")
    return manifest


def require_stop_proof(source: Path, target: Path) -> None:
    if os.environ.get("DEARMACHINE_MIGRATION_STOP_PROOF") == "1":
        return
    test_root_raw = os.environ.get("DEARMACHINE_MIGRATION_TEST_ROOT")
    if not test_root_raw:
        fail("real migration requires lifecycle stop/open-file proof")
    test_root = Path(test_root_raw).resolve()
    if test_root.parent != Path("/tmp") or test_root.is_symlink():
        fail("migration test root must be one direct, non-symlink child of /tmp")
    for candidate in (source, target):
        if test_root not in candidate.parents:
            fail(f"test migration path escapes the scratch root: {candidate}")


def dry_run(source: Path) -> None:
    scratch_parent = Path(os.environ.get("DEARMACHINE_MIGRATION_SCRATCH_PARENT", "/tmp")).resolve()
    if scratch_parent != Path("/tmp"):
        test_root_raw = os.environ.get("DEARMACHINE_MIGRATION_TEST_ROOT")
        if not test_root_raw or scratch_parent != Path(test_root_raw).resolve():
            fail("dry-run scratch parent may differ from /tmp only in an explicit test root")
    scratch = Path(tempfile.mkdtemp(prefix="dearmachine-migrate-dry-run.", dir=scratch_parent))
    scratch.chmod(0o700)
    keep = os.environ.get("DEARMACHINE_KEEP_MIGRATION_SCRATCH") == "1"
    try:
        legacy = scratch / "legacy/device-client.db"
        target = scratch / "new-state/dearmachine.db"
        metadata = scratch / "migration-state"
        print(f"dry-run scratch: {scratch}", flush=True)
        online_backup_readonly(source, legacy)
        print(f"live source opened read-only via SQLite online backup API: {source}")
        snapshot_counts = integrity_and_counts(legacy)
        check_expected(snapshot_counts)
        print_counts("snapshot", snapshot_counts)
        manifest = migrate_files(legacy, target, metadata)
        if manifest["counts"] != snapshot_counts:
            fail("snapshot and migration baseline counts differ")
        print("dry-run migration exercise passed; live database and sidecars were not modified")
    finally:
        if keep:
            print(f"dry-run scratch retained by request: {scratch}")
        else:
            shutil.rmtree(scratch)


def load_manifest(metadata_dir: Path) -> tuple[Path, dict[str, Any]]:
    last = metadata_dir / "last.json"
    if last.is_symlink() or not last.is_file():
        fail(f"no migration rollback metadata exists: {last}")
    with last.open("r", encoding="utf-8") as handle:
        manifest = json.load(handle)
    return last, manifest


def rollback(metadata_dir: Path) -> None:
    last, manifest = load_manifest(metadata_dir)
    if manifest.get("status") not in {
        "migrated-awaiting-clean-poll",
        "clean-poll-observed",
    }:
        fail(f"migration is not rollback-capable in status {manifest.get('status')!r}")
    source = Path(manifest["source"])
    target = Path(manifest["target"])
    backup_dir = Path(manifest["rollback_dir"])
    if source.exists() or Path(f"{source}-wal").exists() or Path(f"{source}-shm").exists():
        fail(f"legacy source path is occupied; refusing rollback: {source}")
    observed = integrity_and_counts(target)
    if observed != manifest["counts"]:
        fail(f"target counts changed since migration: {observed} != {manifest['counts']}")
    for suffix in ("", *SIDECAR_SUFFIXES):
        target_file = Path(f"{target}{suffix}")
        if target_file.exists():
            os.replace(target_file, backup_dir / f"post-migration{suffix}")
    for item in manifest["backups"]:
        backup = Path(item["backup"])
        original = Path(item["original"])
        if sha256(backup) != item["sha256"]:
            fail(f"rollback backup checksum mismatch: {backup}")
        shutil.copy2(backup, original)
        original.chmod(0o600)
    restored = integrity_and_counts(source)
    if restored != manifest["counts"]:
        fail(f"restored counts do not match migration baseline: {restored}")
    manifest["status"] = "rolled-back"
    manifest["rolled_back_at"] = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    atomic_json(backup_dir / "manifest.json", manifest)
    atomic_json(last, manifest)
    print_counts("restored", restored)
    print("migration rolled back; post-migration database retained for diagnosis")


def mark_clean(metadata_dir: Path) -> None:
    last, manifest = load_manifest(metadata_dir)
    if manifest.get("status") != "migrated-awaiting-clean-poll":
        fail(f"cannot mark clean poll from status {manifest.get('status')!r}")
    manifest["status"] = "clean-poll-observed"
    manifest["clean_poll_at"] = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    backup_dir = Path(manifest["rollback_dir"])
    atomic_json(backup_dir / "manifest.json", manifest)
    atomic_json(last, manifest)
    print(f"clean poll recorded; rollback remains at {backup_dir}")


def main(argv: list[str]) -> int:
    if len(argv) != 2 or argv[1] not in {"--dry-run", "--real", "--rollback", "--mark-clean"}:
        print(
            "usage: dearmachine-state-migrate "
            "<--dry-run|--real|--rollback|--mark-clean>",
            file=sys.stderr,
        )
        return 2
    source, target, metadata_dir = paths()
    try:
        if argv[1] == "--dry-run":
            dry_run(source)
        elif argv[1] == "--real":
            require_stop_proof(source, target)
            migrate_files(source, target, metadata_dir)
        elif argv[1] == "--rollback":
            require_stop_proof(source, target)
            rollback(metadata_dir)
        else:
            mark_clean(metadata_dir)
    except (MigrationError, OSError, sqlite3.Error, json.JSONDecodeError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
