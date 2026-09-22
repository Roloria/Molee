//go:build darwin

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s := newServer(serverConfig{
		ScriptDir: t.TempDir(),
		RootDir:   t.TempDir(),
		Token:     "testtoken123",
		Interval:  "2s",
	})
	// Minimal bundle so / serves; API routes never touch static assets.
	s.staticFS = fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>molee</title>")},
	}
	return s
}

func get(handler http.Handler, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = "127.0.0.1:8080"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestAuthRequired(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()

	if rec := get(h, "/api/meta"); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", rec.Code)
	}
	if rec := get(h, "/api/meta?token=wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want 401", rec.Code)
	}
	if rec := get(h, "/api/meta?token=testtoken123"); rec.Code != http.StatusOK {
		t.Errorf("query token: status = %d, want 200", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/meta", nil)
	req.Host = "127.0.0.1:8080"
	req.AddCookie(&http.Cookie{Name: authCookie, Value: "testtoken123"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("cookie token: status = %d, want 200", rec.Code)
	}

	req.Header.Set("Authorization", "Bearer testtoken123")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("bearer token: status = %d, want 200", rec.Code)
	}
}

// A browser navigation to / without a token gets the page (which renders the
// token prompt); programmatic clients without Accept: text/html get 401.
func TestBrowserNavigationWithoutTokenServesPrompt(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("browser navigation: status = %d, want 200", rec.Code)
	}

	rec = get(h, "/")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("non-browser request: status = %d, want 401", rec.Code)
	}
}

func TestQueryTokenSetsCookieAndServesPage(t *testing.T) {
	s := newTestServer(t)
	rec := get(s.Handler(), "/?token=testtoken123")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (page served on the token response, no redirect)", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Value != "testtoken123" {
		t.Errorf("cookie not set: %#v", cookies)
	}
}

func TestHostGuardRejectsRemoteHost(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/meta?token=testtoken123", nil)
	req.Host = "evil.example.com"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("remote host: status = %d, want 403", rec.Code)
	}
}

func TestOriginGuardRejectsCrossSitePost(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/clean/preview?token=testtoken123", nil)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site POST: status = %d, want 403", rec.Code)
	}
}

func TestAnalyzePassthrough(t *testing.T) {
	s := newTestServer(t)
	called := ""
	s.runAnalyze = func(_ context.Context, scriptDir, path string) ([]byte, error) {
		called = path
		return []byte(`{"path":"` + path + `","total_size":5}`), nil
	}
	rec := get(s.Handler(), "/api/analyze?token=testtoken123&path=%2FUsers%2Fme")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if called != "/Users/me" {
		t.Errorf("runAnalyze path = %q", called)
	}
	if body := rec.Body.String(); body == "" || body[0] != '{' {
		t.Errorf("body = %q", body)
	}
}

func TestCleanPreviewParsesLedger(t *testing.T) {
	s := newTestServer(t)
	s.runCleanPreview = func(_ context.Context, _ string) ([]byte, error) {
		return []byte("# Mole Cleanup Preview - X\n=== S ===\n/tmp/a  # 1KB\n"), nil
	}
	rec := get(s.Handler(), "/api/clean/preview?token=testtoken123")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	want := `{"generated_at":"X","sections":[{"name":"S","items":[{"path":"/tmp/a","size_bytes":1000,"size_display":"1KB","items":1,"size_known":true}],"total_bytes":1000,"rows":1}],"total_bytes":1000,"total_known":true,"partial":false,"rows":1,"items":1}`
	if got := rec.Body.String(); got != want+"\n" {
		t.Errorf("body = %q, want %q", got, want+"\n")
	}
}

func TestRunnerErrorSurfaces(t *testing.T) {
	s := newTestServer(t)
	s.runHistory = func(_ context.Context, _ string) ([]byte, error) {
		return nil, &testError{"boom"}
	}
	rec := get(s.Handler(), "/api/history?token=testtoken123")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
