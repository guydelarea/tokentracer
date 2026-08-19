package api

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guydelarea/tokentracer/internal/store"
	_ "modernc.org/sqlite"
)

func newServer(t *testing.T) (http.Handler, *store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return Handler(st, testCfg), st, path
}

func get(t *testing.T, h http.Handler, target, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func post(t *testing.T, h http.Handler, target, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", target, nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// seedCapture writes one fact row plus capture, aged by d into the past.
func seedCapture(t *testing.T, st *store.Store, d time.Duration) int64 {
	t.Helper()
	id, err := st.InsertExchange(store.Row{
		TsMs:     time.Now().Add(-d).UnixMilli(),
		Endpoint: "POST /v1/messages",
		ModelReq: "claude-sonnet-5",
		Status:   200,
	}, []byte(`{"model":"claude-sonnet-5"}`), []byte(`{"type":"message"}`))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Setting a window is not a promise about the next hour — it deletes what has
// already aged out, immediately, or the control looks broken.
func TestSettingsPrunesOnChange(t *testing.T) {
	h, st, _ := newServer(t)

	old := seedCapture(t, st, 8*24*time.Hour)
	fresh := seedCapture(t, st, time.Hour)

	if code := post(t, h, "/api/settings?retention=7d", "127.0.0.1:1234").Code; code != http.StatusNoContent {
		t.Fatalf("POST /api/settings?retention=7d → %d, want 204", code)
	}

	if _, _, err := st.Capture(old); err == nil {
		t.Error("the 8-day-old capture survived a 7-day window")
	}
	if _, _, err := st.Capture(fresh); err != nil {
		t.Errorf("the 1-hour-old capture was pruned by a 7-day window: %v", err)
	}

	// Facts are never a retention concern.
	rows, err := st.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Errorf("%d fact rows survive the prune, want 2", len(rows))
	}

	// And the choice comes back on the poll the page already makes.
	var v statsView
	if err := json.Unmarshal(get(t, h, "/api/stats", "127.0.0.1:1234").Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Storage.Retention != "7d" {
		t.Errorf("stats retention = %q, want %q", v.Storage.Retention, "7d")
	}
	if v.Storage.CaptureBytes <= 0 {
		t.Errorf("stats captureBytes = %d, want the size of the surviving capture", v.Storage.CaptureBytes)
	}
}

// A window we cannot interpret must never be read as permission to delete.
func TestSettingsRejectsUnknownWindow(t *testing.T) {
	h, st, _ := newServer(t)
	id := seedCapture(t, st, 400*24*time.Hour) // ancient: any real window would take it

	for _, q := range []string{"retention=forever", "retention=", "retention=1s", ""} {
		if code := post(t, h, "/api/settings?"+q, "127.0.0.1:1234").Code; code != http.StatusBadRequest {
			t.Errorf("POST /api/settings?%s → %d, want 400", q, code)
		}
	}
	if _, _, err := st.Capture(id); err != nil {
		t.Errorf("a rejected setting still pruned: %v", err)
	}

	var v statsView
	if err := json.Unmarshal(get(t, h, "/api/stats", "127.0.0.1:1234").Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Storage.Retention != "off" {
		t.Errorf("retention = %q after rejected writes, want %q", v.Storage.Retention, "off")
	}
}

func TestPurgeDropsEveryCaptureAndKeepsFacts(t *testing.T) {
	h, st, _ := newServer(t)

	ids := []int64{seedCapture(t, st, 30*24*time.Hour), seedCapture(t, st, time.Minute), seedCapture(t, st, 0)}

	if code := post(t, h, "/api/purge", "127.0.0.1:1234").Code; code != http.StatusNoContent {
		t.Fatalf("POST /api/purge → %d, want 204", code)
	}

	for _, id := range ids {
		if _, _, err := st.Capture(id); err == nil {
			t.Errorf("capture %d survived a purge", id)
		}
	}
	rows, err := st.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(ids) {
		t.Errorf("%d fact rows survive a purge, want %d", len(rows), len(ids))
	}
}

// The write routes delete data, so the loopback rule matters more on them than
// anywhere else.
func TestWriteRoutesAreLoopbackOnly(t *testing.T) {
	h, st, _ := newServer(t)
	id := seedCapture(t, st, 30*24*time.Hour)

	for _, target := range []string{"/api/settings?retention=24h", "/api/purge"} {
		if code := post(t, h, target, "8.8.8.8:443").Code; code != http.StatusNotFound {
			t.Errorf("POST %s from off-machine → %d, want 404", target, code)
		}
	}
	if _, _, err := st.Capture(id); err != nil {
		t.Errorf("an off-machine request deleted a capture: %v", err)
	}
}

// The dashboard holds full request captures — including everything the user
// ever sent to the model. It answers to this machine and nobody else.
func TestLoopbackOnly(t *testing.T) {
	h, _, _ := newServer(t)

	allowed := []string{"127.0.0.1:54321", "[::1]:54321"}
	blocked := []string{"192.168.1.50:54321", "10.0.0.7:1234", "8.8.8.8:443", "[2001:db8::1]:443"}

	for _, target := range []string{"/dashboard", "/api/stats", "/api/capture?id=1", "/web/app.js"} {
		for _, addr := range allowed {
			if code := get(t, h, target, addr).Code; code == http.StatusNotFound && target != "/api/capture?id=1" {
				t.Errorf("%s from loopback %s → 404, want it served", target, addr)
			}
		}
		for _, addr := range blocked {
			if code := get(t, h, target, addr).Code; code != http.StatusNotFound {
				t.Errorf("%s from NON-loopback %s → %d, want 404", target, addr, code)
			}
		}
	}
}

func TestStatsSmoke(t *testing.T) {
	h, st, _ := newServer(t)

	// Empty database: the very first thing a user sees must still be valid JSON.
	rec := get(t, h, "/api/stats", "127.0.0.1:1234")
	if rec.Code != 200 {
		t.Fatalf("empty /api/stats → %d", rec.Code)
	}
	var empty statsView
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil {
		t.Fatalf("empty /api/stats is not valid JSON: %v", err)
	}
	if empty.Traced != 0 || len(empty.Overview.Timeline) != 60 {
		t.Errorf("empty stats = %+v", empty)
	}

	// Now with a real row in it.
	in, out := int64(1000), int64(500)
	id, err := st.InsertExchange(store.Row{
		TsMs:         time.Now().UnixMilli(),
		Endpoint:     "POST /v1/messages",
		ModelReq:     "claude-sonnet-5",
		SessionID:    "sess-9",
		Status:       200,
		Streamed:     true,
		DurationMs:   1200,
		TtftMs:       300,
		InputTokens:  &in,
		OutputTokens: &out,
	}, []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`), []byte(`{"content":[]}`))
	if err != nil {
		t.Fatal(err)
	}

	rec = get(t, h, "/api/stats", "127.0.0.1:1234")
	if rec.Code != 200 {
		t.Fatalf("/api/stats → %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	var v statsView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if v.Traced != 1 {
		t.Errorf("Traced = %d, want 1", v.Traced)
	}
	if len(v.Sessions) != 1 {
		t.Fatalf("Sessions = %+v", v.Sessions)
	}
	// The fixture's model is in the seed table, so a real row must price.
	if !v.Sessions[0].Priced {
		t.Error("claude-sonnet-5 came back unpriced — the seed rate table has a hole")
	}
	if v.Cost <= 0 {
		t.Errorf("Cost = %v, want > 0", v.Cost)
	}

	// The request itself lives one level down, in its session's trace.
	rec = get(t, h, "/api/trace?sid="+url.QueryEscape(v.Sessions[0].ID), "127.0.0.1:1234")
	if rec.Code != 200 {
		t.Fatalf("/api/trace → %d", rec.Code)
	}
	var tr traceView
	if err := json.Unmarshal(rec.Body.Bytes(), &tr); err != nil {
		t.Fatalf("trace is not valid JSON: %v", err)
	}
	if len(tr.Rows) != 1 || tr.Rows[0].ID != id {
		t.Fatalf("trace rows = %+v", tr.Rows)
	}
	if !tr.Rows[0].Priced {
		t.Error("the traced row came back unpriced")
	}
}

// A session nobody recorded has no trace, and asking for one is not an error the
// page has to handle — it is a 404.
func TestTraceUnknownSession(t *testing.T) {
	h, _, _ := newServer(t)

	if code := get(t, h, "/api/trace?sid=nope", "127.0.0.1:1234").Code; code != 404 {
		t.Errorf("unknown session → %d, want 404", code)
	}
	if code := get(t, h, "/api/trace", "127.0.0.1:1234").Code; code != 404 {
		t.Errorf("no sid → %d, want 404", code)
	}
}

func TestTraceFlowConnectsToolsResultsAndSubagents(t *testing.T) {
	h, st, _ := newServer(t)
	base := time.Now().Add(-time.Minute)
	rootReq := []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"run the test suite"}]}`)
	rootResp := []byte(`{"content":[{"type":"tool_use","id":"call-bash","name":"Bash","input":{"command":"go test ./..."}},{"type":"tool_use","id":"call-agent","name":"Task","input":{"prompt":"inspect trace renderer"}}]}`)
	_, err := st.InsertExchange(store.Row{
		TsMs: base.UnixMilli(), Endpoint: "POST /v1/messages", ModelReq: "claude-sonnet-5",
		SessionID: "root", Label: "run the test suite", Status: 200,
	}, rootReq, rootResp)
	if err != nil {
		t.Fatal(err)
	}

	// The child has an authoritative parent_sid and the same opening prompt as
	// the Task call above. That lets the flow attach it to the right call.
	_, err = st.InsertExchange(store.Row{
		TsMs: base.Add(time.Second).UnixMilli(), Endpoint: "POST /v1/messages", ModelReq: "claude-sonnet-5",
		SessionID: "child", ParentSid: "root", Label: "inspect trace renderer", Status: 200,
	}, []byte(`{"messages":[{"role":"user","content":"inspect trace renderer"}]}`), []byte(`{"content":[]}`))
	if err != nil {
		t.Fatal(err)
	}

	_, err = st.InsertExchange(store.Row{
		TsMs: base.Add(2 * time.Second).UnixMilli(), Endpoint: "POST /v1/messages", ModelReq: "claude-sonnet-5",
		SessionID: "root", Label: "run the test suite", Status: 200,
	}, []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-bash","content":"all tests passed"}]}]}`), []byte(`{"content":[{"type":"text","text":"done"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.InsertExchange(store.Row{
		TsMs: base.Add(3 * time.Second).UnixMilli(), Endpoint: "POST /v1/messages", ModelReq: "claude-sonnet-5",
		// The fact label still names the session opener. The flow must instead
		// show the newest human message from this request's history.
		SessionID: "root", Label: "run the test suite", Status: 200,
	}, []byte(`{"messages":[{"role":"user","content":"run the test suite"},{"role":"assistant","content":"done"},{"role":"user","content":"fine how are you"}]}`), []byte(`{"content":[{"type":"text","text":"great"}]}`))
	if err != nil {
		t.Fatal(err)
	}

	var compact traceView
	rec := get(t, h, "/api/trace?sid=root", "127.0.0.1:1234")
	if err := json.Unmarshal(rec.Body.Bytes(), &compact); err != nil {
		t.Fatal(err)
	}
	if len(compact.Flow) != 0 {
		t.Fatalf("ordinary trace poll parsed flow = %+v", compact.Flow)
	}

	var got traceView
	rec = get(t, h, "/api/trace?sid=root&flow=1", "127.0.0.1:1234")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Flow) != 3 {
		t.Fatalf("flow turns = %d, want 3", len(got.Flow))
	}
	first := got.Flow[0]
	if !first.Captured || first.Ask != "run the test suite" || len(first.Calls) != 2 {
		t.Fatalf("first flow turn = %+v", first)
	}
	if first.Calls[0].Name != "Bash" || first.Calls[0].Summary != "go test ./..." {
		t.Errorf("bash call = %+v", first.Calls[0])
	}
	if first.Calls[1].Name != "Task" || !first.Calls[1].Spawn || first.Calls[1].AgentSid != "child" {
		t.Errorf("subagent call = %+v", first.Calls[1])
	}
	if len(got.Flow[1].Results) != 1 || got.Flow[1].Results[0].Name != "Bash" || got.Flow[1].Results[0].Bytes == 0 {
		t.Errorf("result hand-off = %+v", got.Flow[1].Results)
	}
	if got.Flow[2].Ask != "fine how are you" {
		t.Errorf("latest graph prompt = %q, want the latest user text", got.Flow[2].Ask)
	}
}

func TestCapture(t *testing.T) {
	h, st, dbPath := newServer(t)

	reqBody := []byte(`{"model":"claude-sonnet-5","tools":[{"name":"Bash","description":"run a command"}],"system":"be brief","messages":[{"role":"user","content":"hi"}]}`)
	respBody := []byte(`{"model":"claude-sonnet-5","stop_reason":"end_turn","content":[{"type":"text","text":"hello"}]}`)
	id, err := st.InsertExchange(store.Row{
		TsMs: time.Now().UnixMilli(), Endpoint: "POST /v1/messages", ModelReq: "claude-sonnet-5", Status: 200,
	}, reqBody, respBody)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("returns request, response and a server-folded breakdown", func(t *testing.T) {
		rec := get(t, h, "/api/capture?id="+itoa(id), "127.0.0.1:1234")
		if rec.Code != 200 {
			t.Fatalf("→ %d: %s", rec.Code, rec.Body)
		}
		var v captureView
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatalf("not valid JSON: %v", err)
		}
		if string(v.Request) != string(reqBody) {
			t.Errorf("request is not the verbatim body: %s", v.Request)
		}
		if v.Breakdown == nil {
			t.Fatal("breakdown is missing — the inspector's headline tab has nothing to draw")
		}
		if len(v.Breakdown.Tools) != 1 || v.Breakdown.Tools[0].Name != "Bash" {
			t.Errorf("breakdown tools = %+v", v.Breakdown.Tools)
		}
		if len(v.Breakdown.Messages) != 1 || v.Breakdown.Messages[0].Role != "user" {
			t.Errorf("breakdown messages = %+v", v.Breakdown.Messages)
		}
	})

	t.Run("unknown id → 404", func(t *testing.T) {
		if code := get(t, h, "/api/capture?id=99999", "127.0.0.1:1234").Code; code != 404 {
			t.Errorf("→ %d, want 404", code)
		}
	})

	t.Run("missing or junk id → 404", func(t *testing.T) {
		for _, target := range []string{"/api/capture", "/api/capture?id=", "/api/capture?id=abc"} {
			if code := get(t, h, target, "127.0.0.1:1234").Code; code != 404 {
				t.Errorf("%s → %d, want 404", target, code)
			}
		}
	})

	t.Run("deleted capture → 404, and the fact row survives", func(t *testing.T) {
		// Exactly what docs/database.md tells a user to do to reclaim disk:
		//   sqlite3 tokentracer.db 'delete from captures'
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("DELETE FROM captures"); err != nil {
			t.Fatal(err)
		}
		db.Close()
		if code := get(t, h, "/api/capture?id="+itoa(id), "127.0.0.1:1234").Code; code != 404 {
			t.Errorf("deleted capture → %d, want 404", code)
		}

		// The whole point of the fact/capture split: the row is still there.
		rec := get(t, h, "/api/stats", "127.0.0.1:1234")
		var v statsView
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if v.Traced != 1 || len(v.Sessions) != 1 {
			t.Errorf("deleting the capture took the fact row with it: traced=%d sessions=%d", v.Traced, len(v.Sessions))
		}

		// And the trace still folds: the schemas and the tool-result rows are gone
		// with the body, and it says so, but every number derived from the fact row
		// survives.
		rec = get(t, h, "/api/trace?sid="+url.QueryEscape(v.Sessions[0].ID), "127.0.0.1:1234")
		var tr traceView
		if err := json.Unmarshal(rec.Body.Bytes(), &tr); err != nil {
			t.Fatal(err)
		}
		if len(tr.Rows) != 1 {
			t.Errorf("trace lost its row when the capture went: %+v", tr.Rows)
		}
		if !tr.CaptureGone {
			t.Error("the trace did not admit that the capture it reads schemas from is gone")
		}
	})
}

func TestDashboardAndAssetsAreServed(t *testing.T) {
	h, _, _ := newServer(t)

	rec := get(t, h, "/dashboard", "127.0.0.1:1234")
	if rec.Code != 200 {
		t.Fatalf("/dashboard → %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<html") && !strings.Contains(body, "<!DOCTYPE") && !strings.Contains(body, "<div") {
		t.Errorf("/dashboard does not look like a page: %.120s", body)
	}
	if !strings.Contains(body, "/web/app.js") {
		t.Error("/dashboard does not load /web/app.js")
	}

	for _, asset := range []string{"/web/app.js", "/web/app.css", "/web/logo.svg"} {
		rec := get(t, h, asset, "127.0.0.1:1234")
		if rec.Code != 200 {
			t.Errorf("%s → %d", asset, rec.Code)
			continue
		}
		if b, _ := io.ReadAll(rec.Body); len(b) == 0 {
			t.Errorf("%s is empty", asset)
		}
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
