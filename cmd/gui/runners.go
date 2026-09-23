//go:build darwin

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Subprocess timeouts. Sizing a full cleanup preview can legitimately take
// minutes on cold caches, so it gets the widest budget.
const (
	analyzeTimeout       = 3 * time.Minute
	cleanPreviewTimeout  = 5 * time.Minute
	uninstallListTimeout = 2 * time.Minute
	historyTimeout       = 30 * time.Second
	sseHeartbeatInterval = 15 * time.Second
	scannerMaxLine       = 4 << 20 // status snapshots can carry 120-sample histories
)

func defaultRunAnalyze(ctx context.Context, scriptDir, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, analyzeTimeout)
	defer cancel()
	args := []string{"--json"}
	if path != "" {
		args = append(args, path)
	}
	return runCommand(ctx, filepath.Join(scriptDir, "analyze.sh"), args...)
}

func defaultRunCleanPreview(ctx context.Context, rootDir string) (CleanPreview, error) {
	ctx, cancel := context.WithTimeout(ctx, cleanPreviewTimeout)
	defer cancel()
	// Prefer the CLI's structured summary; fall back to the preview ledger
	// when an older CLI without `--json` is deployed.
	if out, err := runCommand(ctx, filepath.Join(rootDir, "mole"), "clean", "--dry-run", "--json"); err == nil {
		if payload := lastJSONLine(out); payload != nil {
			return cleanPreviewFromCLIJSON(payload)
		}
	}

	if _, err := runCommand(ctx, filepath.Join(rootDir, "mole"), "clean", "--dry-run"); err != nil {
		return CleanPreview{}, fmt.Errorf("clean --dry-run failed: %w", err)
	}
	data, err := osReadFile(cleanListPath())
	if err != nil {
		return CleanPreview{}, fmt.Errorf("read preview ledger: %w", err)
	}
	return parseCleanList(data), nil
}

func defaultRunCleanExecute(ctx context.Context, rootDir string, paths []string, onProgress func(CleanProgressEvent)) (cleanExecutionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, cleanExecuteTimeout)
	defer cancel()
	return executeClean(ctx, rootDir, paths, onProgress)
}

func defaultRunUninstallList(ctx context.Context, rootDir string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, uninstallListTimeout)
	defer cancel()
	// The CLI auto-switches to JSON when stdout is not a terminal.
	return runCommand(ctx, filepath.Join(rootDir, "mole"), "uninstall", "--list")
}

func defaultRunHistory(ctx context.Context, rootDir string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	return runCommand(ctx, filepath.Join(rootDir, "mole"), "history", "--json")
}

func cleanListPath() string {
	home, err := osUserHomeDir()
	if err != nil {
		home = "~"
	}
	return filepath.Join(home, ".config", "mole", "clean-list.txt")
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = nil // non-TTY stdin keeps the CLI on its unattended paths
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, tailString(errBuf.String(), 400))
	}
	return out.Bytes(), nil
}

// handleStatusStream relays `status --watch` NDJSON lines as Server-Sent
// Events. One child process per subscriber; it dies with the request context.
func (s *Server) handleStatusStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	ctx := r.Context()
	cmd := exec.CommandContext(ctx, filepath.Join(s.cfg.ScriptDir, "status.sh"),
		"--watch", "--interval", s.cfg.Interval)
	cmd.Stdin = nil
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := cmd.Start(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("start status: %v", err))
		return
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	// Tell browsers to reconnect after 3s if the collector exits.
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()

	lines := make(chan string, 8)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), scannerMaxLine)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()

	heartbeat := time.NewTicker(sseHeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case line, ok := <-lines:
			if !ok {
				if errText := tailString(stderr.String(), 300); errText != "" {
					fmt.Fprintf(w, "event: collector-error\ndata: %s\n\n", jsonEscape(errText))
				} else {
					fmt.Fprint(w, "event: end\n\n")
				}
				flusher.Flush()
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", line); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func jsonEscape(s string) string {
	buf, err := jsonMarshal(s)
	if err != nil {
		return `""`
	}
	return string(buf)
}

// Test seams for filesystem access.
var (
	osReadFile    = os.ReadFile
	osUserHomeDir = os.UserHomeDir
	jsonMarshal   = json.Marshal
)
