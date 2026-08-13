#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT=${PROJECT_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
LIFECYCLE=$PROJECT_ROOT/scripts/nix/host-lifecycle.sh
UNIT_TEMPLATE=$PROJECT_ROOT/contrib/systemd/dearmachine-stack.service.in
TEST_ROOT=$(mktemp -d /tmp/dearmachine-host-lifecycle.XXXXXXXX)
trap 'rm -rf "$TEST_ROOT"' EXIT

export HOME=$TEST_ROOT/home
export XDG_DATA_HOME=$TEST_ROOT/xdg-data
export XDG_CONFIG_HOME=$TEST_ROOT/xdg-config
export XDG_STATE_HOME=$TEST_ROOT/xdg-state
export XDG_CACHE_HOME=$TEST_ROOT/xdg-cache
export DEARMACHINE_SYSTEMD_USER_DIR=$XDG_CONFIG_HOME/systemd/user
export DEARMACHINE_UNIT_NAME=dearmachine-test-host-stack.service
export DEARMACHINE_UNIT_TEMPLATE=$UNIT_TEMPLATE
export DEARMACHINE_SKIP_NIX_GC_ROOT=1
export DEARMACHINE_MIGRATION_HELPER=$TEST_ROOT/migration-helper
export DEARMACHINE_SYSTEMCTL=$TEST_ROOT/systemctl
export TEST_ACTION_LOG=$TEST_ROOT/actions.log
export TEST_FAIL_START=$TEST_ROOT/fail-start-once
export TEST_FAIL_POLL=$TEST_ROOT/fail-poll

mkdir -p "$HOME" "$XDG_CONFIG_HOME/dearmachine"
cat >"$XDG_CONFIG_HOME/dearmachine/stack.env" <<EOF
DEARMACHINE_PROJECT_DIR=$TEST_ROOT/project
DEARMACHINE_INBOX_ID=credential-free-test
DEARMACHINE_STACK_MODE=test
EOF
chmod 0600 "$XDG_CONFIG_HOME/dearmachine/stack.env"
mkdir -p "$TEST_ROOT/project"

printf '#!%s\n' "$BASH" >"$DEARMACHINE_SYSTEMCTL"
cat >>"$DEARMACHINE_SYSTEMCTL" <<'EOF'
set -euo pipefail
printf 'systemctl %s\n' "$*" >>"$TEST_ACTION_LOG"
if [[ ${1:-} == is-active ]]; then
  exit 1
fi
if [[ ${1:-} == start && -e $TEST_FAIL_START ]]; then
  rm -f "$TEST_FAIL_START"
  exit 1
fi
EOF
chmod 0700 "$DEARMACHINE_SYSTEMCTL"

printf '#!%s\n' "$BASH" >"$DEARMACHINE_MIGRATION_HELPER"
cat >>"$DEARMACHINE_MIGRATION_HELPER" <<'EOF'
printf 'migration %s\n' "$*" >>"$TEST_ACTION_LOG"
EOF
chmod 0700 "$DEARMACHINE_MIGRATION_HELPER"

make_runtime() {
  local runtime=$1
  mkdir -p "$runtime/bin"
  printf '#!%s\n' "$BASH" >"$runtime/bin/dearmachine-stack"
  cat >>"$runtime/bin/dearmachine-stack" <<'EOF'
set -euo pipefail
printf 'stack %s\n' "$*" >>"$TEST_ACTION_LOG"
case ${1:-} in
  container-logs) echo 'DearMachine credential-free Compose test service ready' ;;
  poll-ready) [[ ! -e $TEST_FAIL_POLL ]] ;;
  wait) exit 0 ;;
esac
EOF
  chmod 0700 "$runtime/bin/dearmachine-stack"
}

RUNTIME_ONE=$TEST_ROOT/runtime-one
RUNTIME_TWO=$TEST_ROOT/runtime-two
IMAGE_ONE=$TEST_ROOT/release-one.tar.gz
IMAGE_TWO=$TEST_ROOT/release-two.tar.gz
make_runtime "$RUNTIME_ONE"
make_runtime "$RUNTIME_TWO"
printf 'image one\n' >"$IMAGE_ONE"
printf 'image two\n' >"$IMAGE_TWO"

run_lifecycle() {
  DEARMACHINE_RUNTIME_PATH=$1 \
    DEARMACHINE_IMAGE_SOURCE=$2 \
    bash "$LIFECYCLE" "${@:3}"
}

assert_link() {
  [[ $(readlink "$1") == "$2" ]] || {
    printf 'expected %s -> %s, got %s\n' "$1" "$2" "$(readlink "$1")" >&2
    exit 1
  }
}

run_lifecycle "$RUNTIME_ONE" "$IMAGE_ONE" install
archive_dir=$XDG_DATA_HOME/dearmachine/image-archive
runtime_dir=$XDG_DATA_HOME/dearmachine/runtime
unit=$DEARMACHINE_SYSTEMD_USER_DIR/$DEARMACHINE_UNIT_NAME
assert_link "$archive_dir/current" "releases/$(basename "$IMAGE_ONE")"
assert_link "$runtime_dir/current" "$RUNTIME_ONE"
grep -F "ExecStart=$runtime_dir/current/bin/dearmachine-stack up" "$unit"
grep -F 'WantedBy=default.target' "$unit"
touch "$XDG_STATE_HOME/dearmachine-stack/preserved-state"
touch "$HOME/.dearmachine/state/preserved-client-state"

run_lifecycle "$RUNTIME_ONE" "$IMAGE_ONE" install
run_lifecycle "$RUNTIME_ONE" "$IMAGE_ONE" status
run_lifecycle "$RUNTIME_ONE" "$IMAGE_ONE" logs
run_lifecycle "$RUNTIME_ONE" "$IMAGE_ONE" secrets status

touch "$TEST_FAIL_START"
if run_lifecycle "$RUNTIME_TWO" "$IMAGE_TWO" upgrade 2>/dev/null; then
  echo 'upgrade unexpectedly succeeded after injected start failure' >&2
  exit 1
fi
assert_link "$archive_dir/current" "releases/$(basename "$IMAGE_ONE")"
assert_link "$runtime_dir/current" "$RUNTIME_ONE"

run_lifecycle "$RUNTIME_TWO" "$IMAGE_TWO" upgrade
assert_link "$archive_dir/current" "releases/$(basename "$IMAGE_TWO")"
assert_link "$archive_dir/rollback" "releases/$(basename "$IMAGE_ONE")"
assert_link "$runtime_dir/current" "$RUNTIME_TWO"
assert_link "$runtime_dir/rollback" "$RUNTIME_ONE"

run_lifecycle "$RUNTIME_TWO" "$IMAGE_TWO" upgrade --rollback
assert_link "$archive_dir/current" "releases/$(basename "$IMAGE_ONE")"
assert_link "$archive_dir/rollback" "releases/$(basename "$IMAGE_TWO")"
assert_link "$runtime_dir/current" "$RUNTIME_ONE"
assert_link "$runtime_dir/rollback" "$RUNTIME_TWO"

DEARMACHINE_CLEAN_POLL_ATTEMPTS=1 DEARMACHINE_CLEAN_POLL_DELAY=0 \
  run_lifecycle "$RUNTIME_ONE" "$IMAGE_ONE" migrate --real
grep -F 'migration --real' "$TEST_ACTION_LOG" >/dev/null
grep -F 'migration --mark-clean' "$TEST_ACTION_LOG" >/dev/null
if grep -F 'systemctl stop dearmachine.service' "$TEST_ACTION_LOG" >/dev/null; then
  echo 'test-unit migration attempted to stop the live legacy unit' >&2
  exit 1
fi

touch "$TEST_FAIL_POLL"
if DEARMACHINE_CLEAN_POLL_ATTEMPTS=1 DEARMACHINE_CLEAN_POLL_DELAY=0 \
    run_lifecycle "$RUNTIME_ONE" "$IMAGE_ONE" migrate --real 2>/dev/null; then
  echo 'migration unexpectedly accepted a missing clean poll' >&2
  exit 1
fi
rm -f "$TEST_FAIL_POLL"
tail -n 3 "$TEST_ACTION_LOG" | grep -F \
  'systemctl stop dearmachine-test-host-stack.service' >/dev/null

run_lifecycle "$RUNTIME_ONE" "$IMAGE_ONE" uninstall
[[ ! -e $unit ]]
[[ ! -e $archive_dir ]]
[[ ! -e $runtime_dir ]]
[[ -f $XDG_STATE_HOME/dearmachine-stack/preserved-state ]]
[[ -f $HOME/.dearmachine/state/preserved-client-state ]]
grep -F 'stack unload' "$TEST_ACTION_LOG" >/dev/null
printf 'host lifecycle tests passed\n'
