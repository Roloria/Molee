//go:build darwin

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The clean whitelist is a plain newline-separated pattern file at
// ~/.config/mole/whitelist (see lib/manage/whitelist.sh). The editor keeps the
// format untouched: one pattern per line, # comments and blanks preserved only
// as authored by the user — we round-trip exactly what is in the box.
const (
	maxWhitelistEntries    = 500
	maxWhitelistLineLength = 4096
)

func whitelistFilePath() string {
	home, err := osUserHomeDir()
	if err != nil {
		return filepath.Join(string(filepath.Separator), "dev", "null")
	}
	return filepath.Join(home, ".config", "mole", "whitelist")
}

func readWhitelist() ([]string, error) {
	data, err := osReadFile(whitelistFilePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return []string{}, nil
	}
	lines := strings.Split(trimmed, "\n")
	entries := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		entries = append(entries, line)
	}
	return entries, nil
}

func writeWhitelist(entries []string) error {
	if len(entries) > maxWhitelistEntries {
		return fmt.Errorf("too many entries (max %d)", maxWhitelistEntries)
	}
	var builder strings.Builder
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		entry = strings.TrimRight(entry, "\r")
		if strings.TrimSpace(entry) == "" {
			continue
		}
		if len(entry) > maxWhitelistLineLength {
			return fmt.Errorf("entry too long: %.40s…", entry)
		}
		if strings.ContainsFunc(entry, func(r rune) bool { return r < 0x20 && r != '\t' }) {
			return fmt.Errorf("entry contains control characters: %.40s…", entry)
		}
		if seen[entry] {
			continue
		}
		seen[entry] = true
		builder.WriteString(entry)
		builder.WriteString("\n")
	}

	path := whitelistFilePath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Atomic replace so a partial write can never weaken protection.
	tmp, err := os.CreateTemp(dir, ".whitelist-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(builder.String()); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
