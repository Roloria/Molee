#!/usr/bin/env bats

setup_file() {
    PROJECT_ROOT="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
    export PROJECT_ROOT

    ORIGINAL_HOME="${HOME:-}"
    export ORIGINAL_HOME

    HOME="$(mktemp -d "${BATS_TEST_DIRNAME}/tmp-json-surfaces.XXXXXX")"
    export HOME

    # NOTE: no MOLE_TEST_MODE here — bin/installer.sh skips main entirely
    # under it, and none of these flows need the sudo stubs.

    # Controlled purge scan root with one artifact-bearing project.
    PURGE_ROOT="$HOME/work"
    export PURGE_ROOT
    mkdir -p "$PURGE_ROOT/demo/node_modules" "$PURGE_ROOT/keep"
    printf 'x' > "$PURGE_ROOT/demo/node_modules/chunk.js"
    printf '%s\n' "$PURGE_ROOT" > "$HOME/.config_purge_paths_probe"

    mkdir -p "$HOME/.config/mole"
    printf '%s\n' "$PURGE_ROOT" > "$HOME/.config/mole/purge_paths"

    # Installer fixture (Library/Downloads is on the scan list and not TCC-guarded).
    mkdir -p "$HOME/Library/Downloads"
    dd if=/dev/zero of="$HOME/Library/Downloads/fake-tool.dmg" bs=1024 count=64 2> /dev/null
}

teardown_file() {
    rm -f "$HOME/Library/Downloads/fake-tool.dmg"
    if [[ "$HOME" == "${BATS_TEST_DIRNAME}/tmp-"* ]]; then
        rm -rf "$HOME"
    fi
    if [[ -n "${ORIGINAL_HOME:-}" ]]; then
        export HOME="$ORIGINAL_HOME"
    fi
}

@test "optimize --dry-run --json ends with a parseable optimize_result object" {
    run env HOME="$HOME" "$PROJECT_ROOT/mole" optimize --dry-run --json < /dev/null
    [ "$status" -eq 0 ] || return 1

    LAST_LINE="$(printf '%s\n' "$output" | tail -1)" python3 << 'PYEOF'
import json, os

payload = json.loads(os.environ["LAST_LINE"])
assert payload["mode"] == "optimize_result"
assert payload["dry_run"] is True
for key in ("applied", "unchanged", "skipped", "unavailable", "attention", "failed"):
    assert isinstance(payload[key], int)
assert isinstance(payload["actions"], list)
for entry in payload["actions"]:
    assert set(("action", "outcome")) <= set(entry)
PYEOF
}

@test "installer --dry-run --json --yes lists the scanned dmg candidate" {
    run env HOME="$HOME" "$PROJECT_ROOT/mole" installer --dry-run --json --yes < /dev/null
    [ "$status" -eq 0 ] || return 1

    LAST_LINE="$(printf '%s\n' "$output" | tail -1)" python3 << 'PYEOF'
import json, os

payload = json.loads(os.environ["LAST_LINE"])
assert payload["mode"] == "installer_preview"
paths = [c["path"] for c in payload["candidates"]]
assert any(p.endswith("fake-tool.dmg") for p in paths), paths
for candidate in payload["candidates"]:
    assert set(("path", "size_kb", "source")) <= set(candidate)
PYEOF
}

@test "installer --json --yes deletes the candidate and reports a result" {
    run env HOME="$HOME" "$PROJECT_ROOT/mole" installer --json --yes < /dev/null
    [ "$status" -eq 0 ] || return 1
    [[ ! -e "$HOME/Library/Downloads/fake-tool.dmg" ]] || return 1

    LAST_LINE="$(printf '%s\n' "$output" | tail -1)" python3 << 'PYEOF'
import json, os

payload = json.loads(os.environ["LAST_LINE"])
assert payload["mode"] == "installer_result"
assert payload["deleted"] >= 1
for key in ("freed_kb", "failed"):
    assert key in payload
PYEOF
}

@test "purge --dry-run --json lists candidates under the configured root" {
    run env HOME="$HOME" "$PROJECT_ROOT/mole" purge --dry-run --json < /dev/null
    [ "$status" -eq 0 ] || return 1

    LAST_LINE="$(printf '%s\n' "$output" | tail -1)" python3 << 'PYEOF'
import json, os

payload = json.loads(os.environ["LAST_LINE"])
assert payload["mode"] == "purge_preview"
paths = [c["path"] for c in payload["candidates"]]
assert any(p.endswith("/node_modules") for p in paths), paths
for candidate in payload["candidates"]:
    assert set(("path", "size_kb", "recent", "project")) <= set(candidate)
PYEOF
}

@test "purge --only-from --yes --json removes exactly the listed artifact" {
    local list_file="$HOME/purge_only_list.txt"
    printf '%s\n' "$PURGE_ROOT/demo/node_modules" > "$list_file"

    run env HOME="$HOME" "$PROJECT_ROOT/mole" purge --only-from "$list_file" --yes --json < /dev/null
    [ "$status" -eq 0 ] || return 1
    [[ ! -e "$PURGE_ROOT/demo/node_modules" ]] || return 1
    [[ -e "$PURGE_ROOT/keep" ]] || return 1

    LAST_LINE="$(printf '%s\n' "$output" | tail -1)" python3 << 'PYEOF'
import json, os

payload = json.loads(os.environ["LAST_LINE"])
assert payload["mode"] == "purge_result"
assert payload["outcome"] == "completed"
assert payload["items"] == 1
for key in ("freed_kb", "unknown_sizes"):
    assert key in payload
PYEOF
}

@test "purge rejects --only-from without a readable file" {
    run env HOME="$HOME" "$PROJECT_ROOT/mole" purge --only-from "$HOME/missing-list.txt" --yes < /dev/null
    [ "$status" -eq 1 ] || return 1
    [[ "$output" == *"not readable"* ]] || return 1
}
