//go:build darwin

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

const (
	appName    = "Molee"
	appVersion = "0.1.0"
	appTagline = "Deep clean and optimize your Mac, with a local web dashboard."
)

var (
	listenAddr  = flag.String("addr", "127.0.0.1:0", "listen address; loopback only")
	openBrowser = flag.Bool("open", true, "open the dashboard in the default browser")
	interval    = flag.String("interval", "2s", "status stream interval (e.g. 1s, 2s)")
	staticDir   = flag.String("dev-static", "", "serve static assets from a directory instead of the embedded bundle (development)")
	tokenFlag   = flag.String("token", "", "auth token (default: random per launch)")
)

// scriptDir resolves the directory holding status.sh/analyze.sh. bin/gui.sh
// exports MOLEE_SCRIPT_DIR; running the raw binary falls back to the
// executable's own directory so `go run ./cmd/gui` from bin/ still works.
func scriptDir() (string, error) {
	if env := os.Getenv("MOLEE_SCRIPT_DIR"); env != "" {
		if abs, err := filepath.Abs(env); err == nil {
			return abs, nil
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

func randomToken() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		log.Fatalf("generate token: %v", err)
	}
	return hex.EncodeToString(buf)
}

func main() {
	flag.Parse()

	if _, err := time.ParseDuration(*interval); err != nil {
		log.Fatalf("invalid --interval %q: %v", *interval, err)
	}
	scriptDirPath, err := scriptDir()
	if err != nil {
		log.Fatalf("resolve script dir: %v", err)
	}

	token := *tokenFlag
	if token == "" {
		token = randomToken()
	}

	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("listen on %s: %v", *listenAddr, err)
	}
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	baseURL := fmt.Sprintf("http://%s:%s", hostForURL(host), port)

	srv := newServer(serverConfig{
		ScriptDir: scriptDirPath,
		RootDir:   filepath.Dir(scriptDirPath),
		Token:     token,
		Interval:  *interval,
		StaticDir: *staticDir,
	})

	printBanner(baseURL, token, *openBrowser)
	if *openBrowser {
		if cmd := exec.Command("open", baseURL); cmd.Start() == nil {
			_ = cmd.Process.Release()
		}
	}

	httpSrv := &http.Server{Handler: srv.Handler()}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
	log.Printf("%s: dashboard stopped", appName)
}

func hostForURL(host string) string {
	if host == "::" || host == "[::]" {
		return "[::1]"
	}
	return host
}

func printBanner(baseURL, token string, withToken bool) {
	line := baseURL
	if withToken {
		line = baseURL + "/?token=" + token
	}
	fmt.Printf(`
  %s %s
  %s

  Dashboard:  %s
  Token:      %s
  Bind:       loopback only (never exposed to the network)
  Stop:       Ctrl+C

`, appName, appVersion, appTagline, line, token)
}
