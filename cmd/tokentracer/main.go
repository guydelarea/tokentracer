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
	fmt.Println("  1) Claude Code / Pi — Anthropic API (default)")
	fmt.Println("  2) Claude Code — Vertex AI")
	fmt.Println("  3) Codex / OpenCode / Pi — ChatGPT login")
	fmt.Println("  4) Codex / OpenCode / Pi — OpenAI API key")
	fmt.Println("  5) Other — paste an upstream base URL")

	var up string
	switch ask("Choice [1]: ") {
	case "2":
		up = vertexUpstream(ask("Vertex region, e.g. us-east5 (blank = global): "))
	case "3":
		up = "https://chatgpt.com/backend-api/codex"
	case "4":
		up = "https://api.openai.com/v1"
	case "5":
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

// launchLines are copy-paste commands that point clients at the proxy. The
// upstream identifies the wire family; Codex and OpenCode use the same OpenAI
// Responses endpoint but expose their base URL overrides differently.
func launchLines(cfg config) []string {
	base := "http://localhost:" + cfg.Port
	if strings.Contains(cfg.Upstream, "googleapis") {
		return []string{"Claude Code: CLAUDE_CODE_USE_VERTEX=1 ANTHROPIC_VERTEX_BASE_URL=" + base + " claude"}
	}
	if strings.Contains(cfg.Upstream, "api.openai.com") || strings.Contains(cfg.Upstream, "backend-api/codex") {
		provider := "openai"
		if strings.Contains(cfg.Upstream, "backend-api/codex") {
			provider = "openai-codex"
		}
		return []string{
			fmt.Sprintf(`Codex: codex -c 'openai_base_url="%s"'`, base),
			fmt.Sprintf(`OpenCode: OPENCODE_CONFIG_CONTENT='{"provider":{"openai":{"options":{"baseURL":"%s"}}}}' opencode`, base),
			piLaunchLine(provider, base),
		}
	}
	if strings.Contains(cfg.Upstream, "api.anthropic.com") {
		return []string{
			"Claude Code: ANTHROPIC_BASE_URL=" + base + " claude",
			fmt.Sprintf(`OpenCode: OPENCODE_CONFIG_CONTENT='{"provider":{"anthropic":{"options":{"baseURL":"%s"}}}}' opencode`, base),
			piLaunchLine("anthropic", base),
		}
	}
	return []string{"OpenCode/Pi: set the selected provider's base URL to " + base}
}

func piLaunchLine(provider, base string) string {
	return fmt.Sprintf(`Pi: set ~/.pi/agent/models.json providers.%s.baseUrl="%s", then run pi --provider %s`, provider, base, provider)
}

// app is the wiring: store → recorder → proxy, plus the dashboard reading the
// same store. The E2E test builds one of these too, which is the point.
type app struct {
	handler  http.Handler
	store    *store.Store
	recorder *record.Recorder
	proxy    *proxy.Proxy

	stopSweep chan struct{} // closed to end the retention sweeper
	sweepDone chan struct{} // closed when it has ended
}

// sweepEvery is how often the capture retention window is enforced. Hourly:
// the windows on offer are a day and up, so nothing finer would delete anything
// sooner, and the setting is swept the moment it changes anyway.
const sweepEvery = time.Hour

// sweepLoop enforces retention — once at startup, so a window set last session
// applies before the dashboard is even open, then on the tick. It does nothing
// at all unless someone chose a window; the default is off.
func (a *app) sweepLoop(every time.Duration) {
	defer close(a.sweepDone)

	t := time.NewTicker(every)
	defer t.Stop()

	for {
		if n, err := api.Sweep(a.store, time.Now()); err != nil {
			log.Printf("tokentracer: capture sweep: %v", err)
		} else if n > 0 {
			log.Printf("tokentracer: capture sweep pruned %d captures", n)
		}
		select {
		case <-a.stopSweep:
			return
		case <-t.C:
		}
	}
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

	a := &app{
		handler:   mux,
		store:     st,
		recorder:  rec,
		proxy:     p,
		stopSweep: make(chan struct{}),
		sweepDone: make(chan struct{}),
	}
	go a.sweepLoop(sweepEvery)
	return a, nil
}

// close runs the shutdown order that guarantees nothing recorded is lost:
// the server stops first (so no new exchanges arrive), then the recorder drains
// what it already has, and only then does the database close under it.
func (a *app) close() {
	// The sweeper goes first and is waited for: it holds the database's single
	// connection while it vacuums, and closing the store out from under that
	// would turn a clean shutdown into a logged error for no reason.
	close(a.stopSweep)
	<-a.sweepDone

	// Shutdown ignores hijacked WebSockets. End them while the Recorder still
	// accepts the final partial Exchange from any response that was in flight.
	a.proxy.Close()
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
		for _, line := range launchLines(cfg) {
			log.Printf("tokentracer: point your client at it — %s", line)
		}
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
