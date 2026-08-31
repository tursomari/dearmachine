#!/usr/bin/env bash
set -euo pipefail
umask 077

# DearMachine agents intentionally run in the operator's user manager: they
# need that user's selected repositories, backend credentials, and ~/.machtiani.
# Do not translate this lifecycle into xsrc's dedicated system-user model.
readonly SERVICE_NAME=${DEARMACHINE_UNIT_NAME:-dearmachine-stack.service}
readonly DATA_HOME=${XDG_DATA_HOME:-$HOME/.local/share}
readonly CONFIG_HOME=${XDG_CONFIG_HOME:-$HOME/.config}
readonly STATE_HOME=${XDG_STATE_HOME:-$HOME/.local/state}
readonly CACHE_HOME=${XDG_CACHE_HOME:-$HOME/.cache}
readonly DATA_DIR=$DATA_HOME/dearmachine
readonly ARCHIVE_DIR=$DATA_DIR/image-archive
readonly RELEASES_DIR=$ARCHIVE_DIR/releases
readonly RUNTIME_DIR=$DATA_DIR/runtime
readonly CONFIG_DIR=$CONFIG_HOME/dearmachine
readonly STACK_ENV=$CONFIG_DIR/stack.env
readonly RUNTIME_ENV=$CONFIG_DIR/runtime.env
readonly SYSTEMD_DIR=${DEARMACHINE_SYSTEMD_USER_DIR:-$CONFIG_HOME/systemd/user}
readonly UNIT_PATH=$SYSTEMD_DIR/$SERVICE_NAME
readonly STACK_STATE_DIR=$STATE_HOME/dearmachine-stack
readonly STACK_CACHE_DIR=$CACHE_HOME/dearmachine-stack
readonly CLIENT_HOME=$DATA_DIR/client-home
readonly CLIENT_ROOT=$HOME/.dearmachine

usage() {
  cat <<'EOF'
Usage: dearmachine-host-lifecycle <command> [options]

  install                         Install and start the user stack
  create ARGS...                  Create a pair while the stack is down
  upgrade                         Upgrade and retain one rollback release
  upgrade --rollback              Swap current and rollback releases
  start|stop|restart|status|health|logs
                                  Operate dearmachine-stack.service
  exec ARGS...                    Execute a command in the client container
  containers [ARGS...]            Run isolated `podman ps -a` for this stack
  secrets status                  Report the production Podman secret
  secrets sync --file FILE        Create/update the production Podman secret
  secrets rotate --file FILE      Update the secret and restart the stack
  secrets remove                  Remove the production Podman secret
  uninstall                       Remove service, images, and release archives;
                                  preserve user configuration and state
EOF
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_packaged_inputs() {
  : "${DEARMACHINE_RUNTIME_PATH:?DEARMACHINE_RUNTIME_PATH is not set by the Nix package}"
  : "${DEARMACHINE_IMAGE_SOURCE:?DEARMACHINE_IMAGE_SOURCE is not set by the Nix package}"
  : "${DEARMACHINE_UNIT_TEMPLATE:?DEARMACHINE_UNIT_TEMPLATE is not set by the Nix package}"
  [[ -x $DEARMACHINE_RUNTIME_PATH/bin/dearmachine-stack ]] || \
    die "missing packaged stack wrapper"
  [[ -f $DEARMACHINE_IMAGE_SOURCE ]] || die "missing packaged OCI archive"
  [[ -f $DEARMACHINE_UNIT_TEMPLATE ]] || die "missing systemd unit template"
}

validate_service_name() {
  [[ $SERVICE_NAME =~ ^dearmachine(-test-[A-Za-z0-9_-]+)?-stack\.service$ || \
     $SERVICE_NAME == dearmachine-stack.service || \
     $SERVICE_NAME =~ ^dearmachine-test-[A-Za-z0-9_-]+\.service$ ]] || \
    die "unsafe systemd user unit name: $SERVICE_NAME"
}

systemctl_user() {
  if [[ -n ${DEARMACHINE_SYSTEMCTL:-} ]]; then
    "$DEARMACHINE_SYSTEMCTL" "$@"
  else
    systemctl --user "$@"
  fi
}

check_user_manager() {
  systemctl_user show-environment >/dev/null || \
    die "the systemd user manager is unavailable"
}

read_link() {
  readlink "$1" 2>/dev/null || true
}

archive_target() {
  local link=$1 target
  target=$(read_link "$link")
  [[ -n $target ]] || return 0
  if [[ $target == /* ]]; then
    printf '%s\n' "$target"
  else
    printf '%s\n' "$(dirname "$link")/$target"
  fi
}

atomic_link() {
  local target=$1 destination=$2 temporary=$2.dearmachine-new
  [[ ! -e $destination || -L $destination ]] || \
    die "refusing to replace non-symlink path: $destination"
  rm -f "$temporary"
  ln -s "$target" "$temporary"
  mv -Tf "$temporary" "$destination"
}

set_runtime_root() {
  local target=$1 destination=$2
  rm -f "$destination"
  if [[ ${DEARMACHINE_SKIP_NIX_GC_ROOT:-0} == 1 ]]; then
    ln -s "$target" "$destination"
  else
    nix-store --add-root "$destination" --indirect --realise "$target" >/dev/null
  fi
}

ensure_directories() {
  install -d -m 0700 \
    "$DATA_DIR" "$ARCHIVE_DIR" "$RELEASES_DIR" "$RUNTIME_DIR" \
    "$CONFIG_DIR" "$STACK_STATE_DIR" "$STACK_CACHE_DIR" \
    "$CLIENT_HOME" "$CLIENT_ROOT" "$CLIENT_ROOT/config" \
    "$CLIENT_ROOT/state" "$CLIENT_ROOT/run" "$CLIENT_ROOT/log" \
    "$CLIENT_ROOT/agent-manager" \
    "$DATA_DIR/tools" "$SYSTEMD_DIR"
  install -d -m 0700 "$CLIENT_HOME/.config/dearmachine"
}

require_stack_environment() {
  [[ -f $STACK_ENV && ! -L $STACK_ENV ]] || {
    printf 'Create %s before installation. It must define at least:\n' "$STACK_ENV" >&2
    printf '  DEARMACHINE_PROJECT_DIR=/absolute/project\n' >&2
    die "missing stack environment file"
  }
  [[ $(stat -c %u "$STACK_ENV") -eq $(id -u) ]] || \
    die "stack environment must be owned by the current user"
  local mode
  mode=$(stat -c %a "$STACK_ENV")
  (( (8#$mode & 8#077) == 0 )) || \
    die "stack environment must not be accessible by group or other"
  if grep -Eq '^[[:space:]]*(DEARMACHINE_(STATE_DIR|CACHE_DIR|STACK_CONFIG_DIR|PODMAN_ROOT|IMAGE_ARCHIVE|PROJECT_NAME|CLIENT_HOME|CLIENT_CONFIG_DIR|CLIENT_STATE_DIR|CLIENT_RUN_DIR|CLIENT_LOG_DIR|CLIENT_AGENT_MANAGER_DIR|MACHTIANI_DIR|TOOLS_DIR))=' "$STACK_ENV"; then
    die "stack.env may not override lifecycle-managed storage, archive, or project-name variables"
  fi
  grep -Eq '^[[:space:]]*DEARMACHINE_PROJECT_DIR=/.+' "$STACK_ENV" || \
    die "stack.env must set DEARMACHINE_PROJECT_DIR to an absolute path"
}

write_runtime_environment() {
  local temporary=$RUNTIME_ENV.dearmachine-new
  cat >"$temporary" <<EOF
DEARMACHINE_STATE_DIR="$STACK_STATE_DIR"
DEARMACHINE_CACHE_DIR="$STACK_CACHE_DIR"
DEARMACHINE_STACK_CONFIG_DIR="$CONFIG_DIR/containers"
DEARMACHINE_PODMAN_ROOT="$STACK_STATE_DIR/podman"
DEARMACHINE_CLIENT_HOME="$CLIENT_HOME"
DEARMACHINE_CLIENT_CONFIG_DIR="$CLIENT_ROOT/config"
DEARMACHINE_CLIENT_STATE_DIR="$CLIENT_ROOT/state"
DEARMACHINE_CLIENT_RUN_DIR="$CLIENT_ROOT/run"
DEARMACHINE_CLIENT_LOG_DIR="$CLIENT_ROOT/log"
DEARMACHINE_CLIENT_AGENT_MANAGER_DIR="$CLIENT_ROOT/agent-manager"
DEARMACHINE_MACHTIANI_DIR="$HOME/.machtiani"
DEARMACHINE_TOOLS_DIR="$DATA_DIR/tools"
DEARMACHINE_PROJECT_NAME="dearmachine"
DEARMACHINE_STACK_MODE="production"
DEARMACHINE_IMAGE_ARCHIVE="$ARCHIVE_DIR/current"
EOF
  chmod 0600 "$temporary"
  mv -f "$temporary" "$RUNTIME_ENV"
}

escape_sed_replacement() {
  printf '%s' "$1" | sed 's/[\\&|]/\\&/g'
}

install_unit() {
  local temporary=$UNIT_PATH.dearmachine-new
  local release=${1:?release is required}
  sed \
    -e "s|@RELEASE@|$(escape_sed_replacement "$release")|g" \
    -e "s|@RUNTIME_ENV@|$(escape_sed_replacement "$RUNTIME_ENV")|g" \
    -e "s|@STACK_ENV@|$(escape_sed_replacement "$STACK_ENV")|g" \
    -e "s|@STACK_EXEC@|$(escape_sed_replacement "$RUNTIME_DIR/current/bin/dearmachine-stack")|g" \
    "$DEARMACHINE_UNIT_TEMPLATE" >"$temporary"
  chmod 0644 "$temporary"
  mv -f "$temporary" "$UNIT_PATH"
  systemctl_user daemon-reload
}

release_id() {
  basename "$DEARMACHINE_IMAGE_SOURCE"
}

prepare_archive() {
  local release=$1
  local destination=$RELEASES_DIR/$release
  if [[ -e $destination ]]; then
    [[ -f $destination && ! -L $destination ]] || \
      die "invalid managed image archive: $destination"
    cmp -s "$DEARMACHINE_IMAGE_SOURCE" "$destination" || \
      die "release archive collision: $destination"
  else
    cp --reflink=auto "$DEARMACHINE_IMAGE_SOURCE" "$destination.dearmachine-new"
    chmod 0600 "$destination.dearmachine-new"
    mv -f "$destination.dearmachine-new" "$destination"
  fi
}

prune_archives() {
  local current rollback candidate
  current=$(archive_target "$ARCHIVE_DIR/current")
  rollback=$(archive_target "$ARCHIVE_DIR/rollback")
  shopt -s nullglob
  for candidate in "$RELEASES_DIR"/*; do
    if [[ $candidate != "$current" && $candidate != "$rollback" ]]; then
      [[ -f $candidate && ! -L $candidate ]] || \
        die "refusing to prune unexpected release path: $candidate"
      rm -f "$candidate"
    fi
  done
  shopt -u nullglob
}

export_stack_context() {
  local mode=${DEARMACHINE_STACK_MODE:-}
  if [[ -z $mode && -f $STACK_ENV ]]; then
    mode=$(sed -n 's/^[[:space:]]*DEARMACHINE_STACK_MODE=//p' "$STACK_ENV" | tail -n 1)
    mode=${mode#\"}
    mode=${mode%\"}
    mode=${mode#\'}
    mode=${mode%\'}
  fi
  mode=${mode:-production}
  case $mode in
    development|production|test) ;;
    *) die "invalid DEARMACHINE_STACK_MODE in stack context: $mode" ;;
  esac
  export DEARMACHINE_STATE_DIR=$STACK_STATE_DIR
  export DEARMACHINE_CACHE_DIR=$STACK_CACHE_DIR
  export DEARMACHINE_STACK_CONFIG_DIR=$CONFIG_DIR/containers
  export DEARMACHINE_PODMAN_ROOT=$STACK_STATE_DIR/podman
  export DEARMACHINE_CLIENT_HOME=$CLIENT_HOME
  export DEARMACHINE_CLIENT_CONFIG_DIR=$CLIENT_ROOT/config
  export DEARMACHINE_CLIENT_STATE_DIR=$CLIENT_ROOT/state
  export DEARMACHINE_CLIENT_RUN_DIR=$CLIENT_ROOT/run
  export DEARMACHINE_CLIENT_LOG_DIR=$CLIENT_ROOT/log
  export DEARMACHINE_CLIENT_AGENT_MANAGER_DIR=$CLIENT_ROOT/agent-manager
  export DEARMACHINE_MACHTIANI_DIR=$HOME/.machtiani
  export DEARMACHINE_TOOLS_DIR=$DATA_DIR/tools
  export DEARMACHINE_PROJECT_NAME=dearmachine
  export DEARMACHINE_STACK_MODE=$mode
  export DEARMACHINE_IMAGE_ARCHIVE=${DEARMACHINE_CREATE_IMAGE_ARCHIVE:-$ARCHIVE_DIR/current}
}

stack_executable() {
  if [[ -x $RUNTIME_DIR/current/bin/dearmachine-stack ]]; then
    printf '%s\n' "$RUNTIME_DIR/current/bin/dearmachine-stack"
  else
    printf '%s\n' "$DEARMACHINE_RUNTIME_PATH/bin/dearmachine-stack"
  fi
}

run_stack() {
  local executable
  if [[ -f $STACK_ENV ]]; then
    # Match systemd's EnvironmentFile behavior for operator-invoked lifecycle
    # commands such as exec, logs, and health. Managed boundary variables are
    # exported afterward so stack.env cannot redirect lifecycle-owned storage.
    set -a
    # shellcheck disable=SC1090
    source "$STACK_ENV"
    set +a
  fi
  export_stack_context
  executable=$(stack_executable)
  "$executable" "$@"
}

install_host() {
  [[ $# -eq 0 ]] || die "install takes no options"
  require_packaged_inputs
  validate_service_name
  check_user_manager
  ensure_directories
  require_stack_environment

  local release current
  release=$(release_id)
  current=$(archive_target "$ARCHIVE_DIR/current")
  if [[ -n $current && $current != "$RELEASES_DIR/$release" ]]; then
    die "DearMachine is already installed; use upgrade"
  fi
  prepare_archive "$release"
  atomic_link "releases/$release" "$ARCHIVE_DIR/current"
  set_runtime_root "$DEARMACHINE_RUNTIME_PATH" "$RUNTIME_DIR/current"
  write_runtime_environment
  install_unit "$release"
  systemctl_user enable --now "$SERVICE_NAME"
  printf 'DearMachine user stack installed: %s\n' "$SERVICE_NAME"
  printf 'image archive: %s\n' "$ARCHIVE_DIR/current"
  printf 'state preserved across upgrades: %s and %s\n' \
    "$STACK_STATE_DIR" "$CLIENT_ROOT"
}

swap_release_links() {
  local current_archive rollback_archive current_runtime rollback_runtime
  current_archive=$(read_link "$ARCHIVE_DIR/current")
  rollback_archive=$(read_link "$ARCHIVE_DIR/rollback")
  current_runtime=$(read_link "$RUNTIME_DIR/current")
  rollback_runtime=$(read_link "$RUNTIME_DIR/rollback")
  [[ -n $current_archive && -n $rollback_archive ]] || \
    die "no image rollback release is available"
  [[ -n $current_runtime && -n $rollback_runtime ]] || \
    die "no runtime rollback release is available"
  atomic_link "$rollback_archive" "$ARCHIVE_DIR/current"
  atomic_link "$current_archive" "$ARCHIVE_DIR/rollback"
  set_runtime_root "$rollback_runtime" "$RUNTIME_DIR/current"
  set_runtime_root "$current_runtime" "$RUNTIME_DIR/rollback"
}

restart_with_release() {
  systemctl_user stop "$SERVICE_NAME" || true
  run_stack unload || true
  systemctl_user daemon-reload
  systemctl_user start "$SERVICE_NAME"
}

rollback_host() {
  check_user_manager
  [[ -f $UNIT_PATH ]] || die "DearMachine is not installed"
  swap_release_links
  local release
  release=$(basename "$(read_link "$ARCHIVE_DIR/current")")
  install_unit "$release"
  if ! restart_with_release; then
    swap_release_links
    release=$(basename "$(read_link "$ARCHIVE_DIR/current")")
    install_unit "$release"
    restart_with_release || true
    die "rollback failed; restored the prior release links"
  fi
  printf 'DearMachine rolled back to %s\n' "$(read_link "$ARCHIVE_DIR/current")"
}

upgrade_host() {
  if [[ ${1:-} == --rollback ]]; then
    [[ $# -eq 1 ]] || die "upgrade --rollback takes no other options"
    rollback_host
    return
  fi
  [[ $# -eq 0 ]] || die "unknown upgrade option: ${1:-}"
  require_packaged_inputs
  validate_service_name
  check_user_manager
  ensure_directories
  require_stack_environment
  [[ -f $UNIT_PATH ]] || die "DearMachine is not installed; use install"

  local release old_archive old_runtime old_rollback_archive old_rollback_runtime
  release=$(release_id)
  old_archive=$(read_link "$ARCHIVE_DIR/current")
  old_runtime=$(read_link "$RUNTIME_DIR/current")
  old_rollback_archive=$(read_link "$ARCHIVE_DIR/rollback")
  old_rollback_runtime=$(read_link "$RUNTIME_DIR/rollback")
  [[ -n $old_archive && -n $old_runtime ]] || die "installed release links are incomplete"

  if [[ $old_archive == "releases/$release" && \
        $old_runtime == "$DEARMACHINE_RUNTIME_PATH" ]]; then
    write_runtime_environment
    install_unit "$release"
    restart_with_release
    printf 'DearMachine is already at %s\n' "$release"
    return
  fi

  prepare_archive "$release"
  atomic_link "$old_archive" "$ARCHIVE_DIR/rollback"
  atomic_link "releases/$release" "$ARCHIVE_DIR/current"
  set_runtime_root "$old_runtime" "$RUNTIME_DIR/rollback"
  set_runtime_root "$DEARMACHINE_RUNTIME_PATH" "$RUNTIME_DIR/current"
  write_runtime_environment
  install_unit "$release"

  if ! restart_with_release; then
    atomic_link "$old_archive" "$ARCHIVE_DIR/current"
    if [[ -n $old_rollback_archive ]]; then
      atomic_link "$old_rollback_archive" "$ARCHIVE_DIR/rollback"
    else
      rm -f "$ARCHIVE_DIR/rollback"
    fi
    set_runtime_root "$old_runtime" "$RUNTIME_DIR/current"
    if [[ -n $old_rollback_runtime ]]; then
      set_runtime_root "$old_rollback_runtime" "$RUNTIME_DIR/rollback"
    else
      rm -f "$RUNTIME_DIR/rollback"
    fi
    install_unit "$(basename "$old_archive")"
    restart_with_release || true
    prune_archives
    die "upgrade start failed; restored the prior release"
  fi
  prune_archives
  printf 'DearMachine upgraded to %s\n' "$release"
  printf 'rollback: dearmachine-host-lifecycle upgrade --rollback\n'
}

operate_service() {
  local command=$1
  shift
  [[ $# -eq 0 ]] || die "$command takes no options"
  validate_service_name
  check_user_manager
  systemctl_user "$command" "$SERVICE_NAME"
}

status_host() {
  [[ $# -eq 0 ]] || die "status takes no options"
  validate_service_name
  check_user_manager
  systemctl_user status --no-pager "$SERVICE_NAME"
  printf 'current image archive: %s\n' "$(read_link "$ARCHIVE_DIR/current")"
  printf 'rollback image archive: %s\n' "$(read_link "$ARCHIVE_DIR/rollback")"
}

logs_host() {
  require_packaged_inputs
  run_stack container-logs "$@"
}

stack_command() {
  require_packaged_inputs
  run_stack "$@"
}

create_pair() {
  [[ $# -gt 0 ]] || die "create requires pair arguments"
  require_packaged_inputs
  validate_service_name
  ensure_directories
  require_stack_environment
  if [[ ! -e $ARCHIVE_DIR/current ]]; then
    export DEARMACHINE_CREATE_IMAGE_ARCHIVE=$DEARMACHINE_IMAGE_SOURCE
  fi
  run_stack create "$@"
}

secrets_host() {
  require_packaged_inputs
  [[ $# -gt 0 ]] || set -- status
  local command=$1
  run_stack secrets "$@"
  if [[ $command == rotate && -f $UNIT_PATH ]]; then
    systemctl_user restart "$SERVICE_NAME"
  fi
}

remove_managed_tree() {
  local path=$1 expected_parent=$2
  [[ ! -e $path ]] && return 0
  [[ -d $path && ! -L $path ]] || die "refusing to remove unexpected path: $path"
  [[ $(dirname "$path") == "$expected_parent" ]] || \
    die "refusing to remove path outside its managed parent: $path"
  rm -rf -- "$path"
}

uninstall_host() {
  [[ $# -eq 0 ]] || die "uninstall takes no options"
  validate_service_name
  check_user_manager
  systemctl_user disable --now "$SERVICE_NAME" >/dev/null 2>&1 || true
  run_stack unload || true
  rm -f "$UNIT_PATH"
  systemctl_user daemon-reload
  remove_managed_tree "$ARCHIVE_DIR" "$DATA_DIR"
  remove_managed_tree "$RUNTIME_DIR" "$DATA_DIR"
  rm -f "$RUNTIME_ENV"
  printf 'DearMachine service, image, and release archives removed\n'
  printf 'preserved configuration: %s\n' "$STACK_ENV"
  printf 'preserved state: %s and %s\n' "$STACK_STATE_DIR" "$CLIENT_ROOT"
}

main() {
  local command=${1:-}
  [[ -n $command ]] || { usage; exit 2; }
  shift
  case $command in
    install) install_host "$@" ;;
    create) create_pair "$@" ;;
    upgrade) upgrade_host "$@" ;;
    uninstall) uninstall_host "$@" ;;
    start|stop|restart) operate_service "$command" "$@" ;;
    status) status_host "$@" ;;
    health) [[ $# -eq 0 ]] || die "health takes no options"; stack_command health ;;
    exec) [[ $# -gt 0 ]] || die "exec requires a command"; stack_command exec "$@" ;;
    containers) stack_command containers "$@" ;;
    logs) logs_host "$@" ;;
    secrets) secrets_host "$@" ;;
    help|-h|--help) usage ;;
    *) usage >&2; die "unknown command: $command" ;;
  esac
}

main "$@"
