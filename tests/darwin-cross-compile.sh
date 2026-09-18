#!/usr/bin/env bash
# Compile real CGO binaries and tests; this does not execute macOS tests.
set -euo pipefail
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
output=${1:?Usage: tests/darwin-cross-compile.sh /absolute/output [amd64|arm64]}
case "$output" in /*) ;; *) echo 'Output must be an absolute path.' >&2; exit 2;; esac
architectures=(amd64 arm64)
if [ "$#" -gt 1 ]; then architectures=("$2"); fi
cd "$root/dearmachine"
for architecture in "${architectures[@]}"; do
  case "$architecture" in
    amd64) compiler=${MACOS_CC_AMD64:?Set MACOS_CC_AMD64 to a macOS C compiler with an SDK.} ;;
    arm64) compiler=${MACOS_CC_ARM64:?Set MACOS_CC_ARM64 to a macOS C compiler with an SDK.} ;;
    *) echo 'Architecture must be amd64 or arm64.' >&2; exit 2 ;;
  esac
  destination="$output/$architecture"
  mkdir -p "$destination"
  export GOOS=darwin GOARCH="$architecture" CGO_ENABLED=1 CC="$compiler"
  # Disabling DWARF avoids requiring a host dsymutil when cross-linking.
  go build -mod=readonly -trimpath -ldflags=-w -o "$destination/" ./cmd/...
  go test -mod=readonly -trimpath -ldflags=-w -c -o "$destination/" ./...
  printf 'Compiled macOS %s binaries and tests in %s; no tests executed.\n' "$architecture" "$destination"
done
