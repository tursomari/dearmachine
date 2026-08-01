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

The current automated suite lives in
`internal/deviceclient/app_test.go`. Test functions follow Go's
`TestBehaviorDescription` naming convention; shared setup is provided by the
`testRig` helpers in that file. Current source coverage is strongest for the
primary email/session/recovery workflows. CLI wiring and many injected failure
paths remain roadmap work.

The broader
[`device_client_test_suite.md`](../docs_staging/device_client_test_suite.md) is
a pseudocode scenario catalog, not a list of tests that all exist today. Add
its cases to the executable suite as the corresponding features are built.

Live AgentMail and real `mct-agent` checks are deliberately excluded from
`go test ./...`. Keep any future live smoke suite opt-in and credential-gated
so the default test command remains deterministic and safe.
