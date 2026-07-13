// Command tokentracer is a local proxy that records what every LLM request
// actually cost, and shows where the money leaks.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
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

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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
		log.Printf("tokentracer: point your client at it — ANTHROPIC_BASE_URL=http://localhost:%s claude", cfg.Port)
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
