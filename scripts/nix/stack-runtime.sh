#!/usr/bin/env bash
set -euo pipefail
umask 077

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
export DEARMACHINE_PROJECT_NAME=$project
export CONTAINERS_STORAGE_CONF=$storage_conf
export HOME=${DEARMACHINE_PODMAN_HOME:-$config_dir/home}
export XDG_CONFIG_HOME=$HOME/.config
export XDG_RUNTIME_DIR=$runtime_dir
export PATH="$idmap_dir:$PATH"

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
    # podman-compose interpolates the base file before applying this overlay.
    # Supply an unused value so the base file's strict development/production
    # substitution does not reject credential-free test mode.
    export DEARMACHINE_INBOX_ID=${DEARMACHINE_INBOX_ID:-credential-free-test}
    compose_files+=(-f "$compose_dir/compose.test.yaml")
    if [[ ! -f $test_service ]]; then
      cat >"$test_service" <<'EOF'
#!/bin/sh
set -eu
dearmachine --help
printf '%s\n' "$$" >"$DEARMACHINE_PIDFILE"
echo "DearMachine credential-free Compose test service ready"
trap 'rm -f "$DEARMACHINE_PIDFILE"; exit 0' TERM INT
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
    echo "usage: dearmachine-stack <preflight|config|load|unload|image|run|up|rebuild|wait|health|status|stop|exec|logs|container-logs|containers|poll-ready|secrets|down>" >&2
    exit 2
    ;;
esac
