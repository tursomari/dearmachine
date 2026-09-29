#!/usr/bin/env bash
set -euo pipefail
umask 077

operator_home=$HOME

action=${1:-status}
shift || true

state_dir=${DEARMACHINE_STATE_DIR:-${XDG_STATE_HOME:-$PWD/.state}/dearmachine-stack}
cache_dir=${DEARMACHINE_CACHE_DIR:-$state_dir/cache}
config_dir=${DEARMACHINE_STACK_CONFIG_DIR:-$state_dir/config}
podman_root=${DEARMACHINE_PODMAN_ROOT:-$state_dir/podman}
runtime_dir=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}
idmap_dir=${DEARMACHINE_IDMAP_DIR:-/usr/bin}
storage_conf=$config_dir/containers-storage.conf
compose_dir=${DEARMACHINE_COMPOSE_DIR:?DEARMACHINE_COMPOSE_DIR is required}
project=${DEARMACHINE_PROJECT_NAME:-dearmachine}
mode=${DEARMACHINE_STACK_MODE:-development}

export DEARMACHINE_STATE_DIR=$state_dir
export DEARMACHINE_CACHE_DIR=$cache_dir
export DEARMACHINE_STACK_CONFIG_DIR=$config_dir
export DEARMACHINE_CLIENT_HOME=${DEARMACHINE_CLIENT_HOME:-$state_dir/client-home}
export DEARMACHINE_CLIENT_CONFIG_DIR=${DEARMACHINE_CLIENT_CONFIG_DIR:-$DEARMACHINE_CLIENT_HOME/.dearmachine/config}
export DEARMACHINE_CLIENT_STATE_DIR=${DEARMACHINE_CLIENT_STATE_DIR:-$DEARMACHINE_CLIENT_HOME/.dearmachine/state}
export DEARMACHINE_CLIENT_RUN_DIR=${DEARMACHINE_CLIENT_RUN_DIR:-$DEARMACHINE_CLIENT_HOME/.dearmachine/run}
export DEARMACHINE_CLIENT_LOG_DIR=${DEARMACHINE_CLIENT_LOG_DIR:-$DEARMACHINE_CLIENT_HOME/.dearmachine/log}
export DEARMACHINE_CLIENT_AGENT_MANAGER_DIR=${DEARMACHINE_CLIENT_AGENT_MANAGER_DIR:-$DEARMACHINE_CLIENT_HOME/.dearmachine/agent-manager}
export DEARMACHINE_MACHTIANI_DIR=${DEARMACHINE_MACHTIANI_DIR:-$DEARMACHINE_CLIENT_HOME/.machtiani}
export DEARMACHINE_PROJECT_DIR=${DEARMACHINE_PROJECT_DIR:-}
export DEARMACHINE_TOOLS_DIR=${DEARMACHINE_TOOLS_DIR:-$state_dir/tools}
export DEARMACHINE_BACKEND_ENVIRONMENT_FILE=${DEARMACHINE_BACKEND_ENVIRONMENT_FILE:-$DEARMACHINE_CLIENT_HOME/.config/dearmachine/backends.env}
export DEARMACHINE_PROJECT_NAME=$project
export CONTAINERS_STORAGE_CONF=$storage_conf
export HOME=${DEARMACHINE_PODMAN_HOME:-$config_dir/home}
export XDG_CONFIG_HOME=$HOME/.config
export XDG_RUNTIME_DIR=$runtime_dir
export PATH="$idmap_dir:$PATH"

# Validate before creating state or asking Podman to start a worker. Read-only
# status/down actions remain available for recovery from a bad configuration.
case $action in
  create|up|rebuild|config)
    boundary_check=${DEARMACHINE_BOUNDARY_CHECK:-$(dirname "${BASH_SOURCE[0]}")/container-boundary.py}
    boundary_args=(--native-home "$operator_home")
    if [[ $action == create ]]; then
      inbox_value=false
      for argument in "$@"; do
        if [[ $inbox_value == true ]]; then
          boundary_args+=(--create-inbox "$argument")
          inbox_value=false
        else
          case $argument in
            --inbox) inbox_value=true ;;
            --inbox=*) boundary_args+=(--create-inbox "${argument#--inbox=}") ;;
          esac
        fi
      done
    fi
    DEARMACHINE_CONTAINER_PROJECT=$(python3 "$boundary_check" "${boundary_args[@]}")
    export DEARMACHINE_CONTAINER_PROJECT
    ;;
esac

policy_conf=$XDG_CONFIG_HOME/containers/policy.json
test_service=$DEARMACHINE_TOOLS_DIR/test-service.sh

mkdir -p \
  "$state_dir" "$cache_dir" "$config_dir" "$podman_root" \
  "$DEARMACHINE_CLIENT_HOME" \
  "$DEARMACHINE_CLIENT_CONFIG_DIR" \
  "$DEARMACHINE_CLIENT_STATE_DIR" \
  "$DEARMACHINE_CLIENT_RUN_DIR" \
  "$DEARMACHINE_CLIENT_LOG_DIR" \
  "$DEARMACHINE_CLIENT_AGENT_MANAGER_DIR" \
  "$DEARMACHINE_MACHTIANI_DIR" \
  "$DEARMACHINE_CLIENT_HOME/.config" \
  "$DEARMACHINE_CLIENT_HOME/.cache" \
  "$DEARMACHINE_CLIENT_HOME/.local/state" \
  "$DEARMACHINE_TOOLS_DIR" "$runtime_dir/dearmachine-podman" \
  "$(dirname "$policy_conf")"

if [[ -n $DEARMACHINE_PROJECT_DIR ]]; then
  mkdir -p "$DEARMACHINE_PROJECT_DIR"
fi

if [[ ! -f $storage_conf ]]; then
  cat >"$storage_conf" <<EOF
[storage]
driver = "overlay"
runroot = "$runtime_dir/dearmachine-podman"
graphroot = "$podman_root"

[storage.options.overlay]
mount_program = "$DEARMACHINE_FUSE_OVERLAYFS"
EOF
fi

if [[ ! -f $policy_conf ]]; then
  cat >"$policy_conf" <<'EOF'
{"default":[{"type":"insecureAcceptAnything"}]}
EOF
fi

compose_files=(-f "$compose_dir/compose.yaml")
case $mode in
  development) ;;
  production) compose_files+=(-f "$compose_dir/compose.production.yaml") ;;
  test)
    compose_files+=(-f "$compose_dir/compose.test.yaml")
    if [[ ! -f $test_service ]]; then
      cat >"$test_service" <<'EOF'
#!/bin/sh
set -eu
dearmachine --help
printf '%s\n' "$$" >"$DEARMACHINE_PIDFILE"
readyfile=$(dirname "$DEARMACHINE_PIDFILE")/dearmachine.ready
printf '%s\n' "$$" >"$readyfile"
echo "DearMachine credential-free Compose test service ready"
trap 'rm -f "$readyfile" "$DEARMACHINE_PIDFILE"; exit 0' TERM INT
while :; do
  sleep 60 &
  wait $! || true
done
EOF
      chmod 0700 "$test_service"
    fi
    ;;
  *) echo "invalid DEARMACHINE_STACK_MODE: $mode" >&2; exit 2 ;;
esac

compose() {
  [[ -n $DEARMACHINE_PROJECT_DIR ]] || {
    echo "DEARMACHINE_PROJECT_DIR is required for Compose actions" >&2
    return 1
  }
  podman-compose "${compose_files[@]}" --project-name "$project" "$@"
}

container_id() {
  podman ps -aq \
    --filter "label=io.podman.compose.project=$project" \
    --filter "label=io.podman.compose.service=dearmachine" | head -n 1
}

preflight() {
  if [[ ${DEARMACHINE_TEST_SKIP_HOST_PREFLIGHT:-0} == 1 ]]; then
    podman info >/dev/null
    return
  fi
  [[ -r /sys/fs/cgroup/cgroup.controllers ]] || {
    echo "cgroup v2 is required" >&2
    return 1
  }
  [[ $(< /proc/sys/user/max_user_namespaces) -gt 0 ]] || {
    echo "rootless user namespaces are disabled" >&2
    return 1
  }
  grep -q "^$(id -un):" /etc/subuid || {
    echo "missing subordinate UID range for $(id -un)" >&2
    return 1
  }
  grep -q "^$(id -un):" /etc/subgid || {
    echo "missing subordinate GID range for $(id -un)" >&2
    return 1
  }
  [[ -x $idmap_dir/newuidmap && -x $idmap_dir/newgidmap ]] || {
    echo "capability-enabled newuidmap and newgidmap are required under $idmap_dir" >&2
    return 1
  }
  if [[ ! -S $runtime_dir/bus ]] && \
      systemctl --user show-environment >/dev/null 2>&1; then
    echo "warning: XDG_RUNTIME_DIR=$runtime_dir has no bus socket; use the real per-user XDG_RUNTIME_DIR (/run/user/$(id -u)) so rootless Podman networking can start" >&2
  fi
  podman info >/dev/null
}

load_image() {
  podman load --input "$DEARMACHINE_IMAGE_ARCHIVE" >/dev/null
}

unload_image() {
  local image=${DEARMACHINE_IMAGE:-localhost/dearmachine:nix}
  if podman image inspect "$image" >/dev/null 2>&1; then
    podman image rm --force "$image" >/dev/null
  fi
}

manage_secret() {
  local command=${1:-status}
  local name=dearmachine_agentmail_api_key
  local file mode
  shift || true

  case $command in
    status)
      [[ $# -eq 0 ]] || {
        echo "usage: dearmachine-stack secrets status" >&2
        return 2
      }
      if podman secret inspect "$name" >/dev/null 2>&1; then
        printf '%s\tpresent\n' "$name"
      else
        printf '%s\tmissing\n' "$name"
      fi
      ;;
    sync|rotate)
      [[ ${1:-} == --file && -n ${2:-} && $# -eq 2 ]] || {
        echo "usage: dearmachine-stack secrets $command --file FILE" >&2
        return 2
      }
      file=$2
      [[ -f $file && ! -L $file && -r $file ]] || {
        echo "secret input must be a readable regular file, not a symlink: $file" >&2
        return 1
      }
      [[ $(stat -c %u "$file") -eq $(id -u) ]] || {
        echo "secret input must be owned by the current user: $file" >&2
        return 1
      }
      mode=$(stat -c %a "$file")
      (( (8#$mode & 8#077) == 0 )) || {
        echo "secret input must not be accessible by group or other: $file" >&2
        return 1
      }
      [[ -s $file ]] || {
        echo "secret input is empty: $file" >&2
        return 1
      }
      podman secret create --replace "$name" - <"$file" >/dev/null
      printf '%s synchronized\n' "$name"
      ;;
    remove)
      [[ $# -eq 0 ]] || {
        echo "usage: dearmachine-stack secrets remove" >&2
        return 2
      }
      if podman secret inspect "$name" >/dev/null 2>&1; then
        podman secret rm "$name" >/dev/null
      fi
      ;;
    *)
      echo "usage: dearmachine-stack secrets <status|sync|rotate|remove>" >&2
      return 2
      ;;
  esac
}

require_runtime_boundary() {
  [[ $mode == test ]] && return 0
  [[ -x $DEARMACHINE_TOOLS_DIR/machtiani ]] || {
    echo "missing executable boundary: $DEARMACHINE_TOOLS_DIR/machtiani" >&2
    echo "place a Linux-compatible executable or Nix-store symlink there" >&2
    return 1
  }
  if [[ $mode == production ]]; then
    [[ -f $DEARMACHINE_BACKEND_ENVIRONMENT_FILE && \
       ! -L $DEARMACHINE_BACKEND_ENVIRONMENT_FILE && \
       -r $DEARMACHINE_BACKEND_ENVIRONMENT_FILE ]] || {
      echo "backend environment must be a readable regular file, not a symlink: $DEARMACHINE_BACKEND_ENVIRONMENT_FILE" >&2
      return 1
    }
    [[ $(stat -c %u "$DEARMACHINE_BACKEND_ENVIRONMENT_FILE") -eq $(id -u) ]] || {
      echo "backend environment must be owned by the current user: $DEARMACHINE_BACKEND_ENVIRONMENT_FILE" >&2
      return 1
    }
    local backend_mode
    backend_mode=$(stat -c %a "$DEARMACHINE_BACKEND_ENVIRONMENT_FILE")
    (( (8#$backend_mode & 8#077) == 0 )) || {
      echo "backend environment must not be accessible by group or other: $DEARMACHINE_BACKEND_ENVIRONMENT_FILE" >&2
      return 1
    }
  fi
}

wait_healthy() {
  local cid health
  cid=$(container_id)
  [[ -n $cid ]] || return 1
  for _ in $(seq 1 60); do
    podman healthcheck run "$cid" >/dev/null 2>&1 || true
    health=$(podman inspect \
      --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' \
      "$cid")
    case $health in
      healthy) return 0 ;;
      unhealthy|exited|stopped) return 1 ;;
    esac
    sleep 1
  done
  return 1
}

require_production_secret() {
  [[ $mode != production ]] || \
    podman secret inspect dearmachine_agentmail_api_key >/dev/null 2>&1 || {
      echo "missing Podman secret: dearmachine_agentmail_api_key" >&2
      return 1
    }
}

case $action in
  preflight) preflight ;;
  config) compose config ;;
  load)
    preflight
    load_image
    ;;
  unload)
    unload_image
    ;;
  image)
    preflight
    podman image inspect "${DEARMACHINE_IMAGE:-localhost/dearmachine:nix}"
    ;;
  run)
    preflight
    load_image
    podman run --rm "${DEARMACHINE_IMAGE:-localhost/dearmachine:nix}" "$@"
    ;;
  create)
    [[ $mode == production ]] || {
      echo "dearmachine-stack create requires DEARMACHINE_STACK_MODE=production" >&2
      exit 2
    }
    [[ $# -gt 0 ]] || {
      echo "usage: dearmachine-stack create --email ADDRESS (--new-inbox --transport ID | --inbox SELECTOR [--transport ID])" >&2
      exit 2
    }
    require_runtime_boundary
    preflight
    require_production_secret
    load_image
    [[ -z $(container_id) ]] || {
      echo "dearmachine-stack create requires the project stack to be down" >&2
      exit 1
    }
    compose run --rm --no-deps dearmachine \
      up --create "$@" \
      --project "$DEARMACHINE_CONTAINER_PROJECT" \
      --config /home/dearmachine/.dearmachine/config/dearmachine.toml \
      --agent-bin /opt/dearmachine/bin/machtiani \
      --entry-point-repo "${DEARMACHINE_ENTRY_POINT_REPO:-}" \
      --entry-point-prompt "${DEARMACHINE_ENTRY_POINT_PROMPT:-/home/dearmachine/.dearmachine/entrypoint/main/documentation/update-prompt-template.md}" \
      --poll-interval "${DEARMACHINE_POLL_INTERVAL:-10s}" \
      --once \
      --verbose
    ;;
  up)
    require_runtime_boundary
    preflight
    require_production_secret
    load_image
    compose up -d
    wait_healthy
    ;;
  rebuild)
    require_runtime_boundary
    preflight
    require_production_secret
    load_image
    compose up -d --force-recreate
    wait_healthy
    ;;
  wait) wait_healthy ;;
  health)
    cid=$(container_id)
    [[ -n $cid ]]
    podman healthcheck run "$cid" >/dev/null
    podman inspect --format '{{.State.Health.Status}}' "$cid"
    ;;
  status) compose ps ;;
  stop) compose stop ;;
  exec) compose exec -T dearmachine "$@" ;;
  logs) compose logs "$@" ;;
  container-logs)
    cid=$(container_id)
    [[ -n $cid ]] || {
      echo "no dearmachine container found for project $project" >&2
      exit 1
    }
    podman logs "$@" "$cid"
    ;;
  containers)
    podman ps -a "$@" \
      --filter "label=io.podman.compose.project=$project"
    ;;
  poll-ready)
    cid=$(container_id)
    [[ -n $cid ]] || exit 1
    podman logs "$cid" 2>&1 | grep -q 'poll: [0-9][0-9]* unread messages'
    ;;
  secrets)
    preflight
    manage_secret "$@"
    ;;
  down) compose down ;;
  *)
    echo "usage: dearmachine-stack <preflight|config|load|unload|image|run|create|up|rebuild|wait|health|status|stop|exec|logs|container-logs|containers|poll-ready|secrets|down>" >&2
    exit 2
    ;;
esac
