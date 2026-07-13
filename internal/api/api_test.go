package api

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	if len(v.Recent) != 1 || v.Recent[0].ID != id {
		t.Fatalf("Recent = %+v", v.Recent)
	}
	// The fixture's model is in the seed table, so a real row must price.
	if !v.Recent[0].Priced {
		t.Error("claude-sonnet-5 came back unpriced — the seed rate table has a hole")
	}
	if v.Cost <= 0 {
		t.Errorf("Cost = %v, want > 0", v.Cost)
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
		// Exactly what the README tells a user to do to reclaim disk:
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
		if v.Traced != 1 || len(v.Recent) != 1 {
			t.Errorf("deleting the capture took the fact row with it: traced=%d recent=%d", v.Traced, len(v.Recent))
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
