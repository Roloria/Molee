#!/usr/bin/env bats

setup_file() {
    PROJECT_ROOT="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
    export PROJECT_ROOT

    ORIGINAL_HOME="${HOME:-}"
    export ORIGINAL_HOME

    HOME="$(mktemp -d "${BATS_TEST_DIRNAME}/tmp-clean-only.XXXXXX")"
    export HOME

    # Prevent AppleScript permission dialogs during tests
    MOLE_TEST_MODE=1
    export MOLE_TEST_MODE

    mkdir -p "$HOME"
}

teardown_file() {
    if [[ "$HOME" == "${BATS_TEST_DIRNAME}/tmp-"* ]]; then
        rm -rf "$HOME"
    fi
    if [[ -n "${ORIGINAL_HOME:-}" ]]; then
        export HOME="$ORIGINAL_HOME"
    fi
}

@test "--only-from deletes only the listed paths and keeps the rest" {
    local base="$HOME/only_filter"
    mkdir -p "$base"
    printf 'xxxx' > "$base/a"
    printf 'xxxx' > "$base/b"

    local list_file="$HOME/only_list.txt"
    printf '%s\n' "$base/a" > "$list_file"

    run env HOME="$HOME" PROJECT_ROOT="$PROJECT_ROOT" MOLE_TEST_MODE=1 /bin/bash --noprofile --norc << EOF
set -euo pipefail
source "\$PROJECT_ROOT/lib/core/common.sh"
source "\$PROJECT_ROOT/bin/clean.sh"
CLEAN_ONLY_FROM_FILE="$list_file"
load_clean_only_paths "\$CLEAN_ONLY_FROM_FILE"
CLEAN_ONLY_MODE=true
DRY_RUN=false
files_cleaned=0
total_size_cleaned=0
total_items=0
start_section_spinner() { :; }
stop_section_spinner() { :; }
start_inline_spinner() { :; }
stop_inline_spinner() { :; }
note_activity() { :; }
safe_remove() { /bin/rm -rf "\$1"; return 0; }
safe_clean "$base/a" "$base/b" "Selected cache"
EOF

    [ "$status" -eq 0 ] || return 1
    [[ ! -e "$base/a" ]] || return 1
    [[ -e "$base/b" ]] || return 1
}

@test "--only-from still honors the whitelist for listed paths" {
    local base="$HOME/only_whitelist"
    mkdir -p "$base"
    printf 'xxxx' > "$base/protected"

    local list_file="$HOME/only_list_protected.txt"
    printf '%s\n' "$base/protected" > "$list_file"

    run env HOME="$HOME" PROJECT_ROOT="$PROJECT_ROOT" MOLE_TEST_MODE=1 /bin/bash --noprofile --norc << EOF
set -euo pipefail
source "\$PROJECT_ROOT/lib/core/common.sh"
source "\$PROJECT_ROOT/bin/clean.sh"
CLEAN_ONLY_FROM_FILE="$list_file"
load_clean_only_paths "\$CLEAN_ONLY_FROM_FILE"
CLEAN_ONLY_MODE=true
DRY_RUN=false
files_cleaned=0
total_size_cleaned=0
total_items=0
start_section_spinner() { :; }
stop_section_spinner() { :; }
start_inline_spinner() { :; }
stop_inline_spinner() { :; }
note_activity() { :; }
safe_remove() { /bin/rm -rf "\$1"; return 0; }
is_path_whitelisted() { [[ "\$1" == "$base/protected" ]]; }
safe_clean "$base/protected" "Selected cache"
EOF

    [ "$status" -eq 0 ] || return 1
    [[ -e "$base/protected" ]] || return 1
}

@test "load_clean_only_paths rejects empty and unreadable files" {
    local empty_file="$HOME/empty_list.txt"
    : > "$empty_file"

    run env HOME="$HOME" PROJECT_ROOT="$PROJECT_ROOT" /bin/bash --noprofile --norc << EOF
set -euo pipefail
source "\$PROJECT_ROOT/lib/core/common.sh"
source "\$PROJECT_ROOT/bin/clean.sh"
load_clean_only_paths "$empty_file"
EOF
    [ "$status" -eq 1 ]

    run env HOME="$HOME" PROJECT_ROOT="$PROJECT_ROOT" /bin/bash --noprofile --norc << EOF
set -euo pipefail
source "\$PROJECT_ROOT/lib/core/common.sh"
source "\$PROJECT_ROOT/bin/clean.sh"
load_clean_only_paths "$HOME/does-not-exist.txt"
EOF
    [ "$status" -eq 1 ]
}

@test "load_clean_only_paths skips blanks and comments" {
    local list_file="$HOME/commented_list.txt"
    {
        printf '# a comment\n'
        printf '\n'
        printf '   %s/b  \n' "$HOME/only_filter"
    } > "$list_file"

    run env HOME="$HOME" PROJECT_ROOT="$PROJECT_ROOT" /bin/bash --noprofile --norc << EOF
set -euo pipefail
source "\$PROJECT_ROOT/lib/core/common.sh"
source "\$PROJECT_ROOT/bin/clean.sh"
load_clean_only_paths "$list_file" || exit 1
printf 'count:%s\\n' "\${#CLEAN_ONLY_PATHS[@]}"
printf 'entry:%s\\n' "\${CLEAN_ONLY_PATHS[0]}"
EOF
    [ "$status" -eq 0 ] || return 1
    [[ "$output" == *"count:1"* ]] || return 1
    [[ "$output" == *"$HOME/only_filter/b"* ]] || return 1
}

@test "clean --dry-run --json ends with a single-line preview JSON object" {
    run env HOME="$HOME" MOLE_TEST_NO_AUTH=1 MOLE_TEST_MODE=1 "$PROJECT_ROOT/mole" clean --dry-run --json
    [ "$status" -eq 0 ] || return 1

    local last_line
    last_line="$(printf '%s\n' "$output" | tail -1)"

    # Keep literal braces out of the test body: bats' scanner counts them.
    LAST_LINE="$last_line" python3 << 'PYEOF'
import json, os

payload = json.loads(os.environ["LAST_LINE"])
assert payload["mode"] == "clean_preview"
assert isinstance(payload["sections"], list)
for section in payload["sections"]:
    assert set(("name", "items")) <= set(section)
    for item in section["items"]:
        assert set(("path", "size_kb", "size_known", "items")) <= set(item)
for key in ("total_kb", "total_known", "partial", "rows", "items", "categories"):
    assert key in payload
PYEOF
}

@test "clean --json real run ends with a result JSON object" {
    run env HOME="$HOME" MOLE_TEST_NO_AUTH=1 MOLE_TEST_MODE=1 "$PROJECT_ROOT/mole" clean --json
    [ "$status" -eq 0 ] || return 1

    local last_line
    last_line="$(printf '%s\n' "$output" | tail -1)"

    LAST_LINE="$last_line" python3 << 'PYEOF'
import json, os

payload = json.loads(os.environ["LAST_LINE"])
assert payload["mode"] == "clean_result"
for key in ("cancelled", "freed_kb", "items", "categories", "partial"):
    assert key in payload
PYEOF
}

@test "safe_remove refuses non-selected paths even when called directly" {
    local base="$HOME/sink_leak"
    mkdir -p "$base/app/GPUCache" "$base/pip"
    printf 'xxxx' > "$base/app/GPUCache/data_0"
    printf 'xxxx' > "$base/pip/cache"

    local list_file="$HOME/only_list_sink.txt"
    printf '%s\n' "$base/pip" > "$list_file"

    run env HOME="$HOME" PROJECT_ROOT="$PROJECT_ROOT" MOLE_TEST_MODE=1 /bin/bash --noprofile --norc << EOF
set -euo pipefail
source "\$PROJECT_ROOT/lib/core/common.sh"
source "\$PROJECT_ROOT/bin/clean.sh"
load_clean_only_paths "$list_file"
CLEAN_ONLY_MODE=true
DRY_RUN=false
files_cleaned=0
total_size_cleaned=0
total_items=0
start_section_spinner() { :; }
stop_section_spinner() { :; }
start_inline_spinner() { :; }
stop_inline_spinner() { :; }
note_activity() { :; }
safe_remove "$base/app/GPUCache/data_0" true || true
safe_clean "$base/pip" "Selected cache"
EOF

    [ "$status" -eq 0 ] || return 1
    [[ -e "$base/app/GPUCache/data_0" ]] || return 1
    [[ ! -e "$base/pip/cache" ]] || return 1
}

@test "clean rejects --only-from with missing file argument" {
    run env HOME="$HOME" MOLE_TEST_NO_AUTH=1 MOLE_TEST_MODE=1 "$PROJECT_ROOT/mole" clean --only-from
    [ "$status" -eq 1 ] || return 1
    [[ "$output" == *"Missing file for --only-from"* ]] || return 1
}
