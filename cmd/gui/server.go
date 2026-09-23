//go:build darwin

package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const authCookie = "molee_token"

type serverConfig struct {
	// ScriptDir holds status.sh/analyze.sh/gui-go; RootDir holds the `mole`
	// entrypoint. Both come from bin/gui.sh via MOLEE_SCRIPT_DIR.
	ScriptDir string
	RootDir   string
	Token     string
	Interval  string
	StaticDir string // optional dev override for the embedded bundle
}

type Server struct {
	cfg serverConfig

	// Runner hooks, overridable in tests to stub the CLI subprocesses.
	runAnalyze       func(ctx context.Context, scriptDir, path string) ([]byte, error)
	runCleanPreview  func(ctx context.Context, rootDir string) (CleanPreview, error)
	runCleanExecute  func(ctx context.Context, rootDir string, paths []string, onProgress func(CleanProgressEvent)) (cleanExecutionResult, error)
	runUninstallList func(ctx context.Context, rootDir string) ([]byte, error)
	runHistory       func(ctx context.Context, rootDir string) ([]byte, error)
	runOptimize      func(ctx context.Context, rootDir string, dryRun bool) (OptimizeReport, error)
	runPurgePreview  func(ctx context.Context, rootDir string) (PurgePreview, error)
	runPurgeExecute  func(ctx context.Context, rootDir string, paths []string, onProgress func(CleanProgressEvent)) (PurgeResult, error)

	staticFS fs.FS
}

func newServer(cfg serverConfig) *Server {
	s := &Server{cfg: cfg}
	s.runAnalyze = defaultRunAnalyze
	s.runCleanPreview = defaultRunCleanPreview
	s.runCleanExecute = defaultRunCleanExecute
	s.runUninstallList = defaultRunUninstallList
	s.runHistory = defaultRunHistory
	s.runOptimize = defaultRunOptimize
	s.runPurgePreview = defaultRunPurgePreview
	s.runPurgeExecute = defaultRunPurgeExecute

	if cfg.StaticDir != "" {
		s.staticFS = os.DirFS(cfg.StaticDir)
	} else {
		sub, err := fs.Sub(embeddedStatic, "static")
		if err != nil {
			panic("embedded static bundle missing: " + err.Error())
		}
		s.staticFS = sub
	}
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/", s.requireAuth(http.HandlerFunc(s.handleRoot)))
	mux.Handle("/static/", s.requireAuth(http.StripPrefix("/static/", s.staticHandler())))
	mux.Handle("/api/meta", s.requireAuth(http.HandlerFunc(s.handleMeta)))
	mux.Handle("/api/status/stream", s.requireAuth(http.HandlerFunc(s.handleStatusStream)))
	mux.Handle("/api/analyze", s.requireAuth(http.HandlerFunc(s.handleAnalyze)))
	mux.Handle("/api/clean/preview", s.requireAuth(http.HandlerFunc(s.handleCleanPreview)))
	mux.Handle("/api/clean/execute", s.requireAuth(http.HandlerFunc(s.handleCleanExecute)))
	mux.Handle("/api/whitelist", s.requireAuth(http.HandlerFunc(s.handleWhitelist)))
	mux.Handle("/api/optimize", s.requireAuth(http.HandlerFunc(s.handleOptimize)))
	mux.Handle("/api/purge/preview", s.requireAuth(http.HandlerFunc(s.handlePurgePreview)))
	mux.Handle("/api/purge/execute", s.requireAuth(http.HandlerFunc(s.handlePurgeExecute)))
	mux.Handle("/api/uninstall/list", s.requireAuth(http.HandlerFunc(s.handleUninstallList)))
	mux.Handle("/api/history", s.requireAuth(http.HandlerFunc(s.handleHistory)))
	return hostGuard(originGuard(mux))
}

func (s *Server) staticHandler() http.Handler {
	return http.FileServer(http.FS(s.staticFS))
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	serveStaticFile(w, r, s.staticFS, "index.html")
}

func serveStaticFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) {
	f, err := fsys.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	seeker, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "asset not seekable", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), seeker)
}

// hostGuard rejects requests whose Host header is not loopback, defending
// against DNS-rebinding style access from remote origins.
func hostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			host = h
		}
		if !isLoopbackHost(host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originGuard rejects cross-site requests: when a browser sends Origin or
// Referer, they must point at the same host the server saw.
func originGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			for _, header := range []string{"Origin", "Referer"} {
				raw := r.Header.Get(header)
				if raw == "" {
					continue
				}
				u, err := url.Parse(raw)
				if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
					http.Error(w, "cross-site request rejected", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.validToken(r) {
			// Browser navigations to / get the page itself, which renders the
			// token prompt; only programmatic clients see the 401 body.
			if r.URL.Path == "/" && strings.Contains(r.Header.Get("Accept"), "text/html") {
				next.ServeHTTP(w, r)
				return
			}
			writeJSONError(w, http.StatusUnauthorized, "token required: launch via `mee gui` or pass ?token=<token>")
			return
		}
		// A token supplied via query string upgrades to a cookie so asset and
		// API requests from the page keep working without the raw token. The
		// page itself is served on the same response — no redirect — because
		// some browsers race the cookie write against the redirect follow.
		if r.URL.Query().Get("token") == s.cfg.Token && r.URL.Path == "/" {
			http.SetCookie(w, &http.Cookie{
				Name:     authCookie,
				Value:    s.cfg.Token,
				Path:     "/",
				MaxAge:   86400,
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			// The SPA strips the token from the address bar via
			// history.replaceState once it is authed.
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) validToken(r *http.Request) bool {
	token := s.cfg.Token
	if c, err := r.Cookie(authCookie); err == nil && secureEqual(c.Value, token) {
		return true
	}
	if q := r.URL.Query().Get("token"); q != "" && secureEqual(q, token) {
		return true
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		if secureEqual(strings.TrimPrefix(auth, "Bearer "), token) {
			return true
		}
	}
	return false
}

func secureEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Server) handleMeta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":     appName,
		"version":  appVersion,
		"tagline":  appTagline,
		"interval": s.cfg.Interval,
		"readonly": true,
	})
}

func (s *Server) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	out, err := s.runAnalyze(r.Context(), s.cfg.ScriptDir, path)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func (s *Server) handleCleanPreview(w http.ResponseWriter, r *http.Request) {
	preview, err := s.runCleanPreview(r.Context(), s.cfg.RootDir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

// handleCleanExecute streams a restricted cleanup as Server-Sent Events over
// POST (the dashboard reads the stream with fetch; EventSource cannot POST).
func (s *Server) handleCleanExecute(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	var req struct {
		Paths []string `json:"paths"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, executeBodyLimit))
	if err != nil || json.Unmarshal(body, &req) != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateCleanPaths(req.Paths); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx := r.Context()
	events := make(chan CleanProgressEvent, 64)
	go func() {
		defer close(events)
		result, execErr := s.runCleanExecute(ctx, s.cfg.RootDir, req.Paths, func(ev CleanProgressEvent) {
			select {
			case events <- ev:
			case <-ctx.Done():
			}
		})
		final := CleanProgressEvent{
			Type:       "result",
			FreedKB:    result.FreedKB,
			Items:      result.Items,
			Categories: result.Categories,
			Cancelled:  result.Cancelled,
		}
		if execErr != nil {
			final.Message = execErr.Error()
		}
		select {
		case events <- final:
		case <-ctx.Done():
		}
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	startEvent, _ := json.Marshal(CleanProgressEvent{Type: "start"})
	fmt.Fprintf(w, "data: %s\n\n", startEvent)
	flusher.Flush()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				fmt.Fprint(w, "event: end\n\n")
				flusher.Flush()
				return
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

// handleOptimize runs a dry-run inspection or a real optimize pass and
// returns the CLI's structured report. `{"dry_run":true}` previews.
func (s *Server) handleOptimize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		DryRun bool `json:"dry_run"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, executeBodyLimit))
	if err != nil || len(body) == 0 || json.Unmarshal(body, &req) != nil {
		req.DryRun = false
	}
	report, err := s.runOptimize(r.Context(), s.cfg.RootDir, req.DryRun)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// handlePurgePreview returns the scanned project artifacts as candidates.
func (s *Server) handlePurgePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	preview, err := s.runPurgePreview(r.Context(), s.cfg.RootDir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

// handlePurgeExecute streams a restricted purge as Server-Sent Events over
// POST, mirroring the clean execution flow.
func (s *Server) handlePurgeExecute(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	var req struct {
		Paths []string `json:"paths"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, executeBodyLimit))
	if err != nil || json.Unmarshal(body, &req) != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateCleanPaths(req.Paths); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx := r.Context()
	events := make(chan CleanProgressEvent, 64)
	go func() {
		defer close(events)
		result, execErr := s.runPurgeExecute(ctx, s.cfg.RootDir, req.Paths, func(ev CleanProgressEvent) {
			select {
			case events <- ev:
			case <-ctx.Done():
			}
		})
		final := CleanProgressEvent{
			Type:      "result",
			FreedKB:   result.FreedKB,
			Items:     result.Items,
			Cancelled: result.Outcome != "completed",
		}
		if execErr != nil {
			final.Message = execErr.Error()
		}
		select {
		case events <- final:
		case <-ctx.Done():
		}
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	startEvent, _ := json.Marshal(CleanProgressEvent{Type: "start"})
	fmt.Fprintf(w, "data: %s\n\n", startEvent)
	flusher.Flush()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				fmt.Fprint(w, "event: end\n\n")
				flusher.Flush()
				return
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

func (s *Server) handleWhitelist(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = whitelistKindClean
	}
	if kind != whitelistKindClean && kind != whitelistKindOptimize {
		writeJSONError(w, http.StatusBadRequest, "unknown whitelist kind")
		return
	}

	switch r.Method {
	case http.MethodGet:
		entries, err := readWhitelist(kind)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"path":    whitelistFilePath(kind),
			"kind":    kind,
			"entries": entries,
		})
	case http.MethodPut:
		var req struct {
			Entries []string `json:"entries"`
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, executeBodyLimit))
		if err != nil || json.Unmarshal(body, &req) != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := writeWhitelist(kind, req.Entries); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		entries, _ := readWhitelist(kind)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(entries)})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleUninstallList(w http.ResponseWriter, r *http.Request) {
	out, err := s.runUninstallList(r.Context(), s.cfg.RootDir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	out, err := s.runHistory(r.Context(), s.cfg.RootDir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func tailString(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
