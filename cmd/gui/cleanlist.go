//go:build darwin

package main

import (
	"encoding/json"
	"fmt"

	"strconv"
	"strings"
)

// CleanPreview is the structured view of ~/.config/mole/clean-list.txt, the
// ledger `mole clean --dry-run` renders. Rows flagged CoveredBy duplicate an
// ancestor candidate; their bytes are already counted in that ancestor, so
// subtotals exclude them (mirroring the CLI's own accounting).
type CleanPreview struct {
	GeneratedAt string         `json:"generated_at"`
	Sections    []CleanSection `json:"sections"`
	TotalBytes  int64          `json:"total_bytes"`
	TotalKnown  bool           `json:"total_known"`
	Partial     bool           `json:"partial"`
	Rows        int            `json:"rows"`
	Items       int            `json:"items"`
}

type CleanSection struct {
	Name       string      `json:"name"`
	Items      []CleanItem `json:"items"`
	TotalBytes int64       `json:"total_bytes"`
	Rows       int         `json:"rows"`
}

type CleanItem struct {
	Path        string `json:"path"`
	SizeBytes   int64  `json:"size_bytes"` // 0 when unknown
	SizeDisplay string `json:"size_display"`
	Items       int    `json:"items"`
	CoveredBy   string `json:"covered_by,omitempty"`
	SizeKnown   bool   `json:"size_known"`
}

const coveredByPrefix = "counted under "

// parseCleanList understands the exact render format of
// render_clean_preview_from_ledger:
//
//	# Mole Cleanup Preview - 2026-09-23 12:00:00
//	=== Section ===
//	/path  # 1.20GB, 18 items
//	/path  # size unknown, 3 items
//	/path  # 12.8MB, 2 items, counted under /ancestor
func parseCleanList(data []byte) CleanPreview {
	preview := CleanPreview{TotalKnown: true, Sections: []CleanSection{}}
	var current *CleanSection

	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(raw, "\r")
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			if header, ok := strings.CutPrefix(line, "# Mole Cleanup Preview - "); ok {
				preview.GeneratedAt = strings.TrimSpace(header)
			}
		case strings.HasPrefix(line, "=== ") && strings.HasSuffix(line, " ==="):
			name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "=== "), " ==="))
			preview.Sections = append(preview.Sections, CleanSection{Name: name})
			current = &preview.Sections[len(preview.Sections)-1]
		default:
			item, ok := parseCleanItem(line)
			if !ok {
				continue
			}
			if current == nil {
				preview.Sections = append(preview.Sections, CleanSection{Name: "Other"})
				current = &preview.Sections[len(preview.Sections)-1]
			}
			current.Items = append(current.Items, item)
			current.Rows++
			preview.Rows++
			if item.CoveredBy == "" {
				current.TotalBytes += item.SizeBytes
				preview.TotalBytes += item.SizeBytes
				preview.Items += item.Items
				if !item.SizeKnown {
					preview.Partial = true
				}
			}
		}
	}

	if preview.TotalBytes < 0 {
		preview.TotalBytes = 0
	}
	return preview
}

func parseCleanItem(line string) (CleanItem, bool) {
	sep := strings.LastIndex(line, "  #")
	if sep < 0 {
		return CleanItem{}, false
	}
	path := strings.TrimSpace(line[:sep])
	rest := strings.TrimSpace(line[sep+len("  #"):])
	if path == "" || rest == "" {
		return CleanItem{}, false
	}

	item := CleanItem{Path: path, Items: 1, SizeKnown: true}

	parts := strings.Split(rest, ", ")
	// A covered-by path may itself contain ", ", so rejoin everything after
	// the marker instead of treating it as separate fields.
	for i, part := range parts {
		if after, ok := strings.CutPrefix(part, coveredByPrefix); ok {
			tail := strings.TrimSpace(strings.Join(parts[i+1:], ", "))
			if tail == "" {
				item.CoveredBy = strings.TrimSpace(after)
			} else {
				item.CoveredBy = strings.TrimSpace(after + ", " + tail)
			}
			parts = parts[:i]
			break
		}
	}

	sizeStr := strings.TrimSpace(parts[0])
	if sizeStr == "size unknown" {
		item.SizeKnown = false
		item.SizeDisplay = "size unknown"
	} else if bytes, ok := parseHumanBytes(sizeStr); ok {
		item.SizeBytes = bytes
		item.SizeDisplay = sizeStr
	} else {
		item.SizeKnown = false
		item.SizeDisplay = "size unknown"
	}

	for _, part := range parts[1:] {
		part = strings.TrimSpace(part)
		if n, ok := parseTrailingInt(part, " items"); ok {
			item.Items = n
		}
	}
	return item, true
}

// parseHumanBytes reads the decimal units bytes_to_human emits: B, KB, MB, GB.
func parseHumanBytes(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	unit := s
	for _, suffix := range []string{"TB", "GB", "MB", "KB", "B"} {
		if strings.HasSuffix(s, suffix) {
			unit = suffix
			s = strings.TrimSuffix(s, suffix)
			break
		}
	}
	value, err := strconv.ParseFloat(s, 64)
	if err != nil || value < 0 {
		return 0, false
	}
	var multiplier float64 = 1
	switch unit {
	case "KB":
		multiplier = 1e3
	case "MB":
		multiplier = 1e6
	case "GB":
		multiplier = 1e9
	case "TB":
		multiplier = 1e12
	}
	return int64(value * multiplier), true
}

func parseTrailingInt(s, suffix string) (int, bool) {
	if !strings.HasSuffix(s, suffix) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(s, suffix)))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// humanBytes mirrors bytes_to_human: decimal units, GB with two decimals, MB
// with one, KB rounded.
func humanBytes(b int64) string {
	switch {
	case b >= 1e9:
		return fmt.Sprintf("%.2fGB", float64(b)/1e9)
	case b >= 1e6:
		return fmt.Sprintf("%.1fMB", float64(b)/1e6)
	case b >= 1e3:
		return fmt.Sprintf("%dKB", (b+500)/1000)
	default:
		return fmt.Sprintf("%dB", b)
	}
}

// cliCleanPreview mirrors `mole clean --dry-run --json` (single-line JSON on
// the last stdout line).
type cliCleanPreview struct {
	Mode        string `json:"mode"`
	GeneratedAt string `json:"generated_at"`
	Sections    []struct {
		Name  string `json:"name"`
		Items []struct {
			Path      string `json:"path"`
			SizeKB    int64  `json:"size_kb"`
			SizeKnown bool   `json:"size_known"`
			Items     int    `json:"items"`
			CoveredBy string `json:"covered_by"`
		} `json:"items"`
	} `json:"sections"`
	TotalKB    int64 `json:"total_kb"`
	TotalKnown bool  `json:"total_known"`
	Partial    bool  `json:"partial"`
	Rows       int   `json:"rows"`
	Items      int   `json:"items"`
	Categories int   `json:"categories"`
}

// cleanPreviewFromCLIJSON converts the CLI summary into the dashboard schema.
func cleanPreviewFromCLIJSON(data []byte) (CleanPreview, error) {
	var cli cliCleanPreview
	if err := json.Unmarshal(data, &cli); err != nil {
		return CleanPreview{}, err
	}
	if cli.Mode != "clean_preview" {
		return CleanPreview{}, fmt.Errorf("unexpected clean report mode %q", cli.Mode)
	}

	preview := CleanPreview{
		GeneratedAt: cli.GeneratedAt,
		TotalBytes:  cli.TotalKB * 1024,
		TotalKnown:  cli.TotalKnown,
		Partial:     cli.Partial,
		Rows:        cli.Rows,
		Items:       cli.Items,
		Sections:    make([]CleanSection, 0, len(cli.Sections)),
	}
	for _, section := range cli.Sections {
		out := CleanSection{Name: section.Name}
		for _, item := range section.Items {
			out.Items = append(out.Items, CleanItem{
				Path:        item.Path,
				SizeBytes:   item.SizeKB * 1024,
				SizeDisplay: humanBytes(item.SizeKB * 1024),
				Items:       item.Items,
				CoveredBy:   item.CoveredBy,
				SizeKnown:   item.SizeKnown,
			})
			if item.CoveredBy == "" {
				out.TotalBytes += item.SizeKB * 1024
				out.Rows++
			}
		}
		preview.Sections = append(preview.Sections, out)
	}
	return preview, nil
}

// lastJSONLine returns the last stdout line that parses as a JSON object. The
// clean CLI prints progress text first and its report as the final line.
func lastJSONLine(out []byte) []byte {
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "{") {
			continue
		}
		var probe map[string]any
		if json.Unmarshal([]byte(trimmed), &probe) == nil {
			return []byte(trimmed)
		}
	}
	return nil
}
