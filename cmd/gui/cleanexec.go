//go:build darwin

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Execution safety rails. The GUI only ever hands back paths it obtained from
// a preview, but the server still refuses obviously catastrophic selections —
// upstream's per-path protection (should_protect_path, whitelist, deletion
// policy) applies underneath this as the second and harder layer.
const (
	maxCleanPaths       = 500
	maxCleanPathLength  = 4096
	cleanExecuteTimeout = 10 * time.Minute
	deletionsPollPeriod = 400 * time.Millisecond
	executeBodyLimit    = 1 << 20
)

// validateCleanPaths enforces the dashboard's execution invariant: only
// nested, user-owned paths may run — nothing outside the home directory,
// no home top-level entries (~/Documents, ~/Library itself, …), and never
// Molee's own state. Upstream's per-path protection (should_protect_path,
// whitelist, deletion policy) applies underneath this as the second layer.
func validateCleanPaths(paths []string) error {
	if len(paths) == 0 {
		return errors.New("no paths selected")
	}
	if len(paths) > maxCleanPaths {
		return fmt.Errorf("too many paths selected (max %d)", maxCleanPaths)
	}

	home, err := osUserHomeDir()
	if err != nil || home == "" {
		return errors.New("cannot resolve home directory")
	}
	home = filepath.Clean(home)

	for _, path := range paths {
		if path == "" || len(path) > maxCleanPathLength || strings.ContainsRune(path, 0) {
			return errors.New("invalid path in selection")
		}
		if !filepath.IsAbs(path) {
			return fmt.Errorf("path must be absolute: %q", path)
		}
		cleaned := filepath.Clean(path)
		rel, relErr := filepath.Rel(home, cleaned)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("only paths inside the home directory are supported: %s", cleaned)
		}
		// Refuse home's top-level entries: a single click must never be able
		// to target something as broad as ~/Documents or ~/Library itself.
		if !strings.ContainsRune(rel, filepath.Separator) {
			return fmt.Errorf("refusing home top-level path: %s", cleaned)
		}
		// Molee's own state and logs must never be cleaned through the GUI.
		if rel == filepath.Join(".config", "mole") || rel == filepath.Join("Library", "Logs", "mole") {
			return fmt.Errorf("refusing Molee's own state path: %s", cleaned)
		}
	}
	return nil
}

// CleanProgressEvent is streamed to the dashboard while an execution runs.
type CleanProgressEvent struct {
	Type       string `json:"type"` // start | progress | result
	Path       string `json:"path,omitempty"`
	SizeKB     int64  `json:"size_kb"`
	Status     string `json:"status,omitempty"`
	FreedKB    int64  `json:"freed_kb,omitempty"`
	Items      int    `json:"items,omitempty"`
	Categories int    `json:"categories,omitempty"`
	Cancelled  bool   `json:"cancelled,omitempty"`
	Message    string `json:"message,omitempty"`
}

type cleanExecutionResult struct {
	FreedKB    int64
	Items      int
	Categories int
	Cancelled  bool
	Partial    bool
}

// executeClean runs `mole clean --only-from FILE --json` for the selected
// paths while tailing deletions.log for per-item progress. The tailer starts
// from the file's current size, so only this run's deletions stream out.
func executeClean(ctx context.Context, rootDir string, paths []string, onProgress func(CleanProgressEvent)) (cleanExecutionResult, error) {
	tmp, err := os.CreateTemp("", "molee-only-from-*.txt")
	if err != nil {
		return cleanExecutionResult{}, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	var builder strings.Builder
	for _, path := range paths {
		builder.WriteString(path)
		builder.WriteString("\n")
	}
	if _, err := tmp.WriteString(builder.String()); err != nil {
		tmp.Close()
		return cleanExecutionResult{}, err
	}
	if err := tmp.Close(); err != nil {
		return cleanExecutionResult{}, err
	}

	stopTailer := tailDeletionsLog(onProgress)
	defer stopTailer()

	out, runErr := runCommand(ctx, filepath.Join(rootDir, "mole"), "clean", "--only-from", tmpName, "--json")

	result := cleanExecutionResult{}
	if payload := lastJSONLine(out); payload != nil {
		var cli struct {
			Mode       string `json:"mode"`
			Cancelled  bool   `json:"cancelled"`
			FreedKB    int64  `json:"freed_kb"`
			Items      int    `json:"items"`
			Categories int    `json:"categories"`
			Partial    bool   `json:"partial"`
		}
		if json.Unmarshal(payload, &cli) == nil && cli.Mode == "clean_result" {
			result = cleanExecutionResult{
				FreedKB:    cli.FreedKB,
				Items:      cli.Items,
				Categories: cli.Categories,
				Cancelled:  cli.Cancelled,
				Partial:    cli.Partial,
			}
		}
	}

	if runErr != nil {
		if result.Items == 0 && result.FreedKB == 0 && !result.Cancelled {
			return result, fmt.Errorf("clean execution failed: %w", runErr)
		}
		// The run reported results but exited nonzero: surface both.
		return result, nil
	}
	return result, nil
}

// deletionsLogPath mirrors the CLI's own env override (lib/core/history.sh).
func deletionsLogPath() string {
	if env := os.Getenv("MOLE_DELETE_LOG"); env != "" {
		return env
	}
	home, err := osUserHomeDir()
	if err != nil {
		return filepath.Join(string(filepath.Separator), "dev", "null")
	}
	return filepath.Join(home, "Library", "Logs", "mole", "deletions.log")
}

// tailDeletionsLog polls the deletions TSV (ts, mode, size_kb, status, path)
// from its current end and forwards each new record as a progress event.
func tailDeletionsLog(onProgress func(CleanProgressEvent)) func() {
	path := deletionsLogPath()
	var offset int64
	if info, err := os.Stat(path); err == nil {
		offset = info.Size()
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(deletionsPollPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			if info.Size() < offset {
				offset = 0 // truncated or rotated mid-run
			}
			if info.Size() == offset {
				continue
			}
			file, err := os.Open(path)
			if err != nil {
				continue
			}
			buf := make([]byte, info.Size()-offset)
			_, readErr := file.ReadAt(buf, offset)
			file.Close()
			if readErr != nil {
				continue
			}
			offset = info.Size()
			for _, line := range strings.Split(string(buf), "\n") {
				fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
				if len(fields) != 5 || fields[4] == "" {
					continue
				}
				var sizeKB int64
				_, _ = fmt.Sscanf(fields[2], "%d", &sizeKB)
				onProgress(CleanProgressEvent{
					Type:   "progress",
					Status: fields[3],
					Path:   fields[4],
					SizeKB: sizeKB,
				})
			}
		}
	}()

	return func() {
		close(stop)
		<-done
	}
}
