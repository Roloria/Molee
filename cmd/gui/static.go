//go:build darwin

// Package main provides the Molee GUI: a local, read-only web dashboard that
// wraps the existing CLI's JSON surfaces (status --watch, analyze --json,
// clean --dry-run, uninstall --list, history --json) over loopback HTTP.
package main

import "embed"

// embeddedStatic carries the SPA bundle; bin/gui.sh sets MOLEE_SCRIPT_DIR so
// the server can find the sibling CLI wrappers at runtime.
//
//go:embed static
var embeddedStatic embed.FS
