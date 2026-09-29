# Claude adapter

The built-in `claude` backend runs Claude Code in print mode with a text request
on stdin and verbose stream-JSON output. Install the CLI with
`curl -fsSL https://claude.ai/install.sh | bash` and put `claude` on PATH.

The adapter uses `--permission-mode bypassPermissions` to allow headless tool
calls without interactive approvals, following OMP's `--auto-approve` behavior.
Because it inherits the user's host permissions, the catalog requires explicit
opt-in, as it does for `codex-yolo`.

Model selection uses `DEARMACHINE_CLAUDE_MODEL`, then `ANTHROPIC_MODEL`, then
`sonnet`. Names are passed verbatim to `--model`. Credentials use the child
process's inherited environment (for example, `ANTHROPIC_API_KEY`, or
`ANTHROPIC_AUTH_TOKEN` with `ANTHROPIC_BASE_URL`); there is no custom-backend
credential injection. An existing native session in the ticket's `meta.json`
is passed to `--resume`. A fresh ticket starts a fresh session.

The parser captures the first top-level session ID, ignores malformed lines,
and uses the terminal result as the reply. Error results fail even when their
subtype is `success`, as happens on authentication failure. An interrupted
stream retains accumulated assistant text for diagnostics but returns an error
because progress text alone does not prove completion. The manager consumes
stdout before checking the CLI exit status.

## Credential-free checks

From `dearmachine/`, run `go test ./...`. The normal suite includes
parser, launch/model/resume, catalog, and fake-CLI auth-failure lifecycle tests.

## One containerized OpenRouter call

The opt-in `claude_live` test invokes `Prepare`, sends a prompt on stdin, runs
the real CLI once with a one-turn limit, and consumes its stdout with the adapter.
It requires a successful terminal reply, an exit code of zero, and a session ID.
This is a focused adapter integration test, not an email or tool-use exercise.

Set `CLAUDE_BINARY` to the resolved Linux CLI binary and `CLAUDE_KEY_FILE`
to a private mode-`0600` file containing only the test API key. From the
repository root, export a committed source snapshot on the host; never mount
the checkout or its Git metadata into the container. No credential values are passed in Docker arguments or printed. The key
file is mounted read-only and the test adds its value only to the CLI child's
environment. Run as the key file's owner, with an isolated temporary home.

```bash
: "${CLAUDE_BINARY:?set an absolute path to the Linux CLI binary}"
: "${CLAUDE_KEY_FILE:?set an absolute path to the private test key file}"
claude_source=$(mktemp -d "${TMPDIR:-/tmp}/claude-source.XXXXXX")
trap 'rm -rf -- "$claude_source"' EXIT
git archive HEAD | tar -x -C "$claude_source"
docker run --rm --user "$(id -u):$(id -g)" \
  --cap-drop ALL --security-opt no-new-privileges \
  --mount "type=bind,src=$claude_source,dst=/src,readonly" \
  --mount "type=bind,src=$CLAUDE_BINARY,dst=/usr/local/bin/claude,readonly" \
  --mount "type=bind,src=$CLAUDE_KEY_FILE,dst=/run/secrets/openrouter-key,readonly" \
  -e HOME=/tmp/claude-test-home -e GOCACHE=/tmp/go-cache -e GOPATH=/tmp/go \
  -e GOFLAGS=-buildvcs=false \
  -e DEARMACHINE_CLAUDE_LIVE_KEY_FILE=/run/secrets/openrouter-key \
  -w /src/dearmachine golang:1.26.8-bookworm bash -c 'set -eu
    umask 022
    mkdir -p "$HOME"
    go test ./...
    go test -tags claude_live ./internal/agentmanager -run "^TestClaudeLive$" -count=1 -v'
```

The driver sets `ANTHROPIC_BASE_URL=https://openrouter.ai/api` and
`--model z-ai/glm-5.3-flash`. It discards stderr and reports only validation
booleans and the exit code on failure, keeping raw provider diagnostics private.

## Validation recorded on 2026-09-07

- `go test ./...` passed on the host (Go 1.23.0, umask 022) and in
  `golang:1.24-bookworm` as UID/GID 1000. The initial host run under a restrictive
  umask failed two existing public-file permission tests; using 022 resolved both.
- One live CLI invocation using mounted Claude Code 2.1.202 passed in that
  container in 6.28 seconds: exit 0, successful terminal result, native session
  captured, and exact reply `CLAUDE_ADAPTER_OK`.
- The container mounted the checkout and resolved CLI binary read-only, plus
  only the required OpenRouter key file. No host home or CLI auth configuration
  was mounted. The container was automatically removed after completion.
