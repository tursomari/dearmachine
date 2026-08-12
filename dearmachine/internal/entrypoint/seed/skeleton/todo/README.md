# todo/

This directory holds reminders, follow-ups, and flags about the machine
entry point that the User or an agent would want surfaced again in a future
session.

## What belongs here

- Incomplete work the User explicitly asked to be reminded about
- Decisions deferred during a session that need later resolution
- Documentation flagged as stale or in need of revision
- Recurring problems that warrant a permanent process or runbook

## What does not belong here

- Transient notes that will be irrelevant after the next session
- Full session summaries (those belong to the mct project store)
- Project-specific todos (those belong in their respective projects)

## Conventions

- The agent is free to choose the internal structure — a single file,
  per-topic files, dated entries, or any other format that serves clarity.
- Content is tracked in Git (unlike `state/`), so reminders survive across
  sessions and syncs.
- Prefer a small number of high-signal items over exhaustiveness.
- When an item is resolved, remove it or mark it completed rather than
  leaving it to accumulate.
