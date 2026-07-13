package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/guydelarea/tokentracer/internal/store"
)

// The whole stack, wired exactly as main() wires it: fixture request → proxy →
// fake upstream replaying the real SSE → recorder → SQLite → dashboard.

func fixtureRequest(t *testing.T) []byte {
	t.Helper()
	f, err := os.Open("../../testdata/anthropic_capture.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var fix struct {
		Request json.RawMessage `json:"request"`
	}
	if err := json.NewDecoder(zr).Decode(&fix); err != nil {
		t.Fatal(err)
	}
	return fix.Request
}

func replaySSE(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/replay.sse")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeUpstream replays the recorded stream event by event, flushing each one the
// way the real API does.
func fakeUpstream(t *testing.T, sse []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/count_tokens") {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"input_tokens":12345}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		for _, event := range bytes.SplitAfter(sse, []byte("\n\n")) {
			if len(event) == 0 {
				continue
			}
			w.Write(event)
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestApp(t *testing.T, upstream string) (*app, *httptest.Server) {
	t.Helper()
	a, err := newApp(config{
		Port:     "8787",
		Upstream: upstream,
		DBPath:   filepath.Join(t.TempDir(), "e2e.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(a.handler)
	t.Cleanup(func() {
		front.Close()
		a.close()
	})
	return a, front
}

func TestEndToEnd(t *testing.T) {
	sse := replaySSE(t)
	up := fakeUpstream(t, sse)
	a, front := newTestApp(t, up.URL)

	// 1. A real turn goes through the proxy.
	resp, err := http.Post(front.URL+"/v1/messages", "application/json", bytes.NewReader(fixtureRequest(t)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}

	// The client's stream is byte-identical: the proxy is transparent.
	if !bytes.Equal(got, sse) {
		t.Fatalf("the client did not receive the upstream stream verbatim (%d bytes vs %d)", len(got), len(sse))
	}

	// 2. count_tokens is proxied, and leaves no trace.
	ct, err := http.Post(front.URL+"/v1/messages/count_tokens", "application/json", strings.NewReader(`{"model":"claude-sonnet-5"}`))
	if err != nil {
		t.Fatal(err)
	}
	ctBody, _ := io.ReadAll(ct.Body)
	ct.Body.Close()
	if !strings.Contains(string(ctBody), "12345") {
		t.Errorf("count_tokens was not proxied through: %s", ctBody)
	}

	// 3. Drain the recorder and read the facts back out of SQLite.
	a.recorder.Close()

	rows, err := a.store.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want exactly 1 — count_tokens must not be recorded", len(rows))
	}
	row := rows[0]

	if row.ModelReq != "claude-sonnet-5" {
		t.Errorf("model_req = %q", row.ModelReq)
	}
	if row.SessionID != "210b4cd3-be09-484a-a703-d00f5c6e855f" {
		t.Errorf("session_id = %q", row.SessionID)
	}
	if row.ToolCount != 119 {
		t.Errorf("tool_count = %d, want 119", row.ToolCount)
	}
	// Usage merged across message_start and message_delta.
	if row.InputTokens == nil || *row.InputTokens != 1234 {
		t.Errorf("input_tokens = %v, want 1234", row.InputTokens)
	}
	if row.OutputTokens == nil || *row.OutputTokens != 213 {
		t.Errorf("output_tokens = %v, want 213", row.OutputTokens)
	}
	if row.CacheReadTokens == nil || *row.CacheReadTokens != 71234 {
		t.Errorf("cache_read_tokens = %v, want 71234", row.CacheReadTokens)
	}
	if row.TtftMs <= 0 {
		t.Errorf("ttft_ms = %d, want > 0", row.TtftMs)
	}
	if row.TtftMs > row.DurationMs {
		t.Errorf("ttft_ms (%d) > duration_ms (%d)", row.TtftMs, row.DurationMs)
	}
	if !row.Streamed {
		t.Error("streamed = false")
	}
	if row.ErrType != "" {
		t.Errorf("err_type = %q (%s), want a clean row", row.ErrType, row.ErrMsg)
	}

	// 4. The capture round-trips: verbatim request in, assembled response out.
	reqJSON, respJSON, err := a.store.Capture(row.ID)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if !bytes.Equal(reqJSON, fixtureRequest(t)) {
		t.Error("the stored request is not the verbatim body the client sent")
	}
	var assembled struct {
		Model   string `json:"model"`
		Content []struct {
			Type     string          `json:"type"`
			Thinking string          `json:"thinking"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respJSON, &assembled); err != nil {
		t.Fatalf("the stored response is not valid JSON: %v", err)
	}
	if len(assembled.Content) != 3 || assembled.Content[0].Thinking == "" {
		t.Errorf("the assembled response lost content: %+v", assembled.Content)
	}

	// 5. The dashboard prices it, at read time, from the facts.
	stats := getStats(t, front)
	if stats.Traced != 1 {
		t.Errorf("traced = %d, want 1", stats.Traced)
	}
	if stats.Cost <= 0 {
		t.Errorf("cost = %v, want a nonzero priced total", stats.Cost)
	}
	if stats.UnpricedReqs != 0 {
		t.Errorf("unpricedReqs = %d — claude-sonnet-5 has no rate in the seed table", stats.UnpricedReqs)
	}
	if len(stats.Recent) != 1 {
		t.Fatalf("recent = %d rows", len(stats.Recent))
	}
	r0 := stats.Recent[0]
	if !r0.Priced {
		t.Error("the recorded exchange came back unpriced")
	}
	if r0.Cost.In+r0.Cost.Read+r0.Cost.Write+r0.Cost.Out <= 0 {
		t.Errorf("row cost quartet is all zero: %+v", r0.Cost)
	}
	if r0.Op != "tool_use · DesignSync" {
		t.Errorf("op = %q", r0.Op)
	}
	if r0.Bytes.Tools != 232586 {
		t.Errorf("row byte splits missing: %+v", r0.Bytes)
	}

	// 6. The inspector's breakdown comes from the capture, folded server-side.
	cap := getCapture(t, front, row.ID)
	if len(cap.Breakdown.Tools) != 119 {
		t.Errorf("breakdown has %d tools, want 119", len(cap.Breakdown.Tools))
	}
	if cap.Breakdown.Tools[0].Name != "Workflow" {
		t.Errorf("largest tool = %q, want Workflow", cap.Breakdown.Tools[0].Name)
	}
	var sum int
	for _, x := range cap.Breakdown.Tools {
		sum += x.Bytes
	}
	if int64(sum) != row.ToolsBytes {
		t.Errorf("Σ breakdown tool bytes (%d) != the fact row's tools_bytes (%d)", sum, row.ToolsBytes)
	}
}

// Claude Code fires parallel requests as a matter of course. Both must land.
func TestConcurrentStreamsBothLand(t *testing.T) {
	up := fakeUpstream(t, replaySSE(t))
	a, front := newTestApp(t, up.URL)

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Post(front.URL+"/v1/messages", "application/json", bytes.NewReader(fixtureRequest(t)))
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()
	}
	wg.Wait()
	a.recorder.Close()

	rows, err := a.store.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	for _, r := range rows {
		if r.OutputTokens == nil || *r.OutputTokens != 213 || r.ErrType != "" {
			t.Errorf("a concurrent stream landed damaged: %+v (err=%q)", r.OutputTokens, r.ErrType)
		}
	}
}

// Ctrl-C mid-session: whatever the recorder had queued is flushed before the DB
// closes. The tail of a session is where the interesting requests are.
func TestShutdownFlushesTheQueue(t *testing.T) {
	up := fakeUpstream(t, replaySSE(t))

	dbPath := filepath.Join(t.TempDir(), "shutdown.db")
	a, err := newApp(config{Port: "8787", Upstream: up.URL, DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(a.handler)

	const n = 20
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Post(front.URL+"/v1/messages", "application/json", bytes.NewReader(fixtureRequest(t)))
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()
	}
	wg.Wait()

	// The shutdown path from main(), in order.
	front.Close() // ~ srv.Shutdown: no new exchanges arrive
	a.close()     // recorder.Close() drains, then store.Close()

	// Reopen the file from scratch: nothing may have been lost on the way down.
	reopened, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	rows, err := reopened.Recent(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != n {
		t.Fatalf("got %d rows after shutdown, want all %d — the tail of the session was lost", len(rows), n)
	}
}

// --- helpers ---

type statsResponse struct {
	Traced       int     `json:"traced"`
	Cost         float64 `json:"cost"`
	UnpricedReqs int     `json:"unpricedReqs"`
	Recent       []struct {
		ID     int64  `json:"id"`
		Op     string `json:"op"`
		Priced bool   `json:"priced"`
		Cost   struct {
			In, Read, Write, Out float64
		} `json:"cost"`
		Bytes struct {
			Total, Tools, System, Messages int64
		} `json:"bytes"`
	} `json:"recent"`
}

func getStats(t *testing.T, front *httptest.Server) statsResponse {
	t.Helper()
	resp, err := http.Get(front.URL + "/api/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/api/stats → %d", resp.StatusCode)
	}
	var v statsResponse
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

type captureResponse struct {
	Request   json.RawMessage `json:"request"`
	Response  json.RawMessage `json:"response"`
	Breakdown struct {
		Tools []struct {
			Name  string `json:"name"`
			Bytes int    `json:"bytes"`
		} `json:"tools"`
	} `json:"breakdown"`
}

func getCapture(t *testing.T, front *httptest.Server, id int64) captureResponse {
	t.Helper()
	resp, err := http.Get(front.URL + "/api/capture?id=" + strconvItoa(id))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/api/capture → %d", resp.StatusCode)
	}
	var v captureResponse
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func strconvItoa(v int64) string { return strconv.FormatInt(v, 10) }
