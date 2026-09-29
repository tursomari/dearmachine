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

# Magnifica Humanitas is off unless chosen. The persisted runtime
# configuration governs every launcher restart: the launcher never carries the
# flag, so a later choice (including enabled to disabled) cannot be overridden
# by an earlier explicit launcher argument.
RUNTIME=$HOME/.dearmachine/config/runtime.toml
install_wrapper() {
  "$BASH" "$LIFECYCLE" install --transport openmail \
    --client "$BIN/dearmachine" --agent-manager "$BIN/agent-manager" "$@"
}
# The isolated client stand-in reports the effective setting the way the real
# client resolves it: an explicit launch flag wins, otherwise the persisted
# runtime value applies.
cat >"$BIN/dearmachine" <<STUB
#!$BASH
printf 'dearmachine %s\n' "\$*" >>"\$LOG"
effective=false
if grep -qx 'magnifica_humanitas = true' "$RUNTIME" 2>/dev/null; then effective=true; fi
for argument in "\$@"; do
  case \$argument in
    --magnifica-humanitas|--magnifica-humanitas=true) effective=true ;;
    --magnifica-humanitas=false) effective=false ;;
  esac
done
printf 'effective-quote=%s\n' "\$effective" >>"\$LOG"
STUB
chmod 0700 "$BIN/dearmachine"
# Restart the generated launcher on this host and print the effective setting.
restart_effective() {
  : >"$LOG"
  "$BASH" "$WRAPPER"
  if grep -F 'dearmachine up --foreground' "$LOG" | grep -F -- 'magnifica-humanitas' >/dev/null; then
    echo 'launcher names the quote flag' >&2
    exit 1
  fi
  sed -n 's/^effective-quote=//p' "$LOG"
}
assert_effective() {
  local actual
  actual=$(restart_effective)
  [[ $actual == "$1" ]] || { printf 'effective quote %s, want %s\n' "$actual" "$1" >&2; exit 1; }
}
write_runtime() {
  printf 'version = 1\npoll_interval = "10s"\nmagnifica_humanitas = %s\n' "$1" >"$RUNTIME"
  chmod 0600 "$RUNTIME"
}

# Default off: no runtime configuration and no flag.
install_wrapper
assert_effective false
# The option cannot be recorded before pair creation has written runtime.toml.
if install_wrapper --magnifica-humanitas 2>/dev/null; then
  echo 'quote choice was accepted without runtime configuration' >&2
  exit 1
fi
# Explicit opt-in is recorded in the runtime configuration and survives restart.
write_runtime false
install_wrapper --magnifica-humanitas
assert_effective true
grep -Fx 'magnifica_humanitas = true' "$RUNTIME" >/dev/null
[[ $(grep -c '^magnifica_humanitas' "$RUNTIME") -eq 1 && $(stat -c '%a' "$RUNTIME") == 600 ]]
install_wrapper
assert_effective true
# An existing enabled launcher from an earlier generation (explicit =true),
# then onboarding persists false: reinstall and restart must yield disabled.
cat >"$WRAPPER" <<LEGACY
#!/usr/bin/env bash
set -euo pipefail
cd -- '$HOME/.dearmachine/entrypoint/main'
exec '$BIN/dearmachine' up --foreground --magnifica-humanitas=true --verbose --config '$HOME/.dearmachine/config/dearmachine.toml' --agent-manager '$BIN/agent-manager'
LEGACY
chmod 0700 "$WRAPPER"
write_runtime false
# The legacy explicit flag would keep the quote on despite the persisted choice.
: >"$LOG"
"$BASH" "$WRAPPER"
grep -Fx 'effective-quote=true' "$LOG" >/dev/null
install_wrapper
assert_effective false
# The same holds when the disabling choice is passed to the installer.
write_runtime true
install_wrapper --magnifica-humanitas=false
assert_effective false
grep -Fx 'magnifica_humanitas = false' "$RUNTIME" >/dev/null
install_wrapper --magnifica-humanitas=true
assert_effective true
if install_wrapper --magnifica-humanitas=maybe 2>/dev/null; then
  echo 'invalid quote choice was accepted' >&2
  exit 1
fi
# Removing the launcher and runtime configuration returns to default off.
rm -f "$WRAPPER" "$RUNTIME"
install_wrapper
assert_effective false

"$BASH" "$LIFECYCLE" status
"$BASH" "$LIFECYCLE" disable
grep -F 'systemctl status --no-pager dearmachine-native.service' "$LOG" >/dev/null
grep -F 'systemctl disable --now dearmachine-native.service' "$LOG" >/dev/null

printf 'native persistent service lifecycle tests passed\n'
