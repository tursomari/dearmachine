#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT=${PROJECT_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
operate=$PROJECT_ROOT/dearmachine/runbooks/operate-entrypoint-client.md
reinstall=$PROJECT_ROOT/dearmachine/runbooks/uninstall-reinstall.md
migrate=$PROJECT_ROOT/dearmachine/runbooks/migrate-live-state.md
spinup=$PROJECT_ROOT/dearmachine/runbooks/container-spin-up.md
temporary=$PROJECT_ROOT/dearmachine/runbooks/testing/temporary-instance.md

for contract in \
  '## Monitor active email sessions' \
  '## Stop gracefully and verify shutdown' \
  '## Abandon one stuck follow-up' \
  '## Restart and recovery verification'; do
  grep -F "$contract" "$operate" >/dev/null
done

grep -F '## Full reset or destructive reinstall' "$reinstall" >/dev/null
grep -F '**Preserve:**' "$reinstall" >/dev/null
grep -F '**Reset:**' "$reinstall" >/dev/null
grep -F '**Delete:**' "$reinstall" >/dev/null
grep -F 'lifecycle stops the stack' "$migrate" >/dev/null
grep -F 'post-migration' "$migrate" >/dev/null
grep -F 'test-host-podman-integration.sh' "$spinup" >/dev/null
grep -F '### Containerized production-path variant' "$temporary" >/dev/null
grep -F 'pending-to-processed transition' "$temporary" >/dev/null

if grep -F 'disable: true' "$PROJECT_ROOT/deploy/compose/compose.test.yaml"; then
  echo 'test overlay must inherit the production PID healthcheck' >&2
  exit 1
fi

printf 'runbook contract tests passed\n'
