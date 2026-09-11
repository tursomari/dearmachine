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
custom_backend=$PROJECT_ROOT/docs/custom-backend-guide.md
readme=$PROJECT_ROOT/README.md

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
grep -F 'Live Scenario Evaluation (LSE)' "$temporary" >/dev/null
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
grep -F -- 'provisions and registers a real randomized' "$multi_pair" >/dev/null
for transport in AgentMail OpenMail Sendmux; do
  grep -F -- "$transport" "$multi_pair" >/dev/null
done
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
for contract in \
  'source change or local Agent Manager build is required' \
  'Do not hard-code temporary directories or' \
  'Custom backends are configured directly' \
  'The preferred completion path is a nonempty final response' \
  'do not compile another copy from the source' \
  'internal built-in-backend defaults or narrate' \
  'Do not add another conceptual permission question'; do
  grep -F -- "$contract" "$custom_backend" >/dev/null
done
if grep -F -- 'The very last line of stdin is always a close' "$custom_backend"; then
  echo 'custom backend guide still documents the retired CLOSE-line protocol' >&2
  exit 1
fi
grep -F '### 3. Choose an email transport and start' "$readme" >/dev/null
for transport in agentmail openmail sendmux; do
  grep -F -- "--transport $transport" "$readme" >/dev/null
done
# shellcheck disable=SC2016 # These README contracts require literal backticks.
for contract in \
  '| AgentMail | `AGENTMAIL_API_KEY_FILE` | `--new-inbox --transport agentmail` |' \
  '| OpenMail | `OPENMAIL_API_KEY_FILE` | `--new-inbox --transport openmail` |' \
  '| Sendmux | `SENDMUX_API_KEY_FILE` | `--new-inbox --transport sendmux` |'; do
  grep -F -- "$contract" "$readme" >/dev/null
done
if grep -F 'currently adopt an inbox that already exists' "$readme"; then
  echo 'public README must present new-inbox provisioning for every transport' >&2
  exit 1
fi
if grep -F 'DEARMACHINE_LIVE_' "$readme"; then
  echo 'public README must not expose live-test mutation gates' >&2
  exit 1
fi
if grep -Rin -- 'qse' \
  "$PROJECT_ROOT/docs" \
  "$PROJECT_ROOT/dearmachine/runbooks"; then
  echo 'live-scenario documentation must use LSE terminology, not QSE' >&2
  exit 1
fi
if grep -F -- '--agent-manager /bin/agent-manager' "$PROJECT_ROOT/dearmachine/runbooks/testing/continuous-intake.md"; then
  echo 'container protocol must use packaged Agent Manager through the client PATH' >&2
  exit 1
fi

if grep -F 'disable: true' "$PROJECT_ROOT/deploy/compose/compose.test.yaml"; then
  echo 'test overlay must inherit the production PID healthcheck' >&2
  exit 1
fi

for credential in AGENTMAIL_API_KEY_FILE OPENMAIL_API_KEY_FILE SENDMUX_API_KEY_FILE; do
  grep -F "$credential: /run/secrets/dearmachine_agentmail_api_key" \
    "$PROJECT_ROOT/deploy/compose/compose.production.yaml" >/dev/null
done

printf 'runbook contract tests passed\n'
