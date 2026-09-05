#!/usr/bin/env bash
# Install and operate the reboot-persistent native DearMachine systemd user unit.
set -euo pipefail

dm_die() {
  printf 'dearmachine-native-service-lifecycle: %s\n' "$*" >&2
  exit 1
}

dm_usage() {
  cat <<'EOF'
Usage:
  dearmachine-native-service-lifecycle install --transport <agentmail|openmail|sendmux> [options]
  dearmachine-native-service-lifecycle disable [options]
  dearmachine-native-service-lifecycle status [options]

Options:
  --config <path>            Device configuration (default: ~/.dearmachine/config/dearmachine.toml)
  --credential-file <path>   Selected transport credential file
  --environment-file <path>  Backend provider environment file
  --client <path>            DearMachine executable (default: dearmachine from PATH)
  --agent-manager <path>     Agent Manager executable (default: agent-manager from PATH)
  --home <path>              DEARMACHINE_HOME (default: ~/.dearmachine)
  --unit <name>              User-unit name (default: dearmachine-native)
  --working-directory <path> Service working directory (default: ~/.dearmachine/entrypoint/main)
  --linger                   Keep the user manager running without an interactive login
  -h, --help                 Show this help
EOF
}

dm_absolute_path() {
  local input=$1 parent leaf
  if [[ $input == /* ]]; then
    parent=${input%/*}
    leaf=${input##*/}
  else
    parent=.
    leaf=$input
  fi
  [[ -n $parent ]] || parent=/
  printf '%s/%s\n' "$(cd -- "$parent" && pwd -P)" "$leaf"
}

dm_private_file() {
  local path=$1 label=$2 mode owner
  [[ -f $path && ! -L $path && -r $path ]] || dm_die "$label is not a readable regular file: $path"
  mode=$(stat -c '%a' "$path")
  owner=$(stat -c '%u' "$path")
  [[ $mode == 600 ]] || dm_die "$label must have mode 0600: $path"
  [[ $owner == "$(id -u)" ]] || dm_die "$label must be owned by the current user: $path"
}

dm_systemctl() {
  if [[ -n ${DEARMACHINE_SYSTEMCTL:-} ]]; then
    "$DEARMACHINE_SYSTEMCTL" "$@"
  else
    systemctl --user "$@"
  fi
}

dm_loginctl() {
  if [[ -n ${DEARMACHINE_LOGINCTL:-} ]]; then
    "$DEARMACHINE_LOGINCTL" "$@"
  else
    loginctl "$@"
  fi
}

dm_shell_quote() {
  printf '%q' "$1"
}

dm_systemd_quote() {
  local value=$1
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  printf '"%s"' "$value"
}

command=${1:-}
case $command in
  install|disable|status) shift ;;
  -h|--help|'') dm_usage; exit 0 ;;
  *) dm_usage >&2; dm_die "unknown command: $command" ;;
esac

dm_transport=
dm_config=
dm_credential_file=
dm_environment_file=
dm_client=dearmachine
dm_manager=agent-manager
dm_home=${DEARMACHINE_HOME:-"${HOME:?HOME is required}"/.dearmachine}
dm_unit=dearmachine-native
dm_working_directory=
dm_linger=false

while (($# > 0)); do
  case $1 in
    --transport) (($# >= 2)) || dm_die '--transport requires a value'; dm_transport=$2; shift 2 ;;
    --config) (($# >= 2)) || dm_die '--config requires a path'; dm_config=$2; shift 2 ;;
    --credential-file) (($# >= 2)) || dm_die '--credential-file requires a path'; dm_credential_file=$2; shift 2 ;;
    --environment-file) (($# >= 2)) || dm_die '--environment-file requires a path'; dm_environment_file=$2; shift 2 ;;
    --client) (($# >= 2)) || dm_die '--client requires a path'; dm_client=$2; shift 2 ;;
    --agent-manager) (($# >= 2)) || dm_die '--agent-manager requires a path'; dm_manager=$2; shift 2 ;;
    --home) (($# >= 2)) || dm_die '--home requires a path'; dm_home=$2; shift 2 ;;
    --unit) (($# >= 2)) || dm_die '--unit requires a name'; dm_unit=$2; shift 2 ;;
    --working-directory) (($# >= 2)) || dm_die '--working-directory requires a path'; dm_working_directory=$2; shift 2 ;;
    --linger) dm_linger=true; shift ;;
    -h|--help) dm_usage; exit 0 ;;
    *) dm_die "unknown option: $1" ;;
  esac
done

[[ $dm_unit =~ ^[A-Za-z0-9][A-Za-z0-9_.@-]*$ ]] || dm_die "unsafe systemd user unit name: $dm_unit"
dm_service_name=$dm_unit.service
dm_config=${dm_config:-"$dm_home/config/dearmachine.toml"}
dm_working_directory=${dm_working_directory:-"$dm_home/entrypoint/main"}
dm_config=$(dm_absolute_path "$dm_config")
dm_home=$(dm_absolute_path "$dm_home")

dm_config_root=${XDG_CONFIG_HOME:-"${HOME:?HOME is required}"/.config}
dm_data_root=${XDG_DATA_HOME:-"${HOME:?HOME is required}"/.local/share}
dm_systemd_dir=${DEARMACHINE_SYSTEMD_USER_DIR:-"$dm_config_root/systemd/user"}
dm_service_data_dir=${DEARMACHINE_NATIVE_SERVICE_DATA_DIR:-"$dm_data_root/dearmachine/native-service"}
dm_unit_path=$dm_systemd_dir/$dm_service_name
dm_wrapper_path=$dm_service_data_dir/$dm_unit-launcher

if [[ $command == status ]]; then
  dm_systemctl status --no-pager "$dm_service_name"
  exit
fi

if [[ $command == disable ]]; then
  dm_systemctl disable --now "$dm_service_name"
  printf 'Dear Machine automatic startup is disabled.\n'
  exit
fi

case $dm_transport in
  agentmail) dm_credential_variable=AGENTMAIL_API_KEY_FILE ;;
  openmail) dm_credential_variable=OPENMAIL_API_KEY_FILE ;;
  sendmux) dm_credential_variable=SENDMUX_API_KEY_FILE ;;
  *) dm_die 'install requires --transport agentmail, openmail, or sendmux' ;;
esac

dm_credential_file=${dm_credential_file:-"${HOME:?HOME is required}/.config/dearmachine/$dm_transport-api-key"}
dm_environment_file=${dm_environment_file:-"${HOME:?HOME is required}/.config/dearmachine/backends.env"}
dm_credential_file=$(dm_absolute_path "$dm_credential_file")
dm_environment_file=$(dm_absolute_path "$dm_environment_file")
dm_working_directory=$(cd -- "$dm_working_directory" && pwd -P)

if [[ $dm_client != */* ]]; then dm_client=$(type -P "$dm_client" || true); fi
if [[ $dm_manager != */* ]]; then dm_manager=$(type -P "$dm_manager" || true); fi
[[ -x $dm_client ]] || dm_die 'dearmachine is not executable'
[[ -x $dm_manager ]] || dm_die 'agent-manager is not executable'
dm_client=$(dm_absolute_path "$dm_client")
dm_manager=$(dm_absolute_path "$dm_manager")
[[ -f $dm_config && -r $dm_config ]] || dm_die "device configuration is not readable: $dm_config"
dm_private_file "$dm_credential_file" 'transport credential file'
dm_private_file "$dm_environment_file" 'backend environment file'
[[ $dm_environment_file != *[[:space:]]* ]] || dm_die 'backend environment file path must not contain whitespace'
[[ -d $dm_working_directory ]] || dm_die "working directory is missing: $dm_working_directory"

dm_systemctl show-environment >/dev/null || dm_die 'the systemd user manager is unavailable'
"$dm_manager" backend resolve --config "$dm_config" >/dev/null

install -d -m 0700 "$dm_service_data_dir"
install -d -m 0755 "$dm_systemd_dir"
dm_wrapper_temporary=$dm_wrapper_path.new
{
  printf '#!/usr/bin/env bash\nset -euo pipefail\ncd -- '
  dm_shell_quote "$dm_working_directory"
  printf '\nexec '
  dm_shell_quote "$dm_client"
  printf ' up --foreground --magnifica-humanitas --verbose --config '
  dm_shell_quote "$dm_config"
  printf ' --agent-manager '
  dm_shell_quote "$dm_manager"
  printf '\n'
} >"$dm_wrapper_temporary"
chmod 0700 "$dm_wrapper_temporary"
mv -f "$dm_wrapper_temporary" "$dm_wrapper_path"

dm_path=${DEARMACHINE_SERVICE_PATH:-${PATH:?PATH is required}}
dm_unit_temporary=$dm_unit_path.new
{
  printf '[Unit]\nDescription=Dear Machine native client\nAfter=network-online.target\nWants=network-online.target\n\n'
  printf '[Service]\nType=simple\n'
  printf 'Environment=%s\n' "$(dm_systemd_quote "HOME=${HOME:?HOME is required}")"
  printf 'Environment=%s\n' "$(dm_systemd_quote "PATH=$dm_path")"
  printf 'Environment=%s\n' "$(dm_systemd_quote "DEARMACHINE_HOME=$dm_home")"
  printf 'EnvironmentFile=%s\n' "$dm_environment_file"
  printf 'Environment=%s\n' "$(dm_systemd_quote "$dm_credential_variable=$dm_credential_file")"
  printf 'UnsetEnvironment=AGENTMAIL_API_KEY OPENMAIL_API_KEY SENDMUX_API_KEY\n'
  printf 'ExecStart=%s\n' "$(dm_systemd_quote "$dm_wrapper_path")"
  # A normal nonzero exit is an actionable configuration/provider failure.
  # Restarting it cannot repair authentication or quota and would repeatedly
  # rerun pending model work. Signals, timeouts, and other abnormal failures
  # remain restartable for unattended operation.
  printf 'Restart=on-abnormal\nRestartSec=5s\nKillMode=control-group\nUMask=0077\n\n'
  printf '[Install]\nWantedBy=default.target\n'
} >"$dm_unit_temporary"
chmod 0600 "$dm_unit_temporary"
mv -f "$dm_unit_temporary" "$dm_unit_path"

dm_systemctl stop "$dm_service_name" >/dev/null 2>&1 || true
"$dm_client" down >/dev/null 2>&1 || true
dm_systemctl daemon-reload
dm_systemctl enable --now "$dm_service_name"
dm_systemctl is-enabled --quiet "$dm_service_name"
dm_systemctl is-active --quiet "$dm_service_name"

if [[ $dm_linger == true ]]; then
  dm_loginctl enable-linger "${USER:?USER is required}"
  [[ $(dm_loginctl show-user "$USER" -p Linger --value) == yes ]] || dm_die 'user lingering was not enabled'
fi

printf 'Dear Machine will stay on and start automatically after reboot.\n'
printf 'To turn it off: systemctl --user disable --now %s\n' "$dm_service_name"
