#!/usr/bin/env bash
# Launch one native DearMachine client as a transient systemd user service.
#
# The service gets a validated snapshot of the launcher PATH.  This is
# deliberately not a login shell: backend CLIs and their runtimes must have
# the same lookup environment that passed launch-time preflight.
set -euo pipefail

dm_die() {
  printf 'dearmachine-native-service: %s\n' "$*" >&2
  exit 1
}

dm_warn() {
  printf 'dearmachine-native-service: warning: %s\n' "$*" >&2
}

dm_usage() {
  cat <<'EOF'
Usage:
  dearmachine-native-service [options] -- <dearmachine> [run options]

Launch the native DearMachine Client in a transient systemd user service. The
launcher resolves every backend selected in the device configuration, snapshots
the launcher's complete absolute PATH, and passes that PATH explicitly to the
service. It never copies AGENTMAIL_API_KEY into systemd; provide a credential
file instead.

Options:
  --config <path>            Device configuration (default: ~/.dearmachine/config/dearmachine.toml)
  --credential-file <path>   Required one-line AgentMail credential file
  --environment-file <path>  Optional systemd EnvironmentFile for other backend variables
  --agent-manager <path>     Agent Manager executable (default: agent-manager from PATH)
  --home <path>              DEARMACHINE_HOME (default: ~/.dearmachine)
  --unit <name>              Transient user-unit name (default: dearmachine-native)
  --working-directory <path> Service working directory (default: current directory)
  -h, --help                 Show this help
EOF
}

dm_append_path_dir() {
  local dm_candidate=$1
  case $dm_candidate in
    /) ;;
    */) dm_candidate=${dm_candidate%/} ;;
  esac
  [[ $dm_candidate == /* ]] || dm_die "PATH entry is not absolute: $dm_candidate"
  if [[ ! -d $dm_candidate ]]; then
    dm_warn "ignoring PATH entry that is not a directory: $dm_candidate"
    return
  fi
  if [[ -z ${dm_path_seen[$dm_candidate]+present} ]]; then
    dm_path_seen[$dm_candidate]=1
    dm_path_parts+=("$dm_candidate")
  fi
}

dm_absolute_path() {
  local dm_input=$1
  local dm_parent
  local dm_leaf
  if [[ $dm_input == /* ]]; then
    dm_parent=${dm_input%/*}
    dm_leaf=${dm_input##*/}
  else
    dm_parent=.
    dm_leaf=$dm_input
  fi
  [[ -n $dm_parent ]] || dm_parent=/
  printf '%s/%s\n' "$(cd -- "$dm_parent" && pwd -P)" "$dm_leaf"
}

dm_find_on_service_path() {
  local dm_name=$1
  local dm_candidate
  for dm_path_dir in "${dm_path_parts[@]}"; do
    dm_candidate=$dm_path_dir/$dm_name
    if [[ -f $dm_candidate && -x $dm_candidate ]]; then
      printf '%s\n' "$dm_candidate"
      return 0
    fi
  done
  return 1
}

dm_config_path=
dm_credential_file=
dm_environment_file=
dm_manager=agent-manager
dm_home=${DEARMACHINE_HOME:-"${HOME:?HOME is required}"/.dearmachine}
dm_unit=dearmachine-native
dm_working_directory=$PWD

while (($# > 0)); do
  case $1 in
    --config)
      (($# >= 2)) || dm_die '--config requires a path'
      dm_config_path=$2
      shift 2
      ;;
    --credential-file)
      (($# >= 2)) || dm_die '--credential-file requires a path'
      dm_credential_file=$2
      shift 2
      ;;
    --environment-file)
      (($# >= 2)) || dm_die '--environment-file requires a path'
      dm_environment_file=$2
      shift 2
      ;;
    --agent-manager)
      (($# >= 2)) || dm_die '--agent-manager requires a path'
      dm_manager=$2
      shift 2
      ;;
    --home)
      (($# >= 2)) || dm_die '--home requires a path'
      dm_home=$2
      shift 2
      ;;
    --unit)
      (($# >= 2)) || dm_die '--unit requires a name'
      dm_unit=$2
      shift 2
      ;;
    --working-directory)
      (($# >= 2)) || dm_die '--working-directory requires a path'
      dm_working_directory=$2
      shift 2
      ;;
    -h|--help)
      dm_usage
      exit 0
      ;;
    --)
      shift
      break
      ;;
    *)
      dm_die "unknown option: $1"
      ;;
  esac
done

(($# > 0)) || dm_die 'a DearMachine client command is required after --'
[[ -n $dm_credential_file ]] || dm_die '--credential-file is required'
[[ $dm_unit =~ ^[A-Za-z0-9][A-Za-z0-9_.@-]*$ ]] || dm_die "unsafe systemd unit name: $dm_unit"

dm_config_path=${dm_config_path:-"$dm_home/config/dearmachine.toml"}
dm_config_path=$(dm_absolute_path "$dm_config_path")
dm_credential_file=$(dm_absolute_path "$dm_credential_file")
dm_home=$(dm_absolute_path "$dm_home")
dm_working_directory=$(cd -- "$dm_working_directory" && pwd -P)
if [[ -n $dm_environment_file ]]; then
  dm_environment_file=$(dm_absolute_path "$dm_environment_file")
fi

[[ -r $dm_config_path ]] || dm_die "device configuration is not readable: $dm_config_path"
[[ -f $dm_credential_file && -r $dm_credential_file ]] || dm_die "credential file is not readable: $dm_credential_file"
[[ -d $dm_home ]] || dm_die "DEARMACHINE_HOME is not a directory: $dm_home"
if [[ -n $dm_environment_file ]]; then
  [[ -f $dm_environment_file && -r $dm_environment_file ]] || \
    dm_die "environment file is not readable: $dm_environment_file"
fi

declare -a dm_path_parts=()
declare -A dm_path_seen=()
IFS=: read -r -a dm_launch_path_parts <<<"${PATH:?PATH is required}"
for dm_path_dir in "${dm_launch_path_parts[@]}"; do
  [[ -n $dm_path_dir ]] || dm_die 'PATH contains an empty entry'
  dm_append_path_dir "$dm_path_dir"
done
for dm_path_dir in /usr/local/sbin /usr/local/bin /usr/sbin /usr/bin /sbin /bin; do
  [[ -d $dm_path_dir ]] && dm_append_path_dir "$dm_path_dir"
done
dm_service_path=$(IFS=:; printf '%s' "${dm_path_parts[*]}")

if [[ $dm_manager == */* ]]; then
  dm_manager=$(dm_absolute_path "$dm_manager")
  [[ -x $dm_manager ]] || dm_die "agent-manager is not executable: $dm_manager"
else
  dm_manager=$(dm_find_on_service_path "$dm_manager" || true)
  [[ -n $dm_manager ]] || dm_die 'agent-manager is not available on the service PATH'
fi

dm_client_command=$1
shift
if [[ $dm_client_command == */* ]]; then
  dm_client_command=$(dm_absolute_path "$dm_client_command")
  [[ -x $dm_client_command ]] || dm_die "DearMachine client is not executable: $dm_client_command"
else
  dm_client_command=$(dm_find_on_service_path "$dm_client_command" || true)
  [[ -n $dm_client_command ]] || dm_die 'dearmachine is not available on the service PATH'
fi

# This verifies both the configuration and every configured executable after
# the PATH has been normalized exactly as it will be for the service.
env -i \
  "HOME=${HOME:?HOME is required}" \
  "PATH=$dm_service_path" \
  "DEARMACHINE_HOME=$dm_home" \
  "$dm_manager" backend resolve --config "$dm_config_path" >/dev/null

dm_systemd_run=${DEARMACHINE_SYSTEMD_RUN:-systemd-run}
if [[ $dm_systemd_run != */* ]]; then
  dm_systemd_run=$(type -P "$dm_systemd_run" || true)
  [[ -n $dm_systemd_run ]] || dm_die 'systemd-run is not available on PATH'
fi

declare -a dm_environment_file_property=()
if [[ -n $dm_environment_file ]]; then
  dm_environment_file_property=(--property="EnvironmentFile=$dm_environment_file")
fi

exec "$dm_systemd_run" --user \
  --unit="$dm_unit" \
  --collect \
  --property=Restart=on-failure \
  --property=RestartSec=1min \
  --property=RestartSteps=6 \
  --property=RestartMaxDelaySec=1h \
  --property=KillMode=control-group \
  --working-directory="$dm_working_directory" \
  --setenv="PATH=$dm_service_path" \
  --setenv="HOME=${HOME:?HOME is required}" \
  --setenv="DEARMACHINE_HOME=$dm_home" \
  "${dm_environment_file_property[@]}" \
  --property=UnsetEnvironment=AGENTMAIL_API_KEY \
  --setenv="AGENTMAIL_API_KEY_FILE=$dm_credential_file" \
  "$dm_client_command" up --foreground "$@" \
  --config "$dm_config_path" \
  --agent-manager "$dm_manager"
