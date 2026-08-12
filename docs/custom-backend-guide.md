# Register Your Own Backend Agent in DearMachine

DearMachine now supports custom backend agents — you can bring any program that speaks stdin/stdout and have it handle work tickets just like the built-in `codex` and `forge` backends.  You don't need to edit Go source or recompile anything; you just write a short TOML snippet and approve the backend's ID.  Here's how, step by step.

## 1. Create the custom-backends configuration file

Create or edit `~/.dearmachine/config/custom-backends.toml`.  
Each backend gets its own `[id]` section.  Here's the minimal entry for a backend called `"deepcode"`:

```toml
[deepcode]
executable = "/home/david/bin/deepcode-backend"
output_format = "plain"
```

**What the fields mean:**

| Field | Required? | Description |
|-------|-----------|-------------|
| `executable` | yes | Absolute path (or a name on `$PATH`) to your backend program. |
| `output_format` | yes | `"json-stream"` (for tools that emit JSON Lines, like Codex) or `"plain"` (for tools that write plain text). |
| `name` | no | A human-friendly display name; defaults to the section ID. |
| `arguments` | no | Extra command-line arguments. The special token `$WRITABLE_DIR` is replaced with the actual sandbox directory at runtime. |
| `environment` | no | A table of extra environment variables, e.g., `{ API_KEY = "sk-..." }`. |
| `install_help` | no | Shown when the executable is missing; a short note about how to install it. |

For deepcode, the full entry might look like:

```toml
[deepcode]
name = "Deepcode (Deepseek v4 Flash)"
executable = "/home/david/bin/deepcode-backend"
output_format = "plain"
environment = { DEEPSEEK_API_KEY = "sk-..." }
install_help = "See the custom backend runbook for details."
```

## 2. What your backend must do (the contract)

Your executable receives the work request on **stdin**.  The very last line of stdin is always a close‑path in the form:

```
CLOSE <absolute-path>
```

Everything before that line is the work request (ticket description).  
Your program **must** write its reply to that exact file.  DearMachine then picks it up automatically.

That's the whole interface — if your program can read a request, decide what to do, and write a response to a file whose path it learns from stdin, it can be a DearMachine backend.

## 3. Approve the backend

DearMachine only dispatches work to backends you explicitly list.  
Edit `~/.dearmachine/config/device-client.toml` and add `"deepcode"` to the `backends` line:

```
backends = ["codex", "forge", "deepcode"]
```

(If the file doesn't exist, create it with that single line.)

Or, for a quick test without touching the file, use an environment variable:

```bash
export DEARMACHINE_BACKENDS='["codex", "forge", "deepcode"]'
```

The order sets the **priority** — when no specific backend is requested, DearMachine tries the first available one.

## 4. Verify everything is healthy

From the `device-client` directory, build the `agent-manager` and run a health probe:

```bash
cd ~/projects/DearMachine/device-client
go build -o agent-manager ./cmd/agent-manager

DEARMACHINE_BACKENDS='["deepcode"]' ./agent-manager backend health deepcode
```

If the executable is found and responds correctly to the probe, you'll see `result=ok`.  
If the binary is missing or the probe fails, you'll get `result=fail` with details — check the `install_help` hint if you defined one.

## 5. Dispatch a test ticket

Create a tiny work‑request file and send it as a ticket:

```bash
mkdir -p /tmp/test-project
echo "Say hello in French, just one short phrase." > /tmp/test-request.md

DEARMACHINE_BACKENDS='["deepcode"]' \
  ./agent-manager ticket send \
    --backend deepcode \
    --file /tmp/test-request.md \
    --cwd /tmp/test-project
```

The command prints a ticket ID (e.g., `20260811T201221-ca06bb82`).  
Once the ticket status moves to `closed`, view the result:

```bash
./agent-manager ticket view <ticket-id>
```

The reply (e.g., `"Bonjour !"`) appears in the ticket output.  Your custom backend is live.

## A quick note on output format

Picking the right `output_format` is usually the only guesswork:

- **`"plain"`** — Use if your backend prints the reply as normal text (or writes the reply file and prints very little to stdout).  DearMachine captures up to the last 64 KiB of stdout as the conversation reply.
- **`"json-stream"`** — Use if your backend prints one JSON object per line and embeds the reply text in an `agent_message` field (the Codex CLI convention).

**Tip:** If you're unsure, start with `"plain"` and send a test ticket.  If the captured reply is garbled or empty, switch to `"json-stream"`.

## Is My Executable Compatible? (TUI vs Headless Backends)

DearMachine backends must be **headless, non-interactive** programs that read from stdin, do their work, write a reply file, and exit. Terminal TUI (Text User Interface) applications — like many modern AI coding tools in their default mode — are **not compatible** as backends because they expect a pseudo-TTY for interactive input and produce ANSI-escape-heavy output.

### Quick Pre‑Flight Test

Before registering a backend, run this one‑liner to check compatibility:

```bash
echo "Say hello in French, just one short phrase." | timeout 10 /path/to/your-executable --headless-flag 2>&1
```

**Compatible if:**
- The command completes within seconds (not minutes).
- Produces clean, readable text output (plain text or JSON).
- Does **not** print ANSI escape codes, spinner animations, or interactive prompts.

**Incompatible if you see:**
- The command hangs until timeout (waiting for TTY input).
- Escape sequences like `\x1b[`, `ESC[`, or terminal control codes.
- Interactive prompts like `? Select an option`.

### Finding Headless/Automation Flags

Many TUI tools offer a headless or non‑interactive mode. Check the tool's help:

```bash
/path/to/your-executable --help | grep -i -E 'auto|headless|non.interactive|batch|run|--model'
```

Examples:
- **OpenCode**: requires `run --auto` (headless mode). Without `--auto`, opencode hangs waiting for TTY input.
- **Codex CLI**: headless by default; accepts stdin and produces JSON Lines output.
- **DeepCode CLI** (`@vegamo/deepcode-cli`): terminal TUI only; requires a wrapper script to work as a backend.

### Writing a Wrapper Script

If your tool is TUI‑only but can be scripted via an API, write a wrapper that:
1. Reads the work request from stdin.
2. Extracts the `Close-Path` line (format: `# Close-Path: /absolute/path`).
3. Calls the tool's underlying API (or SDK) programmatically.
4. Writes the reply to the close‑path file.

See the deepcode‑backend example in the walkthrough above for a working pattern.

## Beware of MACHTIANI_SESSION_ID when testing

If you are testing a temporary device-client from within an existing
mct-agent session, the inherited `MACHTIANI_SESSION_ID` environment
variable will cause the temporary client to collide with the parent
session lock. The client fails with an error matching
`session already active for .../session.lock`. Either unset the variable
(`unset MACHTIANI_SESSION_ID`) or launch with a clean environment
(`env -i PATH="$PATH" HOME="$HOME" device-client ...`).

## Where to find more help

- **Design document** (architecture details): `~/projects/pm/docs/custom-backend-design.md`
- **PM ticket** (feature track & feedback): `7fe89894` (in `~/projects/pm/.issues`)
- **Runbook** with deepcode example and troubleshooting: `~/projects/mct/docs/registering-a-custom-backend.md`

## DeepCode CLI example (using the `deepcode` CLI directly)

DeepCode is a terminal TUI tool by default, but supports headless operation
via `-x`/`--exec`. To use it as a DearMachine backend, a thin wrapper script
handles the agent-manager's stdin protocol.

### 1. Install deepcode

```bash
cd ~/projects/deepcode-cli
npm install && npm run build
# Ensure Node 23+ is on PATH; deepcode uses regex features from Node 23+
```

### 2. Create the wrapper

Save the wrapper script (see `scripts/deepcode-wrapper.sh` in the DearMachine
repository) to a location on your machine, e.g.
`~/projects/DearMachine/scripts/deepcode-wrapper.sh`. The wrapper:

- Recognises health-check probes and writes the probe file
- Extracts the `Close-Path` from ticket-open.md headers
- Invokes `deepcode -x -p "<work_request>"` in headless mode
- Writes the reply to the Close-Path file and prints it to stdout

### 3. Register

```toml
[deepcode]
name = "DeepCode (Deepseek v4 Flash)"
executable = "/home/david/projects/DearMachine/scripts/deepcode-wrapper.sh"
output_format = "plain"
install_help = "cd ~/projects/deepcode-cli && npm install && npm run build"
```

### 4. How the Close-Path protocol works

The agent-manager feeds the full `ticket-open.md` to stdin. The header
contains metadata lines including:

```
# Close-Path: /absolute/path/to/ticket-close.md
```

Your executable must **write its reply to that file**. The agent-manager
reads `ticket-close.md` to determine the ticket result. If your tool does not
natively understand this protocol, you need a wrapper like the one above.
