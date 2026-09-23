//go:build darwin

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// OptimizeReport mirrors `optimize --json`; PurgePreview/PurgeResult mirror
// the `purge --json` documents.
type OptimizeReport struct {
	Mode        string           `json:"mode"`
	DryRun      bool             `json:"dry_run"`
	Applied     int              `json:"applied"`
	Unchanged   int              `json:"unchanged"`
	Skipped     int              `json:"skipped"`
	Unavailable int              `json:"unavailable"`
	Attention   int              `json:"attention"`
	Failed      int              `json:"failed"`
	Actions     []OptimizeAction `json:"actions"`
}

type OptimizeAction struct {
	Action  string `json:"action"`
	Outcome string `json:"outcome"`
}

type PurgePreview struct {
	Mode       string      `json:"mode"`
	Outcome    string      `json:"outcome"`
	Candidates []PurgeItem `json:"candidates"`
}

type PurgeItem struct {
	Path    string `json:"path"`
	SizeKB  int64  `json:"size_kb"`
	Recent  bool   `json:"recent"`
	Project string `json:"project"`
}

type PurgeResult struct {
	Mode         string `json:"mode"`
	Outcome      string `json:"outcome"`
	FreedKB      int64  `json:"freed_kb"`
	Items        int    `json:"items"`
	UnknownSizes int    `json:"unknown_sizes"`
}

const optimizeTimeout = 15 * time.Minute

func defaultRunOptimize(ctx context.Context, rootDir string, dryRun bool) (OptimizeReport, error) {
	ctx, cancel := context.WithTimeout(ctx, optimizeTimeout)
	defer cancel()
	args := []string{"optimize"}
	if dryRun {
		args = append(args, "--dry-run")
	}
	args = append(args, "--json")
	out, err := runCommand(ctx, filepath.Join(rootDir, "mole"), args...)
	if err != nil {
		return OptimizeReport{}, err
	}
	payload := lastJSONLine(out)
	if payload == nil {
		return OptimizeReport{}, fmt.Errorf("optimize produced no JSON summary")
	}
	var report OptimizeReport
	if err := json.Unmarshal(payload, &report); err != nil {
		return OptimizeReport{}, err
	}
	if report.Mode != "optimize_result" {
		return OptimizeReport{}, fmt.Errorf("unexpected optimize report mode %q", report.Mode)
	}
	return report, nil
}

func defaultRunPurgePreview(ctx context.Context, rootDir string) (PurgePreview, error) {
	ctx, cancel := context.WithTimeout(ctx, cleanExecuteTimeout)
	defer cancel()
	out, err := runCommand(ctx, filepath.Join(rootDir, "mole"), "purge", "--dry-run", "--json")
	if err != nil {
		return PurgePreview{}, err
	}
	return parsePurgePreview(out)
}

func parsePurgePreview(out []byte) (PurgePreview, error) {
	payload := lastJSONLine(out)
	if payload == nil {
		return PurgePreview{}, fmt.Errorf("purge produced no JSON summary")
	}
	var preview PurgePreview
	if err := json.Unmarshal(payload, &preview); err != nil {
		return PurgePreview{}, err
	}
	if preview.Mode != "purge_preview" {
		return PurgePreview{}, fmt.Errorf("unexpected purge report mode %q", preview.Mode)
	}
	return preview, nil
}

// defaultRunPurgeExecute restricts the purge to the listed artifact paths and
// tails operations.log for per-item removal progress.
func defaultRunPurgeExecute(ctx context.Context, rootDir string, paths []string, onProgress func(CleanProgressEvent)) (PurgeResult, error) {
	tmp, err := os.CreateTemp("", "molee-purge-only-*.txt")
	if err != nil {
		return PurgeResult{}, err
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
		return PurgeResult{}, err
	}
	if err := tmp.Close(); err != nil {
		return PurgeResult{}, err
	}

	stopTailer := tailOperationRemovals("purge", onProgress)
	defer stopTailer()

	ctx, cancel := context.WithTimeout(ctx, cleanExecuteTimeout)
	defer cancel()
	out, runErr := runCommand(ctx, filepath.Join(rootDir, "mole"),
		"purge", "--only-from", tmpName, "--yes", "--json")

	result := PurgeResult{Outcome: "unknown"}
	if payload := lastJSONLine(out); payload != nil {
		_ = json.Unmarshal(payload, &result)
	}
	if runErr != nil && result.Items == 0 && result.FreedKB == 0 {
		return result, fmt.Errorf("purge execution failed: %w", runErr)
	}
	return result, nil
}

// operationsRemovalPattern matches "[ts] [purge] REMOVED /path (1.2MB)".
var operationsRemovalPattern = regexp.MustCompile(`\[[^\]]+\] \[(\w+)\] REMOVED (.+) \(([^)]+)\)$`)

// tailOperationRemovals polls operations.log from its current end and forwards
// REMOVED entries of the given command as progress events.
func tailOperationRemovals(command string, onProgress func(CleanProgressEvent)) func() {
	path := operationsLogPath()
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
				match := operationsRemovalPattern.FindStringSubmatch(strings.TrimSpace(line))
				if match == nil || match[1] != command {
					continue
				}
				onProgress(CleanProgressEvent{
					Type:   "progress",
					Status: "removed",
					Path:   match[2],
					SizeKB: humanSizeToKB(match[3]),
				})
			}
		}
	}()

	return func() {
		close(stop)
		<-done
	}
}

func operationsLogPath() string {
	if env := os.Getenv("MOLE_OPERATIONS_LOG"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, "Library", "Logs", "mole", "operations.log")
}

// humanSizeToKB converts the CLI's human sizes ("1.20GB", "12.8MB", "3616B")
// back to kilobytes for the dashboard's running total.
func humanSizeToKB(display string) int64 {
	re := regexp.MustCompile(`^([\d.]+)(B|KB|MB|GB|TB)$`)
	m := re.FindStringSubmatch(display)
	if m == nil {
		return 0
	}
	value, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	mult := map[string]float64{"B": 1, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12}[m[2]]
	return int64(value * mult / 1024)
}
