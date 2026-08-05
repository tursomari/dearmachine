# Machine Entry Point

This repository is the durable, machine-level entry point for agents serving
the User. It preserves the context, processes, references, and assets that are
useful across projects and sessions.

It is not the documentation site for one project or application. Project-local
knowledge stays with its project. This repository records only material that
helps the User operate the machine, find important resources, or repeat a
cross-project workflow reliably.

## Directories

- `process/` contains self-contained runbooks for recurring machine-level work.
- `documentation/` contains descriptive reference material and pointers.
- `assets/` contains non-plaintext material linked by documentation or runbooks.
- `state/` contains generated, machine-local state that is normally not synced.

Each directory has a README that defines its scope.

## Operating Model

Agents begin with a generated internal README derived from this repository.
During later sessions, durable machine-level discoveries may be incorporated
into `process/` or `documentation/`. Updates should remain concise, useful in
future sessions, and centered on the User rather than the implementation of any
single tool.

## Placement Rules

- Put repeatable instructions in `process/`.
- Put explanations, conventions, caveats, and resource pointers in
  `documentation/`.
- Keep project-specific implementation detail in the relevant project.
- Link required binary material from `assets/`; Git LFS tracks common binary
  formats.
- Do not commit credentials, transient session data, or generated local state.
