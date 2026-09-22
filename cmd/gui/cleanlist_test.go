//go:build darwin

package main

import "testing"

const sampleCleanList = `# Mole Cleanup Preview - 2026-09-23 08:30:00
#
# How to protect files:
# 1. Copy any path below to ~/.config/mole/whitelist
#

=== User essentials ===
/Users/me/Library/Caches  # 2.40GB, 18 items
/Users/me/Library/Logs  # 12.8MB, 7 items
/Users/me/.Trash  # 0B, 9 items

=== App caches ===
/Users/me/Library/Caches/com.example.app  # 248.5MB
/Users/me/Library/Caches/partial.app  # size unknown, 2 items

=== Developer tools ===
/Users/me/.npm/_cacache  # 512.3MB, counted under /Users/me/Library/Caches
/Users/me/Library/pnpm  # size unknown
`

func TestParseCleanList(t *testing.T) {
	p := parseCleanList([]byte(sampleCleanList))

	if p.GeneratedAt != "2026-09-23 08:30:00" {
		t.Errorf("GeneratedAt = %q, want %q", p.GeneratedAt, "2026-09-23 08:30:00")
	}
	if len(p.Sections) != 3 {
		t.Fatalf("got %d sections, want 3", len(p.Sections))
	}

	user := p.Sections[0]
	if user.Name != "User essentials" {
		t.Errorf("section 0 name = %q", user.Name)
	}
	if user.Rows != 3 {
		t.Errorf("section 0 rows = %d, want 3", user.Rows)
	}
	// 2.40GB + 12.8MB + 0B, decimal units.
	wantUser := int64(2400000000 + 12800000)
	if user.TotalBytes != wantUser {
		t.Errorf("section 0 total = %d, want %d", user.TotalBytes, wantUser)
	}
	if got := user.Items[0].Items; got != 18 {
		t.Errorf("item 0 count = %d, want 18", got)
	}
	if got := user.Items[0].SizeDisplay; got != "2.40GB" {
		t.Errorf("item 0 display = %q", got)
	}

	// Unknown sizes flag partial and are not counted in totals.
	if !p.Partial {
		t.Error("Partial = false, want true (unknown-size rows present)")
	}

	// Covered rows count toward rows but not totals.
	dev := p.Sections[2]
	if dev.Rows != 2 {
		t.Errorf("developer rows = %d, want 2", dev.Rows)
	}
	if dev.TotalBytes != 0 {
		t.Errorf("developer total = %d, want 0 (single row is covered)", dev.TotalBytes)
	}
	if dev.Items[0].CoveredBy != "/Users/me/Library/Caches" {
		t.Errorf("covered_by = %q", dev.Items[0].CoveredBy)
	}
	if dev.Items[1].SizeKnown {
		t.Error("pnpm row should have unknown size")
	}
}

func TestParseCleanListEmpty(t *testing.T) {
	p := parseCleanList(nil)
	if p.Sections == nil || len(p.Sections) != 0 {
		t.Errorf("sections = %#v, want empty non-nil", p.Sections)
	}
	if p.GeneratedAt != "" || p.Rows != 0 || p.TotalBytes != 0 {
		t.Errorf("unexpected fields: %#v", p)
	}
}

func TestParseHumanBytes(t *testing.T) {
	cases := map[string]int64{
		"0B":     0,
		"999B":   999,
		"1KB":    1000,
		"12.8MB": 12800000,
		"2.40GB": 2400000000,
		"1.5TB":  1500000000000,
	}
	for in, want := range cases {
		got, ok := parseHumanBytes(in)
		if !ok || got != want {
			t.Errorf("parseHumanBytes(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "12 XB", "abc", "-5MB", "1.2.3GB"} {
		if _, ok := parseHumanBytes(bad); ok {
			t.Errorf("parseHumanBytes(%q) should fail", bad)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:          "0B",
		999:        "999B",
		1000:       "1KB",
		12800000:   "12.8MB",
		2400000000: "2.40GB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestCoveredWithCommaPath(t *testing.T) {
	line := "/some/dir  # 1.5MB, 2 items, counted under /weird, path with comma"
	item, ok := parseCleanItem(line)
	if !ok {
		t.Fatal("parse failed")
	}
	if item.CoveredBy != "/weird, path with comma" {
		t.Errorf("CoveredBy = %q", item.CoveredBy)
	}
	if item.Items != 2 || item.SizeBytes != 1500000 {
		t.Errorf("item = %#v", item)
	}
}
