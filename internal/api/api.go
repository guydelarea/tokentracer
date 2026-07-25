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
	"strings"
	"sync"
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

type server struct {
	st    *store.Store
	rates []billing.Rate
	cfg   Config
	now   func() time.Time // swapped in tests

	// The sessions table needs each session's tool schemas to know which of them
	// were never called, and the schemas live in a capture — a body to gunzip and
	// parse, on a page that polls every two seconds. So they are cached, keyed by
	// the shape of the toolset itself: a session shipping the same tool_count and
	// tools_bytes as last time is shipping the same tools, and there is nothing to
	// re-read. A session's toolset changes about once, when it starts.
	toolsMu sync.Mutex
	tools   map[string]cachedTools
}

// cachedTools is one session's schemas, and the fingerprint of the request they
// were read from.
type cachedTools struct {
	count, bytes int64
	set          toolset
}

// Handler returns the dashboard routes. They hold full request captures, so they
// are loopback-only — enforced here as well as by the listener's bind address,
// because one line of defence for someone's API traffic is not enough.
func Handler(st *store.Store, cfg Config) http.Handler {
	s := &server{st: st, rates: billing.Rates, cfg: cfg, now: time.Now, tools: map[string]cachedTools{}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /dashboard", s.dashboard)
	mux.HandleFunc("GET /api/stats", s.stats)
	mux.HandleFunc("GET /api/trace", s.trace)
	mux.HandleFunc("GET /api/capture", s.capture)
	mux.HandleFunc("POST /api/settings", s.settings)
	mux.HandleFunc("POST /api/purge", s.purge)
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

	view := fold(lifetime, window, s.toolsets(lifetime), s.rates, now, s.cfg)
	view.Storage = s.storageOf()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(view); err != nil {
		log.Printf("tokentracer: encoding stats: %v", err)
	}
}

// trace folds one session. The capture read is the reason this is its own
// endpoint rather than a field on /api/stats: it is paid when someone opens a
// session, not twice a second for every session they aren't looking at.
func (s *server) trace(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	if sid == "" {
		http.NotFound(w, r)
		return
	}

	rows, err := s.st.Session(dbSid(sid))
	if err != nil {
		serverError(w, "session", err)
		return
	}
	if len(rows) == 0 {
		http.NotFound(w, r)
		return
	}

	// The schemas, and what the session's tools have dumped into its context.
	// Both are read from the latest capture that carried them — and both are
	// optional: a capture can be deleted, and everything folded from the fact
	// rows survives that.
	set, gone := s.toolsOf(sid, rows[len(rows)-1])
	var results []anthropic.ResultItem
	if body, err := s.latestBody(rows); err == nil {
		results = anthropic.ResultsInContext(body)
	} else {
		gone = true
	}

	view := foldTrace(sid, rows, set, results, gone, s.rates, s.now())

	// The subagents this session spawned. Fail-soft: a broken child query costs
	// the trace its agents list, never the trace.
	var kids []store.Row
	if kids, err = s.st.AgentRows(dbSid(sid)); err != nil {
		log.Printf("tokentracer: agent rows for %q: %v", sid, err)
	} else if len(kids) > 0 {
		view.Agents = foldAgents(kids, s.rates, s.now())
		for _, a := range view.Agents {
			view.AgentCost += a.Cost
			view.AgentReq += a.Req
			if a.Priced {
				view.Priced = true
			}
		}
	}

	// Every capture contains the full assistant response, including every
	// tool_use block. Read them in order so the dashboard can tell the causal
	// story instead of only reporting the first operation in each response.
	view.Flow = s.flowOf(rows, s.childPrompts(kids, view.Agents))

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(view); err != nil {
		log.Printf("tokentracer: encoding trace %q: %v", sid, err)
	}
}

// childPrompts maps the exact opening prompt of each directly-linked child to
// its summary. parent_sid establishes the relationship; this map identifies
// which Task or Agent call in the parent created that known child.
func (s *server) childPrompts(rows []store.Row, agents []agentRow) map[string][]agentRow {
	bySID := make(map[string]agentRow, len(agents))
	for _, a := range agents {
		bySID[a.Sid] = a
	}
	out := map[string][]agentRow{}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.SessionID] {
			continue
		}
		seen[r.SessionID] = true
		a, ok := bySID[r.SessionID]
		if !ok {
			continue
		}
		body, _, err := s.st.Capture(r.ID)
		if err != nil {
			continue
		}
		facts, err := anthropic.ParseRequest(body)
		if err == nil && strings.TrimSpace(facts.FirstText) != "" {
			key := strings.TrimSpace(facts.FirstText)
			out[key] = append(out[key], a)
		}
	}
	return out
}

func (s *server) flowOf(rows []store.Row, children map[string][]agentRow) []flowTurn {
	out := make([]flowTurn, 0, len(rows))
	called := map[string]string{}
	for _, r := range rows {
		turn := flowTurn{ID: r.ID, Time: time.UnixMilli(r.TsMs).Format(time.RFC3339), Ask: r.Label, Status: r.Status,
			Calls: []flowCall{}, Results: []flowResult{}}
		req, resp, err := s.st.Capture(r.ID)
		if err != nil {
			out = append(out, turn)
			continue
		}
		turn.Captured = true
		for _, result := range anthropic.ToolResults(req) {
			name := called[result.ToolUseID]
			if name == "" {
				name = "earlier tool"
			}
			turn.Results = append(turn.Results, flowResult{ToolUseID: result.ToolUseID, Name: name, Bytes: result.Bytes})
		}
		for _, call := range flowCalls(resp) {
			called[call.ID] = call.Name
			if call.Spawn {
				_, prompt := flowInputFromResponse(resp, call.ID)
				if kids := children[prompt]; len(kids) > 0 {
					call.AgentSid, call.AgentLabel = kids[0].Sid, kids[0].Label
					children[prompt] = kids[1:]
				}
			}
			turn.Calls = append(turn.Calls, call)
		}
		out = append(out, turn)
	}
	return out
}

func flowInputFromResponse(body []byte, id string) (summary, prompt string) {
	var resp anthropic.Response
	if json.Unmarshal(body, &resp) != nil {
		return "", ""
	}
	for _, b := range resp.Content {
		if b.Type == "tool_use" && b.ID == id {
			return flowInput(b.Input)
		}
	}
	return "", ""
}

// latestBody is the newest captured request body in the session — the context as
// it stands now, which is the only one the cut list can be computed against.
func (s *server) latestBody(rows []store.Row) ([]byte, error) {
	for i := len(rows) - 1; i >= 0; i-- {
		body, _, err := s.st.Capture(rows[i].ID)
		if err == nil && len(body) > 0 {
			return body, nil
		}
		if !errors.Is(err, store.ErrNoCapture) {
			return nil, err
		}
	}
	return nil, store.ErrNoCapture
}

// toolsets resolves the schemas for every session in the scan, from cache where
// it can. Called on the stats path, so it must be cheap: the cache turns it into
// one map lookup per session in the steady state.
func (s *server) toolsets(lifetime []store.UsageRow) map[string]toolset {
	// The latest request of each session that shipped any tools at all. A session's
	// toolset is whatever it ships NOW — a tool dropped an hour ago is not one you
	// can still cut.
	type latest struct{ count, bytes int64 }
	seen := map[string]latest{}
	for _, u := range lifetime {
		if u.ToolCount == 0 {
			continue
		}
		sid := u.SessionID
		if sid == "" {
			sid = noSessionID
		}
		seen[sid] = latest{u.ToolCount, u.ToolsBytes}
	}

	out := make(map[string]toolset, len(seen))
	s.toolsMu.Lock()
	defer s.toolsMu.Unlock()
	for sid, l := range seen {
		if c, ok := s.tools[sid]; ok && c.count == l.count && c.bytes == l.bytes {
			out[sid] = c.set // same shape, same tools: nothing to re-read
			continue
		}
		set, _ := s.readTools(sid)
		s.tools[sid] = cachedTools{count: l.count, bytes: l.bytes, set: set}
		out[sid] = set
	}
	return out
}

// toolsOf is toolsets for one session, on the trace path. gone reports that the
// capture the schemas would have come from is no longer there.
func (s *server) toolsOf(sid string, latest store.Row) (toolset, bool) {
	s.toolsMu.Lock()
	defer s.toolsMu.Unlock()
	if c, ok := s.tools[sid]; ok && c.count == latest.ToolCount && c.bytes == latest.ToolsBytes {
		return c.set, false
	}
	set, err := s.readTools(sid)
	if err != nil {
		return set, true
	}
	s.tools[sid] = cachedTools{count: latest.ToolCount, bytes: latest.ToolsBytes, set: set}
	return set, false
}

// readTools reads a session's schemas out of its newest capture that had any.
// Caller holds toolsMu.
func (s *server) readTools(sid string) (toolset, error) {
	id, err := s.st.LatestToolsCapture(dbSid(sid))
	if err != nil {
		return toolset{}, err
	}
	body, _, err := s.st.Capture(id)
	if err != nil {
		return toolset{}, err
	}
	bd, err := anthropic.BreakdownRequest(body)
	if err != nil {
		return toolset{}, err
	}
	return toolset{Items: bd.Tools}, nil
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
