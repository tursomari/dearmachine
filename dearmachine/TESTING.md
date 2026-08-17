# Testing the DearMachine Client

The DearMachine Client uses fast component tests to exercise the real application
orchestration without live credentials or network services. Run commands from
this directory:

```bash
cd dearmachine
go test ./...
```

The module requires Go 1.23, CGO, and a C compiler because tests use the real
SQLite driver. The AgentMail SDK is resolved at the version declared in
`go.mod`; the module has no local replacement directive.

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
go test -coverprofile=/tmp/dearmachine.cover ./...
go tool cover -func=/tmp/dearmachine.cover
go tool cover -html=/tmp/dearmachine.cover -o /tmp/dearmachine-cover.html
```

Run one test by exact name:

```bash
go test ./internal/client \
  -run '^TestInterruptedMessageReplaysOnceWithoutSequenceGap$' \
  -count=1 -v
```

Use `-count=1` when a fresh, uncached run matters.

## Test approach

The suite favors behavior-level component tests over isolated mocks:

- `httptest.Server` implements a local AgentMail REST fake while production
  mailbox code still uses the AgentMail Go SDK.
- A separate `httptest.Server` covers the OpenMail REST contract, including
  unread-thread polling, reply idempotency and payloads, read acknowledgement,
  attachment limits, redirect confinement, and correspondent allow-lists.
  Default tests inject an offline key and endpoint; they never read an operator
  OpenMail credential or contact `api.openmail.sh`.
- `internal/client/testdata/fake-agent.sh` is executed as a real
  subprocess. It simulates `sync`, `run`, and `session show`, and captures
  arguments, environment, prompts, and final-answer files.
- Tests use real temporary SQLite databases and reopen them for restart and
  recovery scenarios.
- `t.TempDir()` and test-only environment variables keep runs isolated and
  credential-free.
- Follow-up tests verify that AgentMail `extracted_text` supplies only the new
  user contribution while the existing machtiani session supplies history.

The automated suite is split by responsibility:

- `cmd/dearmachine/main_test.go` covers CLI parsing, construction, signals,
  and command dispatch.
- `internal/client/app_test.go` covers primary email/session workflows
  and supplies the shared AgentMail fake.
- `internal/client/agentmail_test.go` covers AgentMail error contracts.
- `internal/client/agent_test.go` covers subprocess and status failures.
- `internal/client/store_test.go` covers SQLite migrations and durable
  state invariants.
- `internal/synctrigger/detection_test.go` covers the internal-README commit
  boundary, first-commit fallback, session ordering, and the two-session
  threshold.
- `internal/synctrigger/orchestrate_test.go` covers fork/run/delete/sync order,
  failed-run cleanup, checkpoint persistence, no-op suppression, and retry
  eligibility after sync failure.

Test functions follow Go's `TestBehaviorDescription` naming convention. The
shared application rig remains in `app_test.go`; direct tests use smaller
purpose-built helpers. The HTTP fake and shell fixture support injected API
errors, malformed responses, command failures, missing output, and delays.

The broader local `.scratch/docs_staging/device_client_test_suite.md` is a
non-versioned pseudocode scenario catalog, not a list of tests that all exist
today. Add its cases to the executable suite as the corresponding features are
built.

Live AgentMail and real `machtiani` checks are deliberately excluded from
`go test ./...`. Keep any future live smoke suite opt-in and credential-gated
so the default test command remains deterministic and safe.

`internal/entrypoint/bootstrap_test.go` verifies the two-stage seed boundary,
command order, internal-README snapshots, existing-repository preservation,
non-empty-directory rejection, and explicit incomplete-bootstrap state. The
neutral skeleton test also prevents DearMachine-specific framing from entering
the first sync.

The shared provisioning, isolation, evidence, and teardown rules for live tests
are in
[`runbooks/testing/temporary-instance.md`](./runbooks/testing/temporary-instance.md).
Give
[`runbooks/testing/disposable-instance.md`](./runbooks/testing/disposable-instance.md)
to a capable local agent
for the ordinary end-to-end email lifecycle or local skip/unskip exercise.

For a live test of Forge, Codex, and ordered fallback through the complete
email lifecycle, give
[`runbooks/testing/live-backends.md`](./runbooks/testing/live-backends.md) to a capable local
agent. It is a human-guided agent prompt, not an executable test script, and it
includes the required Forge logout and login gates.

For a live evaluation of the rolling one-session checkpoint, valid
documentation no-ops, durable entry-point updates, and internal-README sync,
give [`runbooks/testing/update-sync.md`](./runbooks/testing/update-sync.md) to a capable
local agent. Its priority natural-user prompts and optional explicit controls
support sensitivity comparison across revisions without turning the live
lifecycle into a brittle script.
