# DearMachine Development Roadmap

DearMachine is in pre-alpha. This roadmap orders engineering tracks by their
impact on a reliable local alpha; it is not a release-date commitment or a
cross-repository project-management backlog.

## Current baseline

The Go Device Client proves the core loop: poll AgentMail, map email threads to
durable `mct-agent` sessions in SQLite, recover interrupted work, and send
AnswerUser or AskUser replies. It is single-threaded and has passed two manual
live-test rounds. The automated suite currently has 14 passing tests and 71.2%
statement coverage.

Current behavior and test commands are documented in
[`device-client/README.md`](device-client/README.md) and
[`device-client/TESTING.md`](device-client/TESTING.md).

## 1. Harden the alpha test boundary

- Test CLI flags, required environment, dependency wiring, `--once`, and
  signal cancellation in `cmd/device-client`.
- Extend the AgentMail and fake-`mct-agent` harnesses with injected HTTP,
  subprocess, malformed-status, missing-output, and cancellation failures.
- Add direct SQLite tests for legacy migration, state transitions, duplicates,
  ordering, sequence conflicts, and reopen/recovery invariants.
- Convert applicable scenarios from the staged pseudocode test suite as their
  features land, prioritizing duplicates, timeout/recovery exhaustion, FIFO,
  preemption, and concurrency.
- Add CI for `go test ./...`, `go test -race ./...`, `go vet ./...`, and a
  non-regressing coverage floor.
- Add a separate opt-in smoke suite for a dedicated AgentMail test inbox and a
  real installed `mct-agent`.
- Split the growing `app_test.go` by responsibility while retaining its shared
  component-test rig.

## 2. Make local deployment repeatable

- Install and verify Go, `mct-agent`, and the Device Client in the initial
  alpha WSL environment.
- Replace or reproducibly provision the local `../.state/agentmail-go` module
  replacement before building on another machine.
- Define one supported configuration and secret-provisioning path, including
  mode-0600 API keys and an explicit project directory.
- Resolve foreground/background supervision cleanly, then provide reliable
  start, stop, status, restart, log, and boot-persistence behavior.
- Exercise process death, machine reboot, network loss, and recovery in a
  documented deployment smoke test.
- Add enough health reporting to distinguish a live process, a working poll
  loop, and successful `mct-agent` execution.

## 3. Close security and failure-handling gaps

- Enforce paired-sender authorization at the Device Client boundary as well as
  at the hosted transport.
- Add bounded subprocess timeouts, retry/backoff classification, and useful
  user-facing failure replies without duplicate execution.
- Validate message sizes and attachment policy before agent invocation.
- Add singleton/advisory locking so duplicate clients cannot share one
  database, pidfile, or agent session.
- Keep agent execution at the local user's permissions; do not introduce
  automatic elevation or unsafe approval behavior.

## 4. Add durable scheduling

- Introduce a per-thread durable FIFO so messages in one conversation remain
  ordered while independent threads can progress concurrently.
- Implement the specified preemption flow, including the queue-check debounce,
  durable stop cause, and no-text recovery behavior.
- Add a configurable global concurrency limit and deterministic scheduling
  tests with controllable clocks and subprocess gates.
- Preserve idempotent replies, sequence continuity, and restart recovery at
  every scheduling boundary.

## 5. Separate transport from orchestration

- Define the production transport interface around polling, thread history,
  reply dispatch, acknowledgements, and finalization.
- Move AgentMail-specific REST behavior behind an adapter without changing the
  application state machine.
- Test the adapter contract once and reuse it for the future self-hosted relay.
- Track the stronger delivery, authorization, idempotency, deletion, and
  attestation guarantees described by the relay specification.

## 6. Complete the product surface

- Replace spike-only flags with the intended configuration file and
  `machinemail up`, `status`, and `down` lifecycle.
- Add attachment handling with explicit limits and safe local staging.
- Implement AskUser expiry and clear operator/user recovery messages.
- Add structured logs and minimal diagnostics without retaining email content
  unnecessarily.
- Implement and verify the OpenPGP path only after the core delivery and local
  execution lifecycle is dependable.

## Alpha milestones

1. **Reliable local alpha:** CI, failure contracts, sender enforcement,
   timeouts, singleton protection, and repeatable supervision.
2. **Initial alpha deployment:** clean installation, boot persistence, live
   mail smoke test, and restart/reboot recovery in WSL.
3. **Multi-thread alpha:** durable per-thread FIFO, preemption, and bounded
   concurrency with executable scenario tests.
4. **Transport-ready alpha:** AgentMail behind a tested interface and a clear
   path to the self-hosted relay.

## Design references and documentation cleanup

- [`docs_staging/device_client_spec.md`](docs_staging/device_client_spec.md)
  describes the target Device Client, including unimplemented behavior. Keep it
  labeled as a target specification rather than current operational truth.
- [`docs_staging/device_client_test_suite.md`](docs_staging/device_client_test_suite.md)
  is the future scenario catalog. Keep it as design input and link implemented
  cases to executable tests instead of duplicating those cases in another doc.
- [`docs_staging/relay_email_spec_sanity_checked.md`](docs_staging/relay_email_spec_sanity_checked.md)
  remains the target relay capability profile and should stay separate from
  Device Client runtime documentation.
- `docs_staging/device-client-config.md` duplicates the Device Client README
  and implies a model must be supplied even though model selection is optional.
  Consolidate any still-useful credential/setup guidance into
  `device-client/README.md`, then retire the staging note.
- When the Device Client specification stabilizes against implemented code,
  promote it from `docs_staging/` to a normal documentation location. Until
  then, avoid presenting staged design documents as shipped behavior.
