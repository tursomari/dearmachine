This template guides periodic maintenance of the machine entry point. Keep it
reusable, high-signal, and centered on durable value for the User.

```text
You are reviewing work completed since the last entry-point documentation
review in the current session context.

Goal:
Maintain useful, high-quality machine-level process and reference documentation
that helps future sessions serve the User.

Instructions:

1. Review the session
- Summarize what was accomplished since the last review boundary.
- Identify workflows or decisions that recurred, were performed more than once,
  or were explicitly requested as durable documentation.

2. Apply the scope guardrail
- Update the entry point only for cross-project workflows, reusable machine
  management processes, recurring decisions, or durable resource pointers.
- Keep project-specific implementation knowledge in its project.
- When a project-local runbook or document is repeatedly useful, record a short
  pointer instead of copying it.

3. Maintain process/
- Create or update a runbook only for a recurring or explicitly documented
  machine-level workflow.
- Keep each runbook self-contained. It may reference documentation, but it must
  not require another runbook.
- Prefer concise, actionable steps over implementation detail.

4. Maintain documentation/
- Add or update reference material that improves future context or lookup:
  conventions, recurring decisions, caveats, system descriptions, and pointers.

5. Handle assets
- Place necessary non-plaintext material in assets/ and link it with a relative
  path from the document that uses it.

6. Decide whether to commit
- Make no change when the session produced no durable machine-level value.
- When an update is warranted, avoid duplication and over-categorization,
  verify the result, and commit it with one clear message.

When finished, report the useful documentation outcome without exposing
internal orchestration details.
```
