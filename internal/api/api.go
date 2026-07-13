// Package api is the read side: the dashboard, and the two endpoints it polls.
// Nothing here is ever on the client's proxy path.
package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/guydelarea/tokentracer/internal/anthropic"
	"github.com/guydelarea/tokentracer/internal/billing"
	"github.com/guydelarea/tokentracer/internal/store"
	"github.com/guydelarea/tokentracer/web"
)

// Config is what the dashboard needs to know about the process it is watching.
type Config struct {
	Port     int
	Upstream string
}

// recentLimit is how many rows the request log shows.
const recentLimit = 200

type server struct {
	st    *store.Store
	rates []billing.Rate
	cfg   Config
	now   func() time.Time // swapped in tests
}

// Handler returns the dashboard routes. They hold full request captures, so they
// are loopback-only — enforced here as well as by the listener's bind address,
// because one line of defence for someone's API traffic is not enough.
func Handler(st *store.Store, cfg Config) http.Handler {
	s := &server{st: st, rates: billing.Rates, cfg: cfg, now: time.Now}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /dashboard", s.dashboard)
	mux.HandleFunc("GET /api/stats", s.stats)
	mux.HandleFunc("GET /api/capture", s.capture)
	mux.Handle("GET /web/", http.StripPrefix("/web/", http.FileServer(http.FS(web.FS))))
	return loopbackOnly(mux)
}

// loopbackOnly 404s anything that isn't the machine itself. A 404 rather than a
// 403: a scanner learns nothing about what is here.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) dashboard(w http.ResponseWriter, r *http.Request) {
	page, err := fs.ReadFile(web.FS, "index.html")
	if err != nil {
		http.Error(w, "dashboard missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

// stats is query → fold → encode. Every number is computed in the fold; this
// handler only moves bytes.
func (s *server) stats(w http.ResponseWriter, r *http.Request) {
	now := s.now()

	lifetime, err := s.st.Lifetime()
	if err != nil {
		serverError(w, "lifetime", err)
		return
	}
	window, err := s.st.Window(now.Add(-timelineMin * time.Minute))
	if err != nil {
		serverError(w, "window", err)
		return
	}
	recent, err := s.st.Recent(recentLimit)
	if err != nil {
		serverError(w, "recent", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fold(lifetime, window, recent, s.rates, now, s.cfg)); err != nil {
		log.Printf("tokentracer: encoding stats: %v", err)
	}
}

// captureView is the /api/capture contract: the two blobs, plus the breakdown
// folded server-side so the page never computes a number it is only meant to draw.
type captureView struct {
	Request   json.RawMessage      `json:"request"`
	Response  json.RawMessage      `json:"response,omitempty"`
	Breakdown *anthropic.Breakdown `json:"breakdown,omitempty"`
}

func (s *server) capture(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	reqJSON, respJSON, err := s.st.Capture(id)
	if errors.Is(err, store.ErrNoCapture) {
		// Deleted, or never kept. The inspector degrades to the fact row's byte
		// splits — the breakdown lives and dies with the blob.
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, "capture", err)
		return
	}

	view := captureView{Request: rawOrNull(reqJSON), Response: rawOrNull(respJSON)}
	if bd, err := anthropic.BreakdownRequest(reqJSON); err == nil {
		view.Breakdown = &bd
	}
	// A capture we can't break down still shows its raw bodies: a body we failed
	// to parse is exactly the one worth looking at.

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(view); err != nil {
		log.Printf("tokentracer: encoding capture %d: %v", id, err)
	}
}

// rawOrNull keeps the blob verbatim, and stays valid JSON if it isn't.
func rawOrNull(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	if !json.Valid(b) {
		quoted, err := json.Marshal(string(b))
		if err != nil {
			return nil
		}
		return quoted
	}
	return b
}

func serverError(w http.ResponseWriter, what string, err error) {
	log.Printf("tokentracer: %s query: %v", what, err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
