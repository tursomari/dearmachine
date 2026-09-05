#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT=${PROJECT_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
LAUNCHER=${NATIVE_SERVICE_LAUNCHER:-$PROJECT_ROOT/scripts/nix/native-service-launch.sh}
TEST_ROOT=$(mktemp -d /tmp/dearmachine-native-service.XXXXXXXX)
trap 'rm -rf "$TEST_ROOT"' EXIT

MANAGER_BIN=$TEST_ROOT/manager-bin
CLIENT_BIN=$TEST_ROOT/client-bin
NVM_BIN=$TEST_ROOT/nvm-bin
SYSTEMD_RUN=$TEST_ROOT/systemd-run
CONFIG=$TEST_ROOT/dearmachine.toml
CREDENTIAL=$TEST_ROOT/agentmail-api-key
ENVIRONMENT_FILE=$TEST_ROOT/backends.env
MANAGER_LOG=$TEST_ROOT/home/manager.log
SYSTEMD_LOG=$TEST_ROOT/systemd.log
WORKING_DIRECTORY=$TEST_ROOT/workspace

mkdir -p "$MANAGER_BIN" "$CLIENT_BIN" "$NVM_BIN" "$WORKING_DIRECTORY" "$TEST_ROOT/home"
printf 'credential\n' >"$CREDENTIAL"
chmod 0600 "$CREDENTIAL"
printf 'DEEPSEEK_API_KEY=backend-test\nAGENTMAIL_API_KEY=must-not-reach-service\n' >"$ENVIRONMENT_FILE"
chmod 0600 "$ENVIRONMENT_FILE"
printf 'version = 1\nbackends = ["codex-yolo"]\n' >"$CONFIG"

printf '#!%s\n' "$BASH" >"$MANAGER_BIN/agent-manager"
cat >>"$MANAGER_BIN/agent-manager" <<'EOF'
set -euo pipefail
printf 'PATH=%s\nARGS=' "${PATH:-}" >>"$DEARMACHINE_HOME/manager.log"
printf '%q ' "$@" >>"$DEARMACHINE_HOME/manager.log"
printf '\n' >>"$DEARMACHINE_HOME/manager.log"
if [[ ${4:-} == */reject.toml ]]; then
  exit 17
fi
printf '[{"id":"codex-yolo","executable":"codex","path":"/test/nvm/codex"}]\n'
EOF
chmod 0700 "$MANAGER_BIN/agent-manager"

printf '#!%s\n' "$BASH" >"$CLIENT_BIN/dearmachine"
cat >>"$CLIENT_BIN/dearmachine" <<'EOF'
exit 0
EOF
chmod 0700 "$CLIENT_BIN/dearmachine"

printf '#!%s\n' "$BASH" >"$SYSTEMD_RUN"
cat >>"$SYSTEMD_RUN" <<'EOF'
set -euo pipefail
printf '%q ' "$@" >"$SYSTEMD_LOG"
printf '\n' >>"$SYSTEMD_LOG"
EOF
chmod 0700 "$SYSTEMD_RUN"

export SYSTEMD_LOG
export AGENTMAIL_API_KEY=must-not-appear-in-systemd-properties
LAUNCH_PATH=$PATH
export PATH="$NVM_BIN:$CLIENT_BIN:$MANAGER_BIN:$LAUNCH_PATH"

assert_contains() {
  local expected=$1
  local file=$2
  if ! grep -F -- "$expected" "$file" >/dev/null; then
    printf 'expected %q in %s:\n' "$expected" "$file" >&2
    sed -n '1,120p' "$file" >&2
    exit 1
  fi
}

DEARMACHINE_SYSTEMD_RUN=$SYSTEMD_RUN \
  "$BASH" "$LAUNCHER" \
  --config "$CONFIG" \
  --credential-file "$CREDENTIAL" \
  --environment-file "$ENVIRONMENT_FILE" \
  --agent-manager "$MANAGER_BIN/agent-manager" \
  --home "$TEST_ROOT/home" \
  --unit dearmachine-native-test \
  --working-directory "$WORKING_DIRECTORY" \
  -- "$CLIENT_BIN/dearmachine" --magnifica-humanitas

expected_path_prefix="$NVM_BIN:$CLIENT_BIN:$MANAGER_BIN:"
assert_contains "$expected_path_prefix" "$MANAGER_LOG"
assert_contains "backend resolve --config $CONFIG" "$MANAGER_LOG"
assert_contains "$expected_path_prefix" "$SYSTEMD_LOG"
assert_contains "--property=EnvironmentFile=$ENVIRONMENT_FILE" "$SYSTEMD_LOG"
assert_contains "--property=UnsetEnvironment=AGENTMAIL_API_KEY" "$SYSTEMD_LOG"
assert_contains "--property=Restart=on-abnormal" "$SYSTEMD_LOG"
if grep -F -- '--property=Restart=on-failure' "$SYSTEMD_LOG" >/dev/null; then
  echo 'launcher would restart-loop after an ordinary provider failure' >&2
  exit 1
fi
assert_contains "--setenv=AGENTMAIL_API_KEY_FILE=$CREDENTIAL" "$SYSTEMD_LOG"
assert_contains "--working-directory=$WORKING_DIRECTORY" "$SYSTEMD_LOG"
assert_contains "$CLIENT_BIN/dearmachine up --foreground --magnifica-humanitas --config $CONFIG --agent-manager $MANAGER_BIN/agent-manager" "$SYSTEMD_LOG"
if grep -F -- "$AGENTMAIL_API_KEY" "$SYSTEMD_LOG" >/dev/null; then
  echo 'launcher placed AGENTMAIL_API_KEY in systemd properties' >&2
  exit 1
fi

REJECT_CONFIG=$TEST_ROOT/reject.toml
cp "$CONFIG" "$REJECT_CONFIG"
rm -f "$SYSTEMD_LOG"
if DEARMACHINE_SYSTEMD_RUN=$SYSTEMD_RUN \
  "$BASH" "$LAUNCHER" \
  --config "$REJECT_CONFIG" \
  --credential-file "$CREDENTIAL" \
  --environment-file "$ENVIRONMENT_FILE" \
  --agent-manager "$MANAGER_BIN/agent-manager" \
  --home "$TEST_ROOT/home" \
  -- "$CLIENT_BIN/dearmachine" --magnifica-humanitas 2>/dev/null; then
  echo 'launcher unexpectedly created a service after backend resolution failed' >&2
  exit 1
fi
[[ ! -e $SYSTEMD_LOG ]]

printf 'native service launcher tests passed\n'
