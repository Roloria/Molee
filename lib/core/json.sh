#!/bin/bash
# Shared JSON string encoding for the machine-readable `--json` summaries.

set -euo pipefail

if [[ -n "${MOLE_JSON_LOADED:-}" ]]; then
    return 0
fi
readonly MOLE_JSON_LOADED=1

# Print value encoded as a JSON string literal (with surrounding quotes).
# Control characters are dropped: filesystem paths never legitimately need
# them, and raw control bytes are forbidden inside JSON strings.
mole_json_string() {
    local s="${1:-}"
    s=${s//\\/\\\\}
    s=${s//\"/\\\"}
    printf '"'
    printf '%s' "$s" | LC_ALL=C tr -d '\000-\010\012\013\014\015\016-\037'
    printf '"'
}
