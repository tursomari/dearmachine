#!/usr/bin/env bash
set -euo pipefail
# deepcode-wrapper: adapts deepcode CLI for DearMachine agent-manager backend.
# Handles health-check probes and ticket dispatch with Close-Path protocol.
#
# The agent-manager feeds the full ticket-open.md to stdin.  The backend must:
# 1. For health probes: write the probe file and confirm
# 2. For tickets: extract the Close-Path header, invoke deepcode headless,
#    and write the reply to the Close-Path.

if ! command -v deepcode >/dev/null 2>&1; then
    echo "Error: deepcode is not on PATH; install DeepCode CLI before using this wrapper" >&2
    exit 1
fi

stdin=$(cat)

# ---- Health-check probe ----
# Format: "Write a file at exactly <path> containing exactly this line: <content>. Do not modify ..."
if echo "$stdin" | grep -q 'Write a file at exactly '; then
    probe_path=$(echo "$stdin" | sed -n 's/.*Write a file at exactly \([^ ]*\).*/\1/p')
    probe_content=$(echo "$stdin" | sed -n 's/.*containing exactly this line: \([^.]*\)\. Do.*/\1/p')
    if [ -n "$probe_path" ] && [ -n "$probe_content" ]; then
        echo "$probe_content" > "$probe_path"
        echo "Probe file written successfully. The probe is complete."
        exit 0
    fi
    echo "Probe failed: could not parse probe instruction" >&2
    exit 1
fi

# ---- Ticket dispatch ----
close_path=$(echo "$stdin" | grep '^# Close-Path:' | head -1 | sed 's/^# Close-Path: *//')
if [ -z "$close_path" ]; then
    echo "Error: no Close-Path found in work request" >&2
    exit 1
fi

# Extract the work request: skip header lines (# ...) and the blank separator,
# then capture everything else.
work_request=$(echo "$stdin" | awk 'BEGIN { skip=1 } /^$/ && skip { skip=0; next } !skip' ORS='' | head -c 65536)
if [ -z "$work_request" ]; then
    work_request=$(echo "$stdin" | sed '/^# /d' | sed '/^$/N; /^\n$/d')
fi

# Invoke deepcode in headless mode
reply=$(deepcode -x -p "$work_request" 2>/dev/null) || {
    echo "Error: deepcode execution failed" >&2
    exit 1
}

echo "$reply" | tee "$close_path"
