# Dear Machine, Development Roadmap

Dear Machine, is in pre-alpha. This roadmap orders engineering tracks by its
impact on a reliable local alpha; it is not a release-date commitment or a
cross-repository project-management backlog.

## Current baseline

The Go DearMachine Client proves the core loop: poll AgentMail, map email threads to
durable `mct-agent` sessions in SQLite, recover interrupted work, and send
AnswerUser or AskUser replies. It is single-threaded and has passed two manual
live-test rounds. The automated suite currently has 14 passing tests and 71.2%
statement coverage.

Current behavior and test commands are documented in
[`dearmachine/README.md`](dearmachine/README.md) and
[`dearmachine/TESTING.md`](dearmachine/TESTING.md).

## 1. Harden the alpha test boundary

- Test CLI flags, required environment, dependency wiring, `--once`, and
  signal cancellation in `cmd/dearmachine`.
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

- Install and verify Go, `mct-agent`, and the DearMachine Client in the initial
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
- The root Nix flake now provides the `dearmachine` package, `install` app,
  build and help smoke checks, and a CGO-enabled Go-suite check.
- The Stage 2 install path now provides `localhost/dearmachine:nix`, the
  `dearmachine` Compose service, an isolated rootless `dearmachine-stack`
  wrapper, image and Compose checks, and a documented host-mount boundary for
  client state, mct-agent/Agent Manager/backend tools, and coding repositories.
- Continue with the pending host lifecycle, systemd user unit, production
  secret rotation workflow, richer poll/backend health, and WAL-safe live-state
  migration.

## 3. Close security and failure-handling gaps

- Enforce paired-sender authorization at the DearMachine Client boundary as well as
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
  `dearmachine up`, `status`, and `down` lifecycle.
- Add attachment handling with explicit limits and safe local staging.
- Implement configurable email‑response tiers (Plain, Formatted, Complete)
  that control message format, attachment policy, and artifact packaging per
  session configuration.
- Implement attachment inbox/outbox staging per session‑thread with
  timestamped directories (`.attachments-inbox/` and
  `.attachments-outbox/`). The outbox holds files that `mct-agent` intends to
  share as email attachments, since the turn conclusion is the email response.
- The repository rename is complete: maintain binary `dearmachine`,
  user-facing “DearMachine Client”, Go package `internal/client`, and “Device
  Client” only for the internal and legacy cases allow-listed in
  [`docs/naming.md`](docs/naming.md).
- Implement AskUser expiry and clear operator/user recovery messages.
- Add structured logs and minimal diagnostics without retaining email content
  unnecessarily.
- Implement and verify the OpenPGP path only after the core delivery and local
  execution lifecycle is dependable.

## 7. Agent-managed modes and personalisation

- Define mode profiles (base, Coder, Administrative) that configure the
  agent's system instructions, attachment behaviour, and output expectations.
- Support per‑user preference and values imbuing during device initialization,
  starting with Magnifica Humanitas alignment and user‑specific goals for the
  base mode.
- Wire the attachment inbox/outbox into agent sessions so `mct-agent` can
  discover inbound attachments and publish outbound artifacts.

## Alpha milestones

1. **Reliable local alpha:** CI, failure contracts, sender enforcement,
   timeouts, singleton protection, and repeatable supervision.
2. **Initial alpha deployment:** clean installation, boot persistence, live
   mail smoke test, and restart/reboot recovery in WSL.
3. **Multi-thread alpha:** durable per-thread FIFO, preemption, and bounded
   concurrency with executable scenario tests.
4. **Transport-ready alpha:** AgentMail behind a tested interface and a clear
   path to the self-hosted relay.
5. **Personalized alpha:** Configurable email tiers, attachment round‑trip, at
   least two agent‑managed modes, and values‑driven initialization seeded from
   user preferences and Magnifica Humanitas.

## Design references and documentation cleanup

- Non-versioned design inputs are kept locally under
  `.scratch/docs_staging/`. They include the target DearMachine Client specification,
  future scenario catalog, and relay capability profile. Treat them as design
  inputs rather than current operational truth.
- The local `dearmachine-config.md` staging note duplicates the DearMachine Client
  README and implies a model must be supplied even though model selection is
  optional. Consolidate any still-useful credential/setup guidance into
  `dearmachine/README.md`, then retire the staging note.
- When the DearMachine Client specification stabilizes against implemented code,
  promote it from `.scratch/docs_staging/` to a normal versioned documentation
  location. Until then, avoid presenting staged design documents as shipped
  behavior.
