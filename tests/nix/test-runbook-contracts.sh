#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT=${PROJECT_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
operate=$PROJECT_ROOT/dearmachine/runbooks/operate-entrypoint-client.md
reinstall=$PROJECT_ROOT/dearmachine/runbooks/uninstall-reinstall.md
spinup=$PROJECT_ROOT/dearmachine/runbooks/container-spin-up.md
temporary=$PROJECT_ROOT/dearmachine/runbooks/testing/temporary-instance.md
disposable=$PROJECT_ROOT/dearmachine/runbooks/testing/disposable-instance.md
multi_pair=$PROJECT_ROOT/dearmachine/runbooks/testing/multi-pair.md
openmail=$PROJECT_ROOT/dearmachine/runbooks/testing/openmail-transport.md
native=$PROJECT_ROOT/dearmachine/runbooks/native-install.md

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
grep -F 'test-host-podman-integration.sh' "$spinup" >/dev/null
grep -F '### Default containerized production path' "$temporary" >/dev/null
grep -F 'Live integration protocols use the containerized production path' \
  "$temporary" >/dev/null
grep -F 'without requiring systemd' "$temporary" >/dev/null
grep -F 'nix run .#dearmachine-stack -- up' "$temporary" >/dev/null
grep -F '### Native diagnostic exception' "$temporary" >/dev/null
grep -F 'operator_secrets=<mode-0600-operator-secrets-file>' "$temporary" >/dev/null
# shellcheck disable=SC2016 # The contract requires the literal variable reference.
grep -F 'literal reference `${DEEPSEEK_API_KEY}`' "$temporary" >/dev/null
grep -F 'running client, Agent' "$temporary" >/dev/null
grep -F 'machtiani, and its selected backend inherit it' "$temporary" >/dev/null
grep -F 'pending-to-processed transitions' "$temporary" >/dev/null
grep -F 'two inbound user turns map to one mct session with sequence 2' \
  "$disposable" >/dev/null
grep -F 'exactly two substantive replies arrive' "$disposable" >/dev/null
for contract in \
  '## Contract under test' \
  '## Creation and intent checks' \
  '## Single-daemon routing and state isolation' \
  '## Selection, restart, and scheduling' \
  '## Teardown and report'; do
  grep -F "$contract" "$multi_pair" >/dev/null
done
grep -F -- 'starts every pair. A repeatable' "$multi_pair" >/dev/null
grep -F -- 'email-or-uuid' "$multi_pair" >/dev/null
grep -F -- 'narrows only that invocation and never changes the registry' "$multi_pair" >/dev/null
grep -F -- 'provisions a real randomized AgentMail' "$multi_pair" >/dev/null
grep -F -- 'intentionally shares an inbox' "$multi_pair" >/dev/null
grep -F -- 'are unknown flags.' "$multi_pair" >/dev/null
grep -F -- 'on every daemon launch.' "$multi_pair" >/dev/null
grep -F -- 'schema version' "$multi_pair" >/dev/null
grep -F -- 'must equal 2.' "$multi_pair" >/dev/null
grep -F 'exactly two allowed inbound messages' "$openmail" >/dev/null
grep -F 'exactly two later outbound replies' "$openmail" >/dev/null
grep -F 'Native execution is the default' "$native" >/dev/null
grep -F 'dearmachine up --create' "$native" >/dev/null
grep -F 'dearmachine status' "$native" >/dev/null
grep -F 'up --foreground' "$native" >/dev/null
grep -F 'nix profile install .#dearmachine' "$native" >/dev/null
grep -F 'machtiani` and the backend commands' "$native" >/dev/null
if grep -F -- '--agent-manager /bin/agent-manager' "$PROJECT_ROOT/dearmachine/runbooks/testing/continuous-intake.md"; then
  echo 'container protocol must use packaged Agent Manager through the client PATH' >&2
  exit 1
fi

if grep -F 'disable: true' "$PROJECT_ROOT/deploy/compose/compose.test.yaml"; then
  echo 'test overlay must inherit the production PID healthcheck' >&2
  exit 1
fi

printf 'runbook contract tests passed\n'
