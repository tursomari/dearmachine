# Testing DearMachine

This is the canonical entrypoint for every DearMachine test suite. Automated
checks, Nix packaging checks, and live scenario evaluations are owned here.
More specialized runbooks remain beside the code they exercise and are linked
from this document.

Run commands from the repository root unless a section says otherwise.

## Recommended starting checks

For an ordinary DearMachine change, start with the deterministic Go suite:

```console
cd dearmachine
go test ./...
```

Before landing a DearMachine branch, run the repository-wide Nix gate from the
repository root. It includes the Go suite and the platform-appropriate build,
packaging, lifecycle, contract, and shell checks:

```console
nix flake check
```

Add a Live Scenario Evaluation (LSE) only when the change reaches a real email
transport, backend, concurrency boundary, deployed service, or update path.
Use the narrowest applicable protocol from the catalogue below. Product-wide
installer verification belongs to the Machtiani Installer QSE and is selected
through the umbrella testing guide; it is not duplicated here.

## Automated Go suites

The default suite uses local HTTP fakes, a subprocess agent fixture, and
temporary SQLite databases. It is credential-free and does not contact live
mail or model providers.

| Purpose | Command from `dearmachine/` |
| --- | --- |
| All package tests | `go test ./...` |
| Race detection | `go test -race ./...` |
| Static analysis | `go vet ./...` |
| Package coverage | `go test -cover ./...` |
| Coverage profile | `go test -coverprofile=/tmp/dearmachine.cover ./...` |
| One fresh test | `go test ./path/to/package -run '^TestName$' -count=1 -v` |

The module requires Go 1.23, CGO, and a C compiler because the tests use the
real SQLite driver. The AgentMail SDK is resolved from `go.mod`; there is no
local replacement directive.

The suite is organized by responsibility:

- `cmd/dearmachine` covers CLI parsing, construction, signals, dispatch, and
  status views separating crash recovery from login/reboot configuration,
  consent-independent read-only probes (including unavailable/inconclusive
  observations), and labels distinguishing authorized senders from inboxes.
- `internal/client` covers orchestration, AgentMail, OpenMail, Sendmux,
  subprocess behavior, persistence, recovery, authorization, and message
  normalization.
- `internal/entrypoint` covers the two-stage seed boundary and repository
  preservation.
- `internal/synctrigger` covers maintenance eligibility, fork/run/delete/sync
  ordering, checkpoints, and retries.

The default transport tests remain offline. AgentMail and OpenMail use local
HTTP servers, Sendmux uses a credential-free mailbox fake, and the agent fixture
runs as a real local subprocess. Live provider behavior is intentionally kept
out of `go test ./...`.

For a detailed coverage report:

```console
cd dearmachine
go test -coverprofile=/tmp/dearmachine.cover ./...
go tool cover -func=/tmp/dearmachine.cover
go tool cover -html=/tmp/dearmachine.cover -o /tmp/dearmachine-cover.html
```

## Nix and deployment checks

`nix flake check` is the maintained aggregate gate. Its check attributes are
also independently runnable with
`nix build '.#checks.<system>.<attribute>'` when isolating a failure.

| Check attribute | Coverage | Environment |
| --- | --- | --- |
| `dearmachine` | Production package build | Nix sandbox |
| `go-tests` | Complete Go package suite | Credential-free |
| `smoke` | CLI help and rejection of the retired flag-only invocation | Credential-free |
| `install-smoke` | Installer output and initial directory layout | Disposable Nix build directory |
| `image` | OCI image build | Linux |
| `compose` | Compose artifact evaluation | Linux |
| `host-lifecycle` | Host lifecycle package build | Linux |
| `host-lifecycle-test` | Host lifecycle behavior | Linux sandbox |
| `stack-runtime-test` | Stack runtime, project mount/cwd, native state aliases, and shared-inbox rejection | Linux sandbox |
| `systemd-user-unit` | User-unit structure and contract | Linux sandbox |
| `runbook-contracts` | Commands and assumptions embedded in runbooks | Linux sandbox |
| `native-service-launcher` | Native service launch behavior | Linux sandbox |
| `native-service-lifecycle` | Native start, stop, and restart behavior | Linux sandbox |
| `shellcheck` | Maintained shell entrypoints and test harnesses | Linux sandbox |

The scripts under `tests/nix/` implement those checks. They can be useful for
diagnosis, but the Nix attributes are the canonical entrypoints because they
supply the expected dependencies and environment. In particular,
`test-host-podman-integration.sh` is an opt-in host integration diagnostic; it
requires rootless Podman and is not part of the ordinary sandboxed test ladder.

## Live Scenario Evaluations

LSEs are human-guided, credentialed protocols rather than executable test
scripts. They can create mailboxes, send messages, call model providers, alter
disposable service state, and incur charges. Every LSE must use the isolation,
evidence, authorization, and exact-cleanup rules in
[`temporary-instance.md`](dearmachine/runbooks/testing/temporary-instance.md).
That shared file is a reference, not a standalone test.

| Protocol | Use it for |
| --- | --- |
| [`disposable-instance.md`](dearmachine/runbooks/testing/disposable-instance.md) | Ordinary two-turn email lifecycle or local skip/unskip behavior |
| [`concurrent-sessions.md`](dearmachine/runbooks/testing/concurrent-sessions.md) | Sequential compatibility and concurrent independent threads |
| [`continuous-intake.md`](dearmachine/runbooks/testing/continuous-intake.md) | Intake during active work and turn-gated maintenance |
| [`multi-pair.md`](dearmachine/runbooks/testing/multi-pair.md) | Pair routing, isolation, allow sets, and provider-ID overlap |
| [`queued-grace-preemption.md`](dearmachine/runbooks/testing/queued-grace-preemption.md) | Same-poll queued work and grace-period preemption |
| [`stop-and-resume-preemption.md`](dearmachine/runbooks/testing/stop-and-resume-preemption.md) | New-message interruption and session continuation |
| [`claude-adapter.md`](dearmachine/runbooks/testing/claude-adapter.md) | Claude Code parser, ticket resume, and opt-in containerized provider probe |
| [`live-backends.md`](dearmachine/runbooks/testing/live-backends.md) | Forge, Codex, OMP, and ordered backend fallback |
| [`apple-mail-html-fallback.md`](dearmachine/runbooks/testing/apple-mail-html-fallback.md) | HTML-only Apple Mail normalization through the production container path |
| [`openmail-transport.md`](dearmachine/runbooks/testing/openmail-transport.md) | Isolated OpenMail provisioning, polling, reply, and acknowledgement |
| [`sendmux-transport.md`](dearmachine/runbooks/testing/sendmux-transport.md) | Isolated Sendmux two-turn continuation and scoped credentials |
| [`update-sync.md`](dearmachine/runbooks/testing/update-sync.md) | Rolling checkpoints, entry-point maintenance, and internal-README sync |

Do not substitute one LSE for another merely because both send email. Each
protocol states whether it proves a native diagnostic or the containerized
production path.

## Historical and non-test material

- `.scratch/docs_staging/device_client_test_suite.md`, when present in a local
  checkout, is an unversioned pseudocode scenario catalogue. It is not an
  executable suite and does not establish current coverage.
- Compatibility and legacy cases inside active Go or Nix suites remain normal
  regression coverage; “legacy” in a fixture name does not make the suite
  deprecated.
- The old nested testing URL at `dearmachine/TESTING.md` is retained only as a
  forwarding page. New testing guidance belongs in this root document.

## Maintaining this entrypoint

Add a new runnable suite or LSE here in the same change that introduces it.
Keep detailed operational instructions in the nearest specialized runbook,
and link them here rather than creating another competing entrypoint.

### Transient transport failures

The production transport factory wraps read operations and idempotent message
acknowledgements with at most six retries. Delays grow exponentially from one
second to a 30-second cap, with jitter between half and all of each delay.
Every new operation starts at the initial delay; successful polling carries no
failure penalty. Cancellation interrupts waits. Authentication, authorization,
validation, and other permanent failures return immediately. Exhausted retries
return the original failure to the existing daemon recovery policy.

Reply sends are attempted once. A response lost after delivery must pass through
the durable pending-message and receipt-recovery flow before another send is
considered; transport retries never blindly repeat a reply. The normal Go suite
covers classifications for all three providers, growth/cap/jitter, reset,
exhaustion, cancellation, and receipt recovery after an uncertain send.
