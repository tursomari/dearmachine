# ADR: No Explicit Abort: Stop-and-Resume Preemption Subsumes Abort

## Status

Accepted

## Date

2026-08-25

Amended 2026-08-26 to define queue-aware grace preemption.

## Context

An email conversation needs a natural way for its sender to cancel or redirect
work that is already in progress. A separate abort command, flag, vocabulary,
state machine, or API would make that ordinary conversational act into a second
control surface. The simplified model has been confirmed by the user and its
implementation has begun on this branch.

The relevant boundary is already the conversation. A later same-thread email
is both the sender's next instruction and the information needed to continue
the existing session. This applies equally whether the sender's intent is to
stop, redirect, clarify, or replace the in-flight work.

## Decision

When a sender wants to cancel or redirect an in-flight email, they send another
email in the same thread. There is no special command, flag, or vocabulary.

DearMachine durably claims and dispatches every same-thread email in sequence,
including messages claimed together in one poll or accumulated while
maintenance owns the repository execution lane. Once a message's machtiani run
is launched, it receives a grace interval equal to the configured inbox poll
interval. If a newer durable same-thread message is queued when that interval
expires, DearMachine stops the active machtiani session through machtiani's
graceful interrupt path (SIGINT), allowing machtiani to save its resumable
trajectory. If the newer message arrives after the grace interval has already
elapsed, the stop is requested immediately after it is durably queued.

The graceful stop and normal command release form the completion boundary. If
the stop wins, the interrupted email is skipped without fabricated notice text.
If the run releases first, its result and reply are accepted even when a newer
message is waiting. DearMachine then dispatches the next queued email and
resumes the same session with that message as the prompt: conceptually, `run -p
<message> --resume` (the client uses the spelled-out `--prompt` argument). The
rule repeats until the same-thread queue drains, so every email is represented
as a user turn while only work that outlives its grace interval is preempted.

Child processes of the stopped session die with it. If a process was
deliberately detached and remains running, the sender can instruct it to stop
in the follow-up email.

Machtiani's session model makes this coherent: the conversation has
back-to-back user turns, and the LLM retains the full context needed to
understand the redirect naturally. Therefore DearMachine does not need an
abort command, abort state machine, or abort API.

This decision follows **subsidiarity**: decisions belong at the smallest
competent level, here the conversation itself. It also follows **resistance to
overengineering**: do not add machinery that the natural model already
provides. This ADR is the standing argument against future abort-specific
machinery.

## Consequences

**Positive**

- Senders use the existing email interaction they already understand, and the
  redirected session keeps its relevant context.
- The client has one continuation model rather than a separate cancellation
  protocol and control surface.
- The interrupted message produces no artificial reply or supersession text.
- Co-claimed mail and maintenance backlogs use the same continuation behavior
  as mail observed during an already active run.

**Negative**

- A sender must send a follow-up email to express an interruption; there is no
  out-of-band abort operation.
- Work deliberately detached from the stopped session may need an explicit
  stop instruction in that follow-up.
- A burst of long-running same-thread messages incurs up to one poll interval
  of grace for each superseded turn before the final turn continues.

**Neutral**

- Preemption is a same-thread continuation rule; it does not change how
  independent conversations are scheduled.
- Durable message sequencing and deduplication remain client responsibilities.
- The grace interval reuses the poll interval rather than adding a second
  timing option. A later need to tune these independently requires another
  explicit decision.
- DearMachine observes only its own queue and graceful process lifecycle. It
  does not inspect machtiani's private persistence or add a machtiani abort or
  prompt-acknowledgement API.

## References

- `dearmachine/internal/client/agent.go` — `AgentRunner.Stop` graceful SIGINT
  seam and resumed machtiani invocation.
- `dearmachine/internal/client/app.go` — durable claim and enqueue boundary.
- `dearmachine/internal/client/dispatch.go` — queue-aware grace deadline,
  completion race, and `SkipPreemptedMessage` use.
- `dearmachine/internal/client/store.go` — `SkipPreemptedMessage` preserves
  the canonical session and monotonic sequence for continuation.
- `dearmachine/internal/client/preemption_test.go` — stop-and-resume behavior,
  one substantive reply, and skipped interrupted work.
- `dearmachine/README.md` — persisted machtiani session context and
  `--resume` continuation model; machtiani's SIGINT/interrupt persistence is
  the session behavior on which the graceful-stop seam relies.
- `docs/adr/` — DearMachine ADR registry; the umbrella `ADR.csv` is registered
  at landing time.
