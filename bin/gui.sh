#!/bin/bash
# Molee - GUI command.
# Runs the local web dashboard server (read-only).
# Uses bundled gui-go binary.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_BIN="$SCRIPT_DIR/gui-go"
if [[ -x "$GO_BIN" ]]; then
    export MOLEE_SCRIPT_DIR="$SCRIPT_DIR"
    exec "$GO_BIN" "$@"
fi

echo "Bundled GUI binary not found. Build it with: make build" >&2
exit 1
