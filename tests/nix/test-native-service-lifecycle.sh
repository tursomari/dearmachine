#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT=${PROJECT_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
LIFECYCLE=${NATIVE_SERVICE_LIFECYCLE:-$PROJECT_ROOT/scripts/nix/native-service-lifecycle.sh}
TEST_ROOT=$(mktemp -d /tmp/dearmachine-native-lifecycle.XXXXXXXX)
trap 'rm -rf "$TEST_ROOT"' EXIT

BIN=$TEST_ROOT/bin
HOME=$TEST_ROOT/home
export HOME USER=tester XDG_CONFIG_HOME=$HOME/.config XDG_DATA_HOME=$HOME/.local/share
export DEARMACHINE_SYSTEMD_USER_DIR=$HOME/.config/systemd/user
export DEARMACHINE_NATIVE_SERVICE_DATA_DIR=$HOME/.local/share/dearmachine/native-service
export DEARMACHINE_SYSTEMCTL=$BIN/systemctl DEARMACHINE_LOGINCTL=$BIN/loginctl
LOG=$TEST_ROOT/actions.log
export LOG
mkdir -p "$BIN" "$HOME/.dearmachine/config" "$HOME/.dearmachine/entrypoint/main" "$HOME/.config/dearmachine"
printf 'version = 1\nbackends = ["forge"]\n' >"$HOME/.dearmachine/config/dearmachine.toml"
printf 'OPENROUTER_API_KEY=private-test-value\n' >"$HOME/.config/dearmachine/backends.env"
printf 'openmail-private-test-value\n' >"$HOME/.config/dearmachine/openmail-api-key"
chmod 0600 "$HOME/.config/dearmachine/backends.env" "$HOME/.config/dearmachine/openmail-api-key"

for command in dearmachine agent-manager systemctl loginctl; do
  printf '#!%s\n' "$BASH" >"$BIN/$command"
done

cat >>"$BIN/dearmachine" <<'EOF'
printf 'dearmachine %s\n' "$*" >>"$LOG"
EOF
cat >>"$BIN/agent-manager" <<'EOF'
printf 'agent-manager %s\n' "$*" >>"$LOG"
EOF
cat >>"$BIN/systemctl" <<'EOF'
printf 'systemctl %s\n' "$*" >>"$LOG"
exit 0
EOF
cat >>"$BIN/loginctl" <<'EOF'
printf 'loginctl %s\n' "$*" >>"$LOG"
if [[ $1 == show-user ]]; then printf 'yes\n'; fi
EOF
chmod 0700 "$BIN"/*
export PATH="$BIN:$PATH"

"$BASH" "$LIFECYCLE" install --transport openmail --linger \
  --client "$BIN/dearmachine" --agent-manager "$BIN/agent-manager"

UNIT=$DEARMACHINE_SYSTEMD_USER_DIR/dearmachine-native.service
WRAPPER=$DEARMACHINE_NATIVE_SERVICE_DATA_DIR/dearmachine-native-launcher
[[ -f $UNIT && ! -L $UNIT && $(stat -c '%a' "$UNIT") == 600 ]]
[[ -x $WRAPPER && ! -L $WRAPPER && $(stat -c '%a' "$WRAPPER") == 700 ]]
grep -F 'WantedBy=default.target' "$UNIT" >/dev/null
grep -F 'OPENMAIL_API_KEY_FILE=' "$UNIT" >/dev/null
grep -F "EnvironmentFile=$HOME/.config/dearmachine/backends.env" "$UNIT" >/dev/null
grep -F 'UnsetEnvironment=AGENTMAIL_API_KEY OPENMAIL_API_KEY SENDMUX_API_KEY' "$UNIT" >/dev/null
grep -F 'Restart=on-failure' "$UNIT" >/dev/null
grep -F 'RestartSec=1min' "$UNIT" >/dev/null
grep -F 'RestartSteps=6' "$UNIT" >/dev/null
grep -F 'RestartMaxDelaySec=1h' "$UNIT" >/dev/null
if grep -F 'Restart=on-abnormal' "$UNIT" >/dev/null; then
  echo 'native service would not recover from an ordinary provider failure' >&2
  exit 1
fi
if grep -F 'private-test-value' "$UNIT" "$WRAPPER" >/dev/null; then
  echo 'native service files contain credential values' >&2
  exit 1
fi
grep -F 'agent-manager backend resolve' "$LOG" >/dev/null
grep -F 'systemctl enable --now dearmachine-native.service' "$LOG" >/dev/null
grep -F 'loginctl enable-linger tester' "$LOG" >/dev/null
grep -F 'loginctl show-user tester -p Linger --value' "$LOG" >/dev/null
if command -v systemd-analyze >/dev/null; then
  export XDG_RUNTIME_DIR=$TEST_ROOT/run SYSTEMD_UNIT_PATH="$DEARMACHINE_SYSTEMD_USER_DIR:${SYSTEMD_EXAMPLE_USER:-/usr/lib/systemd/user}"
  mkdir -p "$XDG_RUNTIME_DIR"
  systemd-analyze --user --man=no --generators=no verify "$UNIT"
fi

# Magnifica Humanitas is off unless chosen, and an explicit choice survives
# reinstallation and can be changed from enabled to disabled.
install_wrapper() {
  "$BASH" "$LIFECYCLE" install --transport openmail \
    --client "$BIN/dearmachine" --agent-manager "$BIN/agent-manager" "$@"
}
wrapper_launch() {
  : >"$LOG"
  "$BASH" "$WRAPPER"
  grep -F 'dearmachine up --foreground' "$LOG"
}
assert_quote() {
  local expected=$1 launch
  launch=$(wrapper_launch)
  case $expected in
    absent)
      if [[ $launch == *magnifica-humanitas* ]]; then
        printf 'wrapper unexpectedly names the quote flag: %s\n' "$launch" >&2
        exit 1
      fi
      ;;
    *)
      [[ $launch == *" --magnifica-humanitas=$expected "* ]] || {
        printf 'wrapper lacks --magnifica-humanitas=%s: %s\n' "$expected" "$launch" >&2
        exit 1
      }
      [[ $(grep -o -- '--magnifica-humanitas' <<<"$launch" | wc -l) -eq 1 ]]
      ;;
  esac
}
assert_quote absent
install_wrapper --magnifica-humanitas
assert_quote true
install_wrapper
assert_quote true
install_wrapper --magnifica-humanitas=false
assert_quote false
install_wrapper
assert_quote false
install_wrapper --magnifica-humanitas=true
assert_quote true
if install_wrapper --magnifica-humanitas=maybe 2>/dev/null; then
  echo 'invalid quote choice was accepted' >&2
  exit 1
fi
rm -f "$WRAPPER"
install_wrapper
assert_quote absent

"$BASH" "$LIFECYCLE" status
"$BASH" "$LIFECYCLE" disable
grep -F 'systemctl status --no-pager dearmachine-native.service' "$LOG" >/dev/null
grep -F 'systemctl disable --now dearmachine-native.service' "$LOG" >/dev/null

printf 'native persistent service lifecycle tests passed\n'
