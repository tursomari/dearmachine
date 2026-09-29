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

## macOS portability checks

On a Mac, run `nix flake check` and the ordinary Go suite above. The concierge
PTY tests use the same assertions on Linux and macOS. Native uninstall remains Linux-only. macOS LaunchAgents support explicit
service use and separately authorized startup at login, including after reboot;
they do not provide before-login or after-logout operation.

Run the native, credential-free service integration in an existing graphical
Mac session with a freshly built candidate binary:

```console
python3 tests/macos/launchd-lifecycle.py --binary /absolute/path/to/dearmachine
```

It uses a private temporary home, a unique service label, the real launchd
manager and native supervisor, and an offline child. It verifies start,
singleton ownership, restart, explicit stop, reversible login configuration,
loading the login plist, and exact cleanup. No provider, mailbox, real home,
logout, reboot, or unrelated service is used. Loading the plist simulates a
login boundary; it does not establish an actual logout/reboot or email result.
The deterministic `cmd/dearmachine/launchd_test.go` cases cover consent,
unsafe/unrelated files, preserved credentials, and conservative observations on
Linux as well as macOS.
Darwin process groups support normal supervised shutdown; unlike Linux,
abrupt supervisor death does not automatically kill its direct child.

A Linux host can compile both Darwin architectures with a macOS-targeting C
compiler and SDK. Set `MACOS_CC_AMD64` and `MACOS_CC_ARM64` to the corresponding
compiler commands, then run:

```console
tests/darwin-cross-compile.sh /absolute/path/to/disposable-artifacts
```

An optional second argument selects `amd64` or `arm64`. This builds the actual
SQLite-enabled commands and all test binaries. It does not execute them, verify
Nix packaging on a Mac, or establish installation support. Keep generated
artifacts outside the repository. Execute the binaries in a disposable macOS
guest with the matching source/test fixtures and ordinary-user permissions.

## Automated Go suites

### Confirmed native uninstall

On Windows, `cmd/dearmachine/concierge_windows_test.go` verifies that preserved
independent Machtiani project data does not block fresh DearMachine setup after
uninstall. Actual partial DearMachine state still requires recovery. Execute it
from the `dearmachine/` Go module with the pinned native Windows Go/CGO toolchain:
`powershell.exe -NoProfile -ExecutionPolicy Bypass -File ..\tests\windows\concierge.ps1`.
This compiles the actual Windows production files and the focused Windows test;
unrelated command-package tests contain Unix-only syscall fixtures. The Linux
suite cannot exercise this branch.

`cmd/dearmachine/uninstall_test.go` covers explicit terminal confirmation,
cancellation without mutation, refusal of confirmation bypasses, unsafe roots,
busy acquisition and independent Machtiani preservation. Service tests use a
fake runner to prove disable/stop ordering and refusal of unconfirmed shutdown;
they do not claim live systemd coverage. These tests belong to `go test ./...`.
The umbrella `tests/uninstall/run.py` gate supplies real candidate runtimes in
an offline disposable container and proves process shutdown, binary self-removal,
private-data deletion and preservation before container teardown. Its detailed
contract is in the umbrella `tests/uninstall/README.md`. Its separate `--systemd`
variant proves real service shutdown and unit/drop-in removal with a
container-local user manager, preserving independent services and lingering.

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

The module requires Go 1.26.8, CGO, and a C compiler because the tests use the
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
  preservation. Real-Git tests verify the preset workspace identity with an
  empty email, personal global defaults and signing enabled, interrupted setup,
  subsequent commits, strict Git object validation, and unchanged global and
  existing-repository settings. Model work and Git LFS are fixtures in this test.
- `internal/synctrigger` covers maintenance eligibility, fork/run/delete/sync
  ordering, checkpoints, and retries.
- `internal/machtianiconfig` covers current model selection, explicit run/sync
  flags, role defaults, and configuration changes between launches. Client
  follow-up/recovery tests verify forwarding those selections; sync-trigger
  tests verify cleanup and checkpoint recovery after model failures.

For the native model-selection boundary, build the umbrella-pinned Machtiani
harness and run this additional credential-free check from `dearmachine/`:

```console
DEARMACHINE_TEST_MACHTIANI=/absolute/path/to/machtiani go test ./internal/machtianiconfig -run '^TestNativeResumeOverridesRemovedAliases$' -count=1 -v
```

It uses disposable homes and projects, a loopback provider for real sync, and
native dry-run sessions. For each of the four roles it removes a historical
alias, confirms ordinary resume fails, and verifies DearMachine's explicit
flags allow both the original session and a temporary fork to resume. It also
checks that sync's explicit selection overrides an unavailable inherited
discovery alias. The ordinary suite skips this test when the executable is
not supplied; no installed configuration, real provider, email, or backend is
used. Dry-run success proves model resolution and resume, not live task output.

The default transport tests remain offline. AgentMail and OpenMail use local
HTTP servers, Sendmux uses credential-free REST, native JMAP and IMAP protocol fakes, and the agent fixture
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
| [`participant-approval.md`](dearmachine/runbooks/testing/participant-approval.md) | Authenticated guest invitations, independent private approvals, revocation, exact-model authority, and recovery |
| [`recipient-delivery.md`](dearmachine/runbooks/testing/recipient-delivery.md) | Cross-version multi-recipient delivery, provider-filter recovery, recipient roles, and adapter-neutral routing |
| [`forward-session-fork.md`](dearmachine/runbooks/testing/forward-session-fork.md) | Forward detection, confirmation, and clean session forks across provider threading behavior |
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

## Coordinated Nix updates

`cmd/dearmachine` tests the noninteractive updater handoff and stopped-client
inspection. `internal/supervisor` tests that update shutdown acknowledges the
request and releases ownership before replacement. The complete coordinated
release manager and source snapshot container test lives in the sibling
installer's `tests/managed-nix/run.sh`, documented in its `TESTING.md`.
The container uses an explicitly supplied candidate native CLI, never a command
resolved from the host installation.

## Guest authorization verification

[Guest operations](docs/guest-authorization.md) documents the supported commands
and the authentication policy and supported-provider limits.

[The guest verification guide](verification/guest/README.md) documents the
pinned Gobra/TLC runner, finite model, intentional counterexamples, production
policy correspondence and trusted boundaries. Run its credential-free container
checks alongside `go test ./internal/client -run '^Test(Guest|OutboundApproval)' -count=1`
in a credential-free container when the guest authorization lifecycle changes. These checks do not authenticate external
email providers or establish model obedience.

For a candidate source snapshot (including reviewed uncommitted changes), run:

```console
python3 scripts/test-guest-container.py --output /tmp/dearmachine-guest-check
```

The runner builds a disposable image from source-only files with pinned Go
1.24 tooling, downloads module dependencies during the build, then runs the
complete Go suite, race suite and vet with `--network none`, a read-only image,
and scratch state. It mounts no host state or credentials and removes its exact
image afterward. Source hashes and logs stay in the new output directory.
Use `--docker-host unix:///var/run/docker.sock` when needed. The host Nix gate
remains mandatory and checks the repository's own pinned production toolchain.


The runtime durable outbox gate is implemented for all paired shared replies.
`outbound_approval_test.go` exercises exact frozen text/HTML/files, owner-only
pending previews, authenticated issued revision-bound decisions, instruction
approval separation, recipient reduction/reapproval, accepted grant generations,
restart, uncertain-send reconciliation and revocation ordering. A completed turn
with `outbound-pending:` is only local outbox acceptance, not submission or
delivery; the outbox retains attachment bytes independently of staging files.
`outbound_recovery_test.go` covers known non-submission retries, durable budgets,
revocation before retry, lost responses, private hold notices, record isolation
and notice replay across restart. It also covers transient pending-state errors
without notices, independent preview/submission notices on the same revision,
scope suppression and upgrades from historical shared notice keys.
`outbound_scheduling_test.go` blocks an independent worker or maintenance lane
and requires an approved reply to submit through ongoing polling before that
lane finishes, with exact content/recipients and no replay duplicates. It also
checks that guest removal later in the same poll requires a new preview before
any guest-visible submission.
`outbound_decision_test.go` covers private
feedback and deferred uncertain decisions; `reply_submission_boundary_test.go`
checks adapter error classification, disabled AgentMail SDK retries, and
reused-TCP lost-response/redirect behavior that must not replay mutation bodies. Native
status tests verify visible holds without payload or token disclosure.

Pair DB `outbound_approvals` records expose state and hold reason for private
diagnostics. Missing/ambiguous/mismatched receipts stay held without blind
resends. Plain text matching normalizes only CRLF/LF; HTML must match the
provider HTML retained by adapters in `RawHTML` exactly, and files must match
metadata and bytes. Header-only yes/no decisions with unresolved references
during uncertain preview issuance remain unprocessed until receipt recovery.

Run the container suite above when the candidate source changes, including the
outbound race tests. Nix checks and live provider delivery validation remain
separate obligations. The unchanged `Outbound.tla` contract and these regressions
are not a Go refinement/composition proof. Use the participant-approval and
recipient-delivery protocols linked here to distinguish private preview,
approved submission and observed destination delivery. See the
[validation record](verification/guest/VALIDATION.md) for results and run scope.

Guest answer-recipient regressions live in `guest_reply_test.go` and
`reply_cc_test.go`. They cover owner/guest visibility, private approval prompts,
full-envelope receipt recovery, persisted retry recipients, and real SDK request
encoding for AgentMail, OpenMail JSON/multipart, and both Sendmux sending APIs.
`guest_permission_test.go` additionally covers migration from receive-only state,
per-direction ownership, shared references and uncertain outbound responses.
The recipient-delivery runbook owns the corresponding disposable live probes.

`guest_participation_test.go` exercises automatic To/CC grants, all managed
provider directions, repeated independent approvals, private owner continuations,
removal-token scope/replay, revocation/reinvitation and immutable request content.
`sender_auth_test.go` verifies real signatures with ephemeral keys and the real
AgentMail SDK over loopback TLS. It rejects spoofed or unsigned author/routing
fields, body tampering, fake verdicts, missing evidence and unsupported adapters.
The core Gobra contracts cover invitation, delivery, execution and recipient
eligibility; `Participation.tla` checks the abstract per-message workflow.
`Replacement.tla` checks polling/completion interleavings and conditional restart
progress; `participant_replacement_test.go` covers real subprocess-start repolls,
SQLite recovery with sent/unsent saved results, and narrowly scoped legacy repair.

`sendmux_jmap_test.go` exercises durable outgoing message/submission recovery after
lost responses and stale reads across restart, conditional-state contention, changed-envelope idempotency
rejection, reply ancestry, private approval references, attachments, credential
destination checks and queued-versus-delivered reporting. Scoped authenticated
approval correlation is covered by `message_references_test.go`.

Guest authentication exceptions are covered by `guest_auth_exception_test.go`:
private warnings, independent approval of every instruction, removal/reinvitation,
scoped authenticated decisions, restart, rate limits, lost-response receipt
recovery, content substitution and logging. `Participation.tla` and the six
production Gobra contracts distinguish verified identity from a remembered owner
risk exception. Use the guest verification runner above for this policy boundary;
notification transport effects remain implementation/LSE evidence.

## Native Windows development proof

Cross-compile the `internal/hostos`, `internal/supervisor`, and CGO-enabled
`internal/client` test binaries and run them in a disposable Windows guest as
an ordinary user. The umbrella's `tests/windows-native/primitives.ps1` selects
Windows ACL/locking, named-pipe and job lifecycle, pairing/SQLite, and guest
authorization-store cases. `TestGuestStoreScopeAndNoHistoricalMigration` uses
a filename with Unicode and URI punctuation to exercise SQLite URI escaping.
The historical CLI package test suite still contains Unix-only test code; this
proof does not claim full Windows Go test coverage.

`TestBackgroundTreeHasNoConsoleWindow` exercises a detached supervisor, its
managed worker, and an ordinary console grandchild. Every process must lack a
console window while output still reaches the supervisor's log. Checking only
window visibility under SSH is insufficient: the old launch allocated a hidden
window in that session but opened a visible terminal on the desktop. Run this
with `TestDetachedWorkerSurvivesShellAndRemainsOwned` to verify that windowless
launch preserves descendant ownership and cancellation.

`TestWindowsSupervisorSurvivesSessionJob` closes a launcher job with the same
kill-on-close and explicit-breakaway flags as Windows OpenSSH. The detached
supervisor must survive. Keep the worker ownership test above in the gate:
leaving a temporary session must not let task descendants escape their client.

Run the CGO-enabled client binary with
`'-test.run=^(TestSendmux|TestWindowsSendmux)'` for the native Sendmux gate.
Windows uses a private SQLite submission journal with PERSIST rollback and
EXTRA synchronization; Unix retains the existing atomic JSON journal.
The suite verifies lost responses, stale reads, concurrent retries, abrupt
writer termination, legacy migration, changed-envelope rejection, corrupt
state refusal, and private directory/file ACLs. Repeat
`TestSendmuxJMAPConcurrentRecoverySubmitsOnce` with `'-test.count=50'` to exercise
simultaneous first-use directory creation. Process termination is not a
physical power-loss test or a substitute for a live transport evaluation.

`TestWindowsSendmuxPowerCycleFixture` is an opt-in two-phase VM check and
skips during the ordinary suite. Use a separate disposable Windows guest,
an ordinary account, and synthetic data only. Set
`DM_SENDMUX_POWERCYCLE_DIRECTORY` to a new absolute directory and
`DM_SENDMUX_POWERCYCLE_PHASE=write`, then run only that test with verbose
output. After its `POWERCYCLE_READY` marker, abruptly terminate the exact
guest's hypervisor process without a graceful shutdown and restart its same
disk. Set the phase to `verify` and rerun the test against the same directory;
the uncertain email/submission attempt must survive. Never interrupt a human
IXE or shared guest for this check. The writer fails after ten minutes if no
restart occurs. This proves recovery after abrupt VM termination, not physical
host or storage power loss.

## Linux systemd lifecycle regression

In a disposable Linux VM with a real systemd user manager, provision a fresh
ordinary account and enable lingering for that account. Build the candidate
native CLI, then run as that user:

```sh
python3 tests/linux/systemd-lifecycle.py --binary /absolute/path/to/dearmachine
```

The test refuses preexisting Dear Machine state or services. It uses an offline
child under the production supervisor to exercise ownership transfer, the stable
launcher, duplicate starts, crash recovery, explicit stop, restart and persistence
settings. It cleans up its service by default. With `--leave-running`, a successful
run leaves only its own fixture enabled and writes `~/lifecycle-reboot.json` with
the boot ID and process identity. Keep the binary outside temporary directories
that the guest clears at boot. After a separately authorized VM reboot, verify a
changed boot ID, active user service and live daemon before any login by the
fixture user. Clean up using that user's native `down`, `persistence off`, and
`systemd off` commands. Live installation and email/model requests remain
separate checks; the offline child does not claim to cover them.
