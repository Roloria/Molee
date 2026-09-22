#!/usr/bin/env bats

setup_file() {
	PROJECT_ROOT="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
	export PROJECT_ROOT
}

@test "mole --help documents gui command" {
	run env HOME="${HOME}" "$PROJECT_ROOT/mole" --help
	[ "$status" -eq 0 ]
	[[ "$output" == *"mo gui"* ]] || return 1
	[[ "$output" == *"web dashboard"* ]] || return 1
}

@test "mole gui without bundled binary exits with guidance" {
	if [[ -x "$PROJECT_ROOT/bin/gui-go" ]]; then
		skip "gui-go is built; routing test only applies to clean checkouts"
	fi
	run env HOME="${HOME}" "$PROJECT_ROOT/mole" gui
	[ "$status" -eq 1 ]
	[[ "$output" == *"Bundled GUI binary not found"* ]] || return 1
	[[ "$output" == *"make build"* ]] || return 1
}
