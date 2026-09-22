//go:build darwin

package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
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
	runCleanPreview  func(ctx context.Context, rootDir string) ([]byte, error)
	runUninstallList func(ctx context.Context, rootDir string) ([]byte, error)
	runHistory       func(ctx context.Context, rootDir string) ([]byte, error)

	staticFS fs.FS
}

func newServer(cfg serverConfig) *Server {
	s := &Server{cfg: cfg}
	s.runAnalyze = defaultRunAnalyze
	s.runCleanPreview = defaultRunCleanPreview
	s.runUninstallList = defaultRunUninstallList
	s.runHistory = defaultRunHistory

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
	out, err := s.runCleanPreview(r.Context(), s.cfg.RootDir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, parseCleanList(out))
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
