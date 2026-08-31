#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT=${PROJECT_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
HOST_RUNTIME_DIR=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}
TEST_ROOT=$(mktemp -d /tmp/dearmachine-host-podman.XXXXXXXX)
readonly TEST_ROOT
readonly UNIT_NAME=dearmachine-test-podman-$RANDOM$RANDOM.service
readonly LEGACY_UNIT_NAME=dearmachine-test-legacy-$RANDOM$RANDOM.service
UNIT_DIR=$(systemd-path user-configuration)/systemd/user
readonly UNIT_DIR
PROTECTED_MAIN_PID=$(systemctl --user show dearmachine.service \
  --property=MainPID --value 2>/dev/null || true)
readonly PROTECTED_MAIN_PID
INSTALLED=0

assert_live_client_unchanged() {
  [[ $PROTECTED_MAIN_PID =~ ^[1-9][0-9]*$ ]] || return 0
  systemctl --user is-active --quiet dearmachine.service
  [[ $(systemctl --user show dearmachine.service \
    --property=MainPID --value) == "$PROTECTED_MAIN_PID" ]]
}

cleanup() {
  if (( INSTALLED )); then
    (
      cd "$PROJECT_ROOT"
      nix run .#host-uninstall >/dev/null 2>&1 || true
    )
  fi
  systemctl --user disable --now "$UNIT_NAME" >/dev/null 2>&1 || true
  rm -f "$UNIT_DIR/$UNIT_NAME"
  systemctl --user daemon-reload >/dev/null 2>&1 || true
  if [[ $TEST_ROOT == /tmp/dearmachine-host-podman.* && \
        -d $TEST_ROOT && ! -L $TEST_ROOT && \
        $(stat -c %u "$TEST_ROOT") -eq $(id -u) ]]; then
    rm -rf -- "$TEST_ROOT"
  else
    echo "refusing to remove unexpected test root: $TEST_ROOT" >&2
  fi
}
trap cleanup EXIT

export HOME=$TEST_ROOT/home
export XDG_DATA_HOME=$TEST_ROOT/xdg-data
export XDG_CONFIG_HOME=$TEST_ROOT/xdg-config
export XDG_STATE_HOME=$TEST_ROOT/xdg-state
export XDG_CACHE_HOME=$TEST_ROOT/xdg-cache
export XDG_RUNTIME_DIR=$HOST_RUNTIME_DIR
export DEARMACHINE_SYSTEMD_USER_DIR=$UNIT_DIR
export DEARMACHINE_UNIT_NAME=$UNIT_NAME
export DEARMACHINE_LEGACY_UNIT_NAME=$LEGACY_UNIT_NAME
export DEARMACHINE_PROJECT_DIR=$TEST_ROOT/project
export DEARMACHINE_STACK_MODE=test
export NIX_CONFIG=${NIX_CONFIG:-experimental-features = nix-command flakes}

install -d -m 0700 "$HOME" "$XDG_CONFIG_HOME/dearmachine" "$DEARMACHINE_PROJECT_DIR"
cat >"$XDG_CONFIG_HOME/dearmachine/stack.env" <<EOF
DEARMACHINE_PROJECT_DIR=$DEARMACHINE_PROJECT_DIR
DEARMACHINE_STACK_MODE=test
EOF
chmod 0600 "$XDG_CONFIG_HOME/dearmachine/stack.env"

cd "$PROJECT_ROOT"
nix run .#host-install
INSTALLED=1

[[ $(nix run .#dearmachine-host-lifecycle -- health) == healthy ]]
nix run .#dearmachine-host-lifecycle -- exec dearmachine --help 2>&1 |
  grep -F 'Usage: dearmachine <command>'
nix run .#dearmachine-host-lifecycle -- logs --tail 100 |
  grep -F 'DearMachine credential-free Compose test service ready'

original_archive=$(readlink "$XDG_DATA_HOME/dearmachine/image-archive/current")
printf 'not an OCI archive\n' >"$TEST_ROOT/broken-image.tar.gz"
if DEARMACHINE_IMAGE_SOURCE=$TEST_ROOT/broken-image.tar.gz \
    nix run .#host-upgrade >/dev/null 2>&1; then
  echo 'upgrade unexpectedly accepted an invalid OCI archive' >&2
  exit 1
fi
[[ $(readlink "$XDG_DATA_HOME/dearmachine/image-archive/current") == "$original_archive" ]]
[[ ! -e $XDG_DATA_HOME/dearmachine/image-archive/releases/broken-image.tar.gz ]]
systemctl --user is-active --quiet "$UNIT_NAME"
grep -F "$(basename "$original_archive")" "$UNIT_DIR/$UNIT_NAME" >/dev/null
[[ $(nix run .#dearmachine-host-lifecycle -- health) == healthy ]]

current_archive=$(readlink -f "$XDG_DATA_HOME/dearmachine/image-archive/current")
trial_upgrade=$TEST_ROOT/dearmachine-trial-upgrade.tar.gz
cp --reflink=auto "$current_archive" "$trial_upgrade"
DEARMACHINE_IMAGE_SOURCE=$trial_upgrade nix run .#host-upgrade
DEARMACHINE_IMAGE_SOURCE=$trial_upgrade nix run .#host-upgrade -- --rollback
[[ $(nix run .#dearmachine-host-lifecycle -- health) == healthy ]]

nix run .#host-uninstall
INSTALLED=0
[[ -z $(nix run .#dearmachine-host-lifecycle -- containers --format '{{.ID}}') ]]
[[ ! -e $UNIT_DIR/$UNIT_NAME ]]
[[ ! -e $XDG_DATA_HOME/dearmachine/image-archive ]]
[[ ! -e $XDG_DATA_HOME/dearmachine/runtime ]]
[[ -z $(systemctl --user list-unit-files "$UNIT_NAME" --no-legend 2>/dev/null) ]]
assert_live_client_unchanged

printf 'host Podman/systemd integration tests passed\n'
