# Naming Reference

Use this matrix for source code, documentation, commands, and runtime files.

| Context | Canonical form | Usage |
| --- | --- | --- |
| Product copy | Dear Machine, | Use the space and include the comma whenever the full product name appears. |
| Client title or heading | DearMachine Client | Use for user-facing titles, headings, and the formal client name. |
| Client prose near commands | dearmachine client | Use lowercase when prose directly describes the `dearmachine` command or process. |
| Binary and command | `dearmachine` | Use in builds, shell commands, process names, help, and examples. |
| Go module | `github.com/dearmachine/dearmachine` | Keep this module path in `go.mod` and imports. |
| Repository module directory | `dearmachine/` | Run module-relative Go commands from this directory. |
| Go client package | `internal/client` | Use package name `client`; keep meaningful domain identifiers such as `DeviceConfig` when they remain accurate. |
| Go entry point | `cmd/dearmachine/` | Build the `dearmachine` binary from this directory. |
| Configuration | `~/.dearmachine/config/dearmachine.toml` | Default client configuration. |
| State database | `~/.dearmachine/state/dearmachine.db` | Default SQLite database. |
| PID file | `~/.dearmachine/run/dearmachine.pid` | Standard supervised-process PID file. |
| Log | `~/.dearmachine/log/dearmachine.log` | Standard client log. |
| Temporary results | `dearmachine-mct-results` | Temporary-directory prefix for mct-agent result files. |
| systemd unit | `dearmachine.service` | Use for every documented systemd command and unit reference. |

## Internal and legacy allow-list

“Device Client” and “device client” are not canonical user-facing names. They
are allowed only in these cases:

- a historical quotation, issue title, migration note, or compatibility note
  that explicitly identifies the former name, such as “the former Device
  Client”;
- internal technical analysis that must distinguish a legacy device client
  implementation from the current `internal/client` package; or
- meaningful source identifiers whose device-domain meaning remains accurate,
  such as `DeviceConfig` or `DefaultDeviceDatabasePath`.

Do not use the legacy forms in titles, CLI help, installation instructions,
runbooks, executable names, package paths, or runtime artifact names.

## Superseded names and paths

| Obsolete form | Replacement |
| --- | --- |
| `machinemail` | `dearmachine` |
| `~/.config/machinemail` | `~/.dearmachine/config` |

The obsolete command and path may appear only in an explicit migration or
supersession note.

## Nix package and app conventions

| Context | Canonical form |
| --- | --- |
| Default package and named package | `dearmachine` (`nix build` or `nix build .#dearmachine`) |
| User install app | `install` (`nix run .#install`) |

The `install` app installs `~/.local/bin/dearmachine` and prepares the canonical
directories under `~/.dearmachine`. The container lifecycle is implemented;
host supervision remains planned work.

## Container conventions

| Context | Canonical form |
| --- | --- |
| Container image | `localhost/dearmachine:nix` |
| Compose service | `dearmachine` |
| Stack wrapper | `dearmachine-stack` |

The image is the `dearmachine-image` flake package, and the wrapper is available
as both the `dearmachine-stack` package and app. The wrapper names follow the
xsrc pattern.

## Planned host convention

| Context | Planned form |
| --- | --- |
| Host lifecycle wrapper | `dearmachine-host-lifecycle` |

Host lifecycle and live-state migration remain planned until Stage 3 lands.
