# Testing the Device Client

The Device Client uses fast component tests to exercise the real application
orchestration without live credentials or network services. Run commands from
this directory:

```bash
cd device-client
go test ./...
```

The module requires Go 1.23, CGO, and a C compiler because tests use the real
SQLite driver. The AgentMail SDK is currently resolved from
`../.state/agentmail-go` by the replacement in `go.mod`.

## Useful commands

Run the standard suite:

```bash
go test ./...
```

Run race detection and static checks:

```bash
go test -race ./...
go vet ./...
```

Print package coverage:

```bash
go test -cover ./...
```

Create a detailed coverage profile:

```bash
go test -coverprofile=/tmp/device-client.cover ./...
go tool cover -func=/tmp/device-client.cover
go tool cover -html=/tmp/device-client.cover -o /tmp/device-client-cover.html
```

Run one test by exact name:

```bash
go test ./internal/deviceclient \
  -run '^TestInterruptedMessageReplaysOnceWithoutSequenceGap$' \
  -count=1 -v
```

Use `-count=1` when a fresh, uncached run matters.

## Test approach

The suite favors behavior-level component tests over isolated mocks:

- `httptest.Server` implements a local AgentMail REST fake while production
  mailbox code still uses the AgentMail Go SDK.
- `internal/deviceclient/testdata/fake-mct-agent.sh` is executed as a real
  subprocess. It simulates `sync`, `run`, and `session show`, and captures
  arguments, environment, prompts, and final-answer files.
- Tests use real temporary SQLite databases and reopen them for restart and
  recovery scenarios.
- `t.TempDir()` and test-only environment variables keep runs isolated and
  credential-free.

The automated suite is split by responsibility:

- `cmd/device-client/main_test.go` covers CLI parsing, construction, signals,
  and command dispatch.
- `internal/deviceclient/app_test.go` covers primary email/session workflows
  and supplies the shared AgentMail fake.
- `internal/deviceclient/agentmail_test.go` covers AgentMail error contracts.
- `internal/deviceclient/mct_test.go` covers subprocess and status failures.
- `internal/deviceclient/store_test.go` covers SQLite migrations and durable
  state invariants.

Test functions follow Go's `TestBehaviorDescription` naming convention. The
shared application rig remains in `app_test.go`; direct tests use smaller
purpose-built helpers. The HTTP fake and shell fixture support injected API
errors, malformed responses, command failures, missing output, and delays.

The broader
[`device_client_test_suite.md`](../docs_staging/device_client_test_suite.md) is
a pseudocode scenario catalog, not a list of tests that all exist today. Add
its cases to the executable suite as the corresponding features are built.

Live AgentMail and real `mct-agent` checks are deliberately excluded from
`go test ./...`. Keep any future live smoke suite opt-in and credential-gated
so the default test command remains deterministic and safe.

For a live test of Forgecode, Codex, and ordered fallback through the complete
email lifecycle, give
[`LIVE_BACKEND_TESTING.md`](./LIVE_BACKEND_TESTING.md) to a capable local
agent. It is a human-guided agent prompt, not an executable test script, and it
includes the required Forge logout and login gates.
