// Command tokentracer is a local proxy that records what every LLM request
// actually cost, and shows where the money leaks.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/guydelarea/tokentracer/internal/api"
	"github.com/guydelarea/tokentracer/internal/proxy"
	"github.com/guydelarea/tokentracer/internal/record"
	"github.com/guydelarea/tokentracer/internal/store"
)

// shutdownGrace bounds the whole shutdown: in-flight streams, the proxy's 30s
// abort drains, the recorder's queue. A blown deadline is WAL-crash-equivalent,
// which SQLite survives — but the tail of a session is where the interesting
// requests are, so we wait for it.
const shutdownGrace = 35 * time.Second

// banner is tokentrace's ANSI Shadow wordmark, extended with the trailing R.
const banner = `
████████╗ ██████╗ ██╗  ██╗███████╗███╗   ██╗████████╗██████╗  █████╗  ██████╗███████╗██████╗
╚══██╔══╝██╔═══██╗██║ ██╔╝██╔════╝████╗  ██║╚══██╔══╝██╔══██╗██╔══██╗██╔════╝██╔════╝██╔══██╗
   ██║   ██║   ██║█████╔╝ █████╗  ██╔██╗ ██║   ██║   ██████╔╝███████║██║     █████╗  ██████╔╝
   ██║   ██║   ██║██╔═██╗ ██╔══╝  ██║╚██╗██║   ██║   ██╔══██╗██╔══██║██║     ██╔══╝  ██╔══██╗
   ██║   ╚██████╔╝██║  ██╗███████╗██║ ╚████║   ██║   ██║  ██║██║  ██║╚██████╗███████╗██║  ██║
   ╚═╝    ╚═════╝ ╚═╝  ╚═╝╚══════╝╚═╝  ╚═══╝   ╚═╝   ╚═╝  ╚═╝╚═╝  ╚═╝ ╚═════╝╚══════╝╚═╝  ╚═╝
`

type config struct {
	Port     string
	Upstream string
	DBPath   string
}

func loadConfig() config {
	return config{
		Port:     env("PORT", "8787"),
		Upstream: env("UPSTREAM", "https://api.anthropic.com"),
		DBPath:   env("TOKENTRACER_DB", "./tokentracer.db"),
	}
}

// envFile is where the setup wizard saves its answer, next to the DB in cwd.
const envFile = ".env"

// loadEnvFile sets KEY=value pairs from path into the process env, but never
// overrides a variable the shell already set — the shell is the louder opinion.
func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

func writeEnvFile(path string, kv map[string]string) error {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# tokentracer config — edit, or re-run `go run ./cmd/tokentracer setup`\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, kv[k])
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// vertexUpstream is the Google endpoint a region implies — including the /v1
// that Claude Code's Vertex paths do not carry.
func vertexUpstream(region string) string {
	if region == "" || region == "global" {
		return "https://aiplatform.googleapis.com/v1"
	}
	return fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1", region)
}

// runSetup asks which client this proxy fronts and saves the upstream that
// answer implies. The upstream is the only fact the proxy needs — the launch
// line is derived from it, so no client name is stored.
func runSetup() {
	r := bufio.NewReader(os.Stdin)
	// /dev/null passes the TTY check (it is a char device), so EOF is the real
	// "nobody is answering" signal: default everything and persist nothing.
	interactive := true
	ask := func(prompt string) string {
		fmt.Print(prompt)
		s, err := r.ReadString('\n')
		if err != nil {
			interactive = false
		}
		return strings.TrimSpace(s)
	}

	fmt.Println("tokentracer: first-run setup — which client?")
	fmt.Println("  1) Claude Code — Anthropic API (default)")
	fmt.Println("  2) Claude Code — Vertex AI")
	fmt.Println("  3) Other — paste an upstream base URL")

	var up string
	switch ask("Choice [1]: ") {
	case "2":
		up = vertexUpstream(ask("Vertex region, e.g. us-east5 (blank = global): "))
	case "3":
		if up = ask("Upstream base URL: "); up == "" {
			up = "https://api.anthropic.com"
		}
	default:
		up = "https://api.anthropic.com"
	}

	if interactive {
		if err := writeEnvFile(envFile, map[string]string{"UPSTREAM": up}); err != nil {
			fmt.Fprintf(os.Stderr, "tokentracer: could not write %s: %v\n", envFile, err)
		} else {
			fmt.Printf("tokentracer: saved %s — re-run `go run ./cmd/tokentracer setup` to change it\n", envFile)
		}
	}
	os.Setenv("UPSTREAM", up)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// launchLine is the one env-and-command line that points Claude Code at the
// proxy. Vertex needs different env than Anthropic, and the upstream already
// says which backend this is.
func launchLine(cfg config) string {
	if strings.Contains(cfg.Upstream, "googleapis") {
		return fmt.Sprintf("CLAUDE_CODE_USE_VERTEX=1 ANTHROPIC_VERTEX_BASE_URL=http://localhost:%s claude", cfg.Port)
	}
	return fmt.Sprintf("ANTHROPIC_BASE_URL=http://localhost:%s claude", cfg.Port)
}

// app is the wiring: store → recorder → proxy, plus the dashboard reading the
// same store. The E2E test builds one of these too, which is the point.
type app struct {
	handler  http.Handler
	store    *store.Store
	recorder *record.Recorder
}

func newApp(cfg config) (*app, error) {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}

	rec := record.New(st)

	p, err := proxy.New(cfg.Upstream, rec)
	if err != nil {
		st.Close()
		return nil, err
	}

	port, _ := strconv.Atoi(cfg.Port)
	dash := api.Handler(st, api.Config{Port: port, Upstream: cfg.Upstream})

	// Everything that isn't ours belongs to the client's API call.
	mux := http.NewServeMux()
	mux.Handle("/", p)
	mux.Handle("/dashboard", dash)
	mux.Handle("/api/", dash)
	mux.Handle("/web/", dash)

	return &app{handler: mux, store: st, recorder: rec}, nil
}

// close runs the shutdown order that guarantees nothing recorded is lost:
// the server stops first (so no new exchanges arrive), then the recorder drains
// what it already has, and only then does the database close under it.
func (a *app) close() {
	a.recorder.Close()
	a.store.Close()
}

func main() {
	fmt.Fprint(os.Stderr, banner)

	// A saved .env counts as configured; the shell env outranks it. The wizard
	// runs on an explicit `setup`, or on first run — when nothing configured the
	// upstream and there is a terminal to ask on (pipes and CI get the default).
	loadEnvFile(envFile)
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		runSetup()
	} else if os.Getenv("UPSTREAM") == "" && stdinIsTTY() {
		runSetup()
	}

	cfg := loadConfig()

	a, err := newApp(cfg)
	if err != nil {
		log.Fatalf("tokentracer: %v", err)
	}

	addr := "127.0.0.1:" + cfg.Port
	srv := &http.Server{Addr: addr, Handler: a.handler}

	// Catch the signal before ListenAndServe, so a Ctrl-C during startup still
	// takes the clean path.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("tokentracer: http://%s → %s  (db: %s)", addr, cfg.Upstream, cfg.DBPath)
		log.Printf("tokentracer: point your client at it — %s", launchLine(cfg))
		log.Printf("tokentracer: dashboard — http://localhost:%s/dashboard", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("tokentracer: %v", err)
		}
	}()

	<-ctx.Done()
	stop() // a second Ctrl-C now kills the process outright

	log.Printf("tokentracer: shutting down — flushing the queue (up to %s)", shutdownGrace)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("tokentracer: server shutdown: %v", err)
	}
	a.close()
	log.Print("tokentracer: done")
}
