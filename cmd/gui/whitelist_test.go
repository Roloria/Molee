//go:build darwin

package main

import (
	"strings"
	"testing"
)

func withTempHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	orig := osUserHomeDir
	osUserHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { osUserHomeDir = orig })
}

func TestWhitelistRoundtrip(t *testing.T) {
	withTempHome(t)

	entries, err := readWhitelist(whitelistKindClean)
	if err != nil || len(entries) != 0 {
		t.Fatalf("initial read = %v, %v; want empty, nil", entries, err)
	}

	want := []string{
		"# a comment survives",
		"/Users/*/Library/Caches/com.example.app",
		"/Users/me/Library/Caches/bigger cache name",
	}
	if err := writeWhitelist(whitelistKindClean, want); err != nil {
		t.Fatalf("writeWhitelist: %v", err)
	}

	got, err := readWhitelist(whitelistKindClean)
	if err != nil {
		t.Fatalf("readWhitelist: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("roundtrip entries = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
	if !strings.Contains(got[0], "#") {
		t.Errorf("comment line lost: %q", got[0])
	}
}

func TestWriteWhitelistDeduplicatesAndSkipsBlanks(t *testing.T) {
	withTempHome(t)

	err := writeWhitelist(whitelistKindClean, []string{"", "  ", "/a/b", "/a/b", "/a/c"})
	if err != nil {
		t.Fatalf("writeWhitelist: %v", err)
	}
	got, _ := readWhitelist(whitelistKindClean)
	if len(got) != 2 || got[0] != "/a/b" || got[1] != "/a/c" {
		t.Errorf("entries = %v, want [/a/b /a/c]", got)
	}
}

func TestWriteWhitelistRejectsBadInput(t *testing.T) {
	withTempHome(t)

	if err := writeWhitelist(whitelistKindClean, []string{"/ok", "bad\x01control"}); err == nil {
		t.Error("control character accepted")
	}
	many := make([]string, maxWhitelistEntries+1)
	for i := range many {
		many[i] = "/path/entry"
	}
	if err := writeWhitelist(whitelistKindClean, many); err == nil {
		t.Error("oversized entry list accepted")
	}
}
