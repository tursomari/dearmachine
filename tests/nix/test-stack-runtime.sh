#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT=${PROJECT_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
STACK_RUNTIME=$PROJECT_ROOT/scripts/nix/stack-runtime.sh
TEST_ROOT=$(mktemp -d /tmp/dearmachine-stack-runtime.XXXXXXXX)
trap 'rm -rf "$TEST_ROOT"' EXIT

export TEST_ACTION_LOG=$TEST_ROOT/actions.log
export DEARMACHINE_STATE_DIR=$TEST_ROOT/state
export DEARMACHINE_CACHE_DIR=$TEST_ROOT/cache
export DEARMACHINE_STACK_CONFIG_DIR=$TEST_ROOT/config
export DEARMACHINE_PODMAN_ROOT=$TEST_ROOT/podman
export DEARMACHINE_CLIENT_HOME=$TEST_ROOT/client-home
export DEARMACHINE_PROJECT_DIR=$TEST_ROOT/project
export DEARMACHINE_TOOLS_DIR=$TEST_ROOT/tools
export DEARMACHINE_COMPOSE_DIR=$TEST_ROOT/compose
export DEARMACHINE_IMAGE_ARCHIVE=$TEST_ROOT/image.tar
export DEARMACHINE_FUSE_OVERLAYFS=/bin/true
export DEARMACHINE_IDMAP_DIR=$TEST_ROOT/bin
export DEARMACHINE_STACK_MODE=production
export DEARMACHINE_PROJECT_NAME=dearmachine-stack-runtime-test
export DEARMACHINE_TEST_SKIP_HOST_PREFLIGHT=1

install -d -m 0700 \
  "$DEARMACHINE_CLIENT_HOME/.config/dearmachine" \
  "$DEARMACHINE_PROJECT_DIR" "$DEARMACHINE_TOOLS_DIR" \
  "$DEARMACHINE_COMPOSE_DIR" "$DEARMACHINE_IDMAP_DIR"
install -m 0600 /dev/null \
  "$DEARMACHINE_CLIENT_HOME/.config/dearmachine/backends.env"
printf 'image\n' >"$DEARMACHINE_IMAGE_ARCHIVE"
printf 'services: {}\n' >"$DEARMACHINE_COMPOSE_DIR/compose.yaml"
printf 'services: {}\n' >"$DEARMACHINE_COMPOSE_DIR/compose.production.yaml"

for executable in machtiani newuidmap newgidmap; do
  printf '#!%s\nexit 0\n' "$BASH" >"$DEARMACHINE_TOOLS_DIR/$executable"
  if [[ $executable != machtiani ]]; then
    mv "$DEARMACHINE_TOOLS_DIR/$executable" "$DEARMACHINE_IDMAP_DIR/$executable"
  fi
done
chmod 0700 "$DEARMACHINE_TOOLS_DIR/machtiani" \
  "$DEARMACHINE_IDMAP_DIR/newuidmap" "$DEARMACHINE_IDMAP_DIR/newgidmap"

printf '#!%s\n' "$BASH" >"$DEARMACHINE_IDMAP_DIR/podman"
cat >>"$DEARMACHINE_IDMAP_DIR/podman" <<'EOF'
set -euo pipefail
printf 'podman %s\n' "$*" >>"$TEST_ACTION_LOG"
case ${1:-} in
  info|load) exit 0 ;;
  secret)
    [[ ${2:-} == inspect ]] && exit 0
    ;;
  ps)
    [[ ! -e ${TEST_CONTAINER_PRESENT:-/nonexistent} ]] || printf 'container-id\n'
    exit 0
    ;;
esac
exit 0
EOF
chmod 0700 "$DEARMACHINE_IDMAP_DIR/podman"

printf '#!%s\n' "$BASH" >"$DEARMACHINE_IDMAP_DIR/podman-compose"
cat >>"$DEARMACHINE_IDMAP_DIR/podman-compose" <<'EOF'
set -euo pipefail
printf 'compose' >>"$TEST_ACTION_LOG"
printf ' <%s>' "$@" >>"$TEST_ACTION_LOG"
printf '\n' >>"$TEST_ACTION_LOG"
EOF
chmod 0700 "$DEARMACHINE_IDMAP_DIR/podman-compose"

bash "$STACK_RUNTIME" create \
  --email person@example.test --new-inbox --transport agentmail

grep -F 'compose <-f>' "$TEST_ACTION_LOG" >/dev/null
grep -F '<run> <--rm> <--no-deps> <dearmachine> <up> <--create>' \
  "$TEST_ACTION_LOG" >/dev/null
grep -F '<--email> <person@example.test> <--new-inbox> <--transport> <agentmail>' \
  "$TEST_ACTION_LOG" >/dev/null
grep -F '<--once> <--magnifica-humanitas> <--verbose>' \
  "$TEST_ACTION_LOG" >/dev/null

chmod 0644 "$DEARMACHINE_CLIENT_HOME/.config/dearmachine/backends.env"
if bash "$STACK_RUNTIME" create \
    --email person@example.test --new-inbox --transport agentmail \
    >/dev/null 2>&1; then
  echo 'create unexpectedly accepted a group-readable backend environment' >&2
  exit 1
fi
chmod 0600 "$DEARMACHINE_CLIENT_HOME/.config/dearmachine/backends.env"

if (
  export DEARMACHINE_STACK_MODE=test
  bash "$STACK_RUNTIME" create \
    --email person@example.test --new-inbox --transport agentmail
) >/dev/null 2>&1; then
  echo 'create unexpectedly accepted test mode' >&2
  exit 1
fi

TEST_CONTAINER_PRESENT=$TEST_ROOT/container-present
export TEST_CONTAINER_PRESENT
touch "$TEST_CONTAINER_PRESENT"
if bash "$STACK_RUNTIME" create \
    --email person@example.test --new-inbox --transport agentmail \
    >/dev/null 2>&1; then
  echo 'create unexpectedly accepted a running stack' >&2
  exit 1
fi

printf 'stack runtime tests passed\n'
