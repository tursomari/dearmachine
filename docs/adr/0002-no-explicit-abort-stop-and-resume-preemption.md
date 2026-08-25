# ADR: No Explicit Abort: Stop-and-Resume Preemption Subsumes Abort

## Status

Accepted

## Date

2026-08-25

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

On receipt of a new same-thread email, DearMachine Client stops the running
machtiani session through machtiani's graceful interrupt path (SIGINT), allowing
machtiani to save its resumable trajectory. The interrupted email is skipped
without fabricated notice text. DearMachine Client then resumes that session
with the new message as the prompt: conceptually, `run -p <message> --resume`
(the client uses the spelled-out `--prompt` argument). Child processes of the
stopped session die with it. If a process was deliberately detached and remains
running, the sender can instruct it to stop in the follow-up email.

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

**Negative**

- A sender must send a follow-up email to express an interruption; there is no
  out-of-band abort operation.
- Work deliberately detached from the stopped session may need an explicit
  stop instruction in that follow-up.

**Neutral**

- Preemption is a same-thread continuation rule; it does not change how
  independent conversations are scheduled.
- Durable message sequencing and deduplication remain client responsibilities.

## References

- `dearmachine/internal/client/agent.go` — `AgentRunner.Stop` graceful SIGINT
  seam and resumed machtiani invocation.
- `dearmachine/internal/client/app.go` — `pollAndClaim` same-thread
  preemption and `SkipPreemptedMessage` use.
- `dearmachine/internal/client/store.go` — `SkipPreemptedMessage` preserves
  the canonical session and monotonic sequence for continuation.
- `dearmachine/internal/client/preemption_test.go` — stop-and-resume behavior,
  one substantive reply, and skipped interrupted work.
- `dearmachine/README.md` — persisted machtiani session context and
  `--resume` continuation model; machtiani's SIGINT/interrupt persistence is
  the session behavior on which the graceful-stop seam relies.
- `docs/adr/` — DearMachine ADR registry; the umbrella `ADR.csv` is registered
  at landing time.
