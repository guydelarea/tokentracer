package store

import (
	"bytes"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// openTemp opens a store in a fresh temp dir and closes it at test end.
func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tokentracer.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

// sampleRow is a fully-populated fact row: every column non-zero so a
// round-trip that silently drops one is visible.
func sampleRow() Row {
	in, out, read, w5m := int64(1200), int64(340), int64(98000), int64(4096)
	return Row{
		TsMs:            time.Now().UnixMilli(),
		Endpoint:        "POST /v1/messages",
		SessionID:       "210b4cd3-be09-484a-a703-d00f5c6e855f",
		ModelReq:        "claude-sonnet-5",
		ModelServed:     "claude-sonnet-5-20260101",
		Status:          200,
		Streamed:        true,
		Aborted:         true,
		DurationMs:      8123,
		TtftMs:          412,
		StopReason:      "tool_use",
		Op:              "tool_use · Bash — git st…",
		Label:           "fix the flaky store test",
		InputTokens:     &in,
		OutputTokens:    &out,
		CacheReadTokens: &read,
		CacheW5mTokens:  &w5m,
		CacheW1hTokens:  nil, // "unknown" must stay distinct from zero
		Turns:           7,
		ToolCount:       119,
		TotalBytes:      311871,
		ToolsBytes:      236217,
		SystemBytes:     29469,
		MessagesBytes:   45459,
		ErrType:         "oversize",
		ErrMsg:          "response: tee buffer full",
	}
}

func TestOpenAppliesPragmas(t *testing.T) {
	s, _ := openTemp(t)

	// Read the pragmas back on a *fresh* query. modernc.org/sqlite pools
	// connections; a pragma set on the wrong connection is a silent no-op,
	// so asserting on what we set proves nothing — only a read-back does.
	var journal string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if !strings.EqualFold(journal, "wal") {
		t.Errorf("journal_mode = %q, want wal", journal)
	}

	var sync int
	if err := s.db.QueryRow("PRAGMA synchronous").Scan(&sync); err != nil {
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	if sync != 1 { // 1 == NORMAL
		t.Errorf("synchronous = %d, want 1 (NORMAL)", sync)
	}

	var busy int
	if err := s.db.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if busy != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busy)
	}

	var fk int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1 (ON)", fk)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokentracer.db")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	id, err := s1.InsertExchange(sampleRow(), []byte(`{"model":"claude-sonnet-5"}`), []byte(`{"type":"message"}`))
	if err != nil {
		t.Fatalf("InsertExchange: %v", err)
	}
	var v1 int
	if err := s1.db.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&v1); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-Open the same file: migrations must not re-run, and the data must survive.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()

	var v2 int
	if err := s2.db.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&v2); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if v1 != v2 {
		t.Errorf("migration count changed across Open: %d → %d", v1, v2)
	}

	rows, err := s2.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("data did not survive re-Open: got %d rows, want the row id %d", len(rows), id)
	}
}

func TestInsertExchangeRoundTrip(t *testing.T) {
	s, _ := openTemp(t)

	want := sampleRow()
	reqBody := []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`)
	respBody := []byte(`{"type":"message","stop_reason":"tool_use"}`)

	id, err := s.InsertExchange(want, reqBody, respBody)
	if err != nil {
		t.Fatalf("InsertExchange: %v", err)
	}
	if id <= 0 {
		t.Fatalf("InsertExchange returned id %d, want > 0", id)
	}

	rows, err := s.Recent(1)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Recent(1) returned %d rows, want 1", len(rows))
	}
	got := rows[0]

	want.ID = id
	assertRowEqual(t, got, want)

	// The capture landed in the same transaction.
	gotReq, gotResp, err := s.Capture(id)
	if err != nil {
		t.Fatalf("Capture(%d): %v", id, err)
	}
	if !bytes.Equal(gotReq, reqBody) {
		t.Errorf("request capture = %q, want %q", gotReq, reqBody)
	}
	if !bytes.Equal(gotResp, respBody) {
		t.Errorf("response capture = %q, want %q", gotResp, respBody)
	}
}

// assertRowEqual compares every field, so a column dropped by the insert or
// the scan is named rather than hidden in a struct-wide diff.
func assertRowEqual(t *testing.T, got, want Row) {
	t.Helper()

	type field struct {
		name      string
		got, want any
	}
	for _, f := range []field{
		{"ID", got.ID, want.ID},
		{"TsMs", got.TsMs, want.TsMs},
		{"Endpoint", got.Endpoint, want.Endpoint},
		{"SessionID", got.SessionID, want.SessionID},
		{"ModelReq", got.ModelReq, want.ModelReq},
		{"ModelServed", got.ModelServed, want.ModelServed},
		{"Status", got.Status, want.Status},
		{"Streamed", got.Streamed, want.Streamed},
		{"Aborted", got.Aborted, want.Aborted},
		{"DurationMs", got.DurationMs, want.DurationMs},
		{"TtftMs", got.TtftMs, want.TtftMs},
		{"StopReason", got.StopReason, want.StopReason},
		{"Op", got.Op, want.Op},
		{"Label", got.Label, want.Label},
		{"Turns", got.Turns, want.Turns},
		{"ToolCount", got.ToolCount, want.ToolCount},
		{"TotalBytes", got.TotalBytes, want.TotalBytes},
		{"ToolsBytes", got.ToolsBytes, want.ToolsBytes},
		{"SystemBytes", got.SystemBytes, want.SystemBytes},
		{"MessagesBytes", got.MessagesBytes, want.MessagesBytes},
		{"ErrType", got.ErrType, want.ErrType},
		{"ErrMsg", got.ErrMsg, want.ErrMsg},
	} {
		if f.got != f.want {
			t.Errorf("%s = %v, want %v", f.name, f.got, f.want)
		}
	}

	for _, f := range []struct {
		name      string
		got, want *int64
	}{
		{"InputTokens", got.InputTokens, want.InputTokens},
		{"OutputTokens", got.OutputTokens, want.OutputTokens},
		{"CacheReadTokens", got.CacheReadTokens, want.CacheReadTokens},
		{"CacheW5mTokens", got.CacheW5mTokens, want.CacheW5mTokens},
		{"CacheW1hTokens", got.CacheW1hTokens, want.CacheW1hTokens},
	} {
		switch {
		case f.got == nil && f.want == nil:
		case f.got == nil || f.want == nil:
			t.Errorf("%s: got %s, want %s", f.name, ptrStr(f.got), ptrStr(f.want))
		case *f.got != *f.want:
			t.Errorf("%s = %d, want %d", f.name, *f.got, *f.want)
		}
	}
}

func ptrStr(p *int64) string {
	if p == nil {
		return "nil"
	}
	return strconv.FormatInt(*p, 10)
}

// A nil usage column must read back nil, not 0: "we never learned the token
// count" and "the request used zero tokens" are different facts.
func TestUsageNullsStayNull(t *testing.T) {
	s, _ := openTemp(t)

	r := sampleRow()
	r.InputTokens = nil
	r.OutputTokens = nil
	r.CacheReadTokens = nil
	r.CacheW5mTokens = nil
	r.CacheW1hTokens = nil
	r.ErrType = "parse"

	id, err := s.InsertExchange(r, []byte(`not json`), nil)
	if err != nil {
		t.Fatalf("InsertExchange: %v", err)
	}

	rows, err := s.Recent(1)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	got := rows[0]
	if got.ID != id {
		t.Fatalf("Recent returned id %d, want %d", got.ID, id)
	}
	for name, p := range map[string]*int64{
		"InputTokens":     got.InputTokens,
		"OutputTokens":    got.OutputTokens,
		"CacheReadTokens": got.CacheReadTokens,
		"CacheW5mTokens":  got.CacheW5mTokens,
		"CacheW1hTokens":  got.CacheW1hTokens,
	} {
		if p != nil {
			t.Errorf("%s = %d, want nil", name, *p)
		}
	}

	// Lifetime reads NULL usage back as 0 — it is the pricing projection, and
	// an unknown token count prices as nothing.
	life, err := s.Lifetime()
	if err != nil {
		t.Fatalf("Lifetime: %v", err)
	}
	if len(life) != 1 {
		t.Fatalf("Lifetime returned %d rows, want 1", len(life))
	}
	u := life[0]
	if u.In != 0 || u.Out != 0 || u.Read != 0 || u.W5m != 0 || u.W1h != 0 {
		t.Errorf("Lifetime usage = %+v, want all zero for NULL columns", u)
	}
}

// Empty strings map to NULL, so the read side sees "" and SQL sees NULL.
func TestEmptyStringsBecomeNull(t *testing.T) {
	s, _ := openTemp(t)

	r := sampleRow()
	r.SessionID = ""
	r.ModelServed = ""
	r.StopReason = ""
	r.Op = ""
	r.Label = ""
	r.ErrType = ""
	r.ErrMsg = ""

	id, err := s.InsertExchange(r, []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("InsertExchange: %v", err)
	}

	var nulls int
	err = s.db.QueryRow(`
		SELECT (session_id IS NULL) + (model_served IS NULL) + (stop_reason IS NULL) +
		       (op IS NULL) + (label IS NULL) + (err_type IS NULL) + (err_msg IS NULL)
		FROM requests WHERE id = ?`, id).Scan(&nulls)
	if err != nil {
		t.Fatalf("null check: %v", err)
	}
	if nulls != 7 {
		t.Errorf("%d of 7 optional text columns are NULL, want 7", nulls)
	}

	rows, err := s.Recent(1)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if got := rows[0]; got.SessionID != "" || got.ModelServed != "" || got.StopReason != "" ||
		got.Op != "" || got.Label != "" || got.ErrType != "" || got.ErrMsg != "" {
		t.Errorf("NULL text columns did not read back as empty strings: %+v", got)
	}
}

func TestCaptureGzipRoundTrip(t *testing.T) {
	s, _ := openTemp(t)

	// Bodies large and repetitive enough that gzip actually does something —
	// a round-trip that only works on tiny payloads is not a round-trip.
	reqBody := bytes.Repeat([]byte(`{"tools":[{"name":"Bash","description":"run a command"}]}`), 200)
	respBody := bytes.Repeat([]byte(`{"type":"text","text":"hello world"}`), 200)

	id, err := s.InsertExchange(sampleRow(), reqBody, respBody)
	if err != nil {
		t.Fatalf("InsertExchange: %v", err)
	}

	gotReq, gotResp, err := s.Capture(id)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if !bytes.Equal(gotReq, reqBody) {
		t.Errorf("request body did not round-trip (%d bytes out, %d in)", len(gotReq), len(reqBody))
	}
	if !bytes.Equal(gotResp, respBody) {
		t.Errorf("response body did not round-trip (%d bytes out, %d in)", len(gotResp), len(respBody))
	}

	// The blob on disk is actually compressed, not stored verbatim.
	var stored int
	if err := s.db.QueryRow("SELECT length(request_gz) FROM captures WHERE request_id = ?", id).Scan(&stored); err != nil {
		t.Fatalf("length(request_gz): %v", err)
	}
	if stored >= len(reqBody) {
		t.Errorf("request_gz is %d bytes for a %d-byte body — not gzipped?", stored, len(reqBody))
	}
}

func TestCaptureNilResponse(t *testing.T) {
	s, _ := openTemp(t)

	reqBody := []byte(`{"model":"claude-sonnet-5"}`)
	id, err := s.InsertExchange(sampleRow(), reqBody, nil)
	if err != nil {
		t.Fatalf("InsertExchange: %v", err)
	}

	var isNull bool
	if err := s.db.QueryRow("SELECT response_gz IS NULL FROM captures WHERE request_id = ?", id).Scan(&isNull); err != nil {
		t.Fatalf("response_gz null check: %v", err)
	}
	if !isNull {
		t.Error("response_gz is not NULL for a nil respBody")
	}

	gotReq, gotResp, err := s.Capture(id)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if !bytes.Equal(gotReq, reqBody) {
		t.Errorf("request capture = %q, want %q", gotReq, reqBody)
	}
	if gotResp != nil {
		t.Errorf("response capture = %q, want nil", gotResp)
	}
}

// No body to keep → no capture row, but the fact row still lands.
func TestNoBodySkipsCaptureRow(t *testing.T) {
	s, _ := openTemp(t)

	id, err := s.InsertExchange(sampleRow(), nil, nil)
	if err != nil {
		t.Fatalf("InsertExchange: %v", err)
	}

	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM captures WHERE request_id = ?", id).Scan(&n); err != nil {
		t.Fatalf("count captures: %v", err)
	}
	if n != 0 {
		t.Errorf("%d capture rows for a body-less exchange, want 0", n)
	}

	rows, err := s.Recent(1)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != id {
		t.Error("fact row did not land for a body-less exchange")
	}

	if _, _, err := s.Capture(id); !errors.Is(err, ErrNoCapture) {
		t.Errorf("Capture on a captureless row: err = %v, want ErrNoCapture", err)
	}
}

// The foreign key points captures → requests, never the other way: purging
// blobs to reclaim disk must not cost a single fact.
func TestDeleteCapturesLeavesFactRows(t *testing.T) {
	s, _ := openTemp(t)

	for i := 0; i < 3; i++ {
		if _, err := s.InsertExchange(sampleRow(), []byte(`{"model":"x"}`), []byte(`{"type":"message"}`)); err != nil {
			t.Fatalf("InsertExchange %d: %v", i, err)
		}
	}

	if _, err := s.db.Exec("DELETE FROM captures"); err != nil {
		t.Fatalf("DELETE FROM captures: %v", err)
	}

	rows, err := s.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("%d fact rows survive DELETE FROM captures, want 3", len(rows))
	}

	if _, _, err := s.Capture(rows[0].ID); !errors.Is(err, ErrNoCapture) {
		t.Errorf("Capture after purge: err = %v, want ErrNoCapture", err)
	}
}

func TestCaptureUnknownID(t *testing.T) {
	s, _ := openTemp(t)

	if _, _, err := s.Capture(4242); !errors.Is(err, ErrNoCapture) {
		t.Errorf("Capture(4242): err = %v, want ErrNoCapture", err)
	}
}

// The fact row goes in first and the capture second, so a failure on the
// capture is the case a missing rollback would leave half-written: facts
// committed, blob gone. Forced by dropping the captures table out from under
// the insert — cheap, and it fails exactly where it matters.
func TestInsertRollsBackOnError(t *testing.T) {
	s, _ := openTemp(t)

	goodID, err := s.InsertExchange(sampleRow(), []byte(`{"ok":true}`), nil)
	if err != nil {
		t.Fatalf("InsertExchange (good): %v", err)
	}

	if _, err := s.db.Exec("DROP TABLE captures"); err != nil {
		t.Fatalf("DROP TABLE captures: %v", err)
	}

	if _, err := s.InsertExchange(sampleRow(), []byte(`{"doomed":true}`), []byte(`{}`)); err == nil {
		t.Fatal("InsertExchange succeeded with no captures table, want an error")
	}

	// The fact row from the doomed insert must have rolled back with it.
	rows, err := s.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != goodID {
		t.Errorf("%d fact rows survive the failed insert, want only the good row %d", len(rows), goodID)
	}
}

// foreign_keys=ON is not decoration: it is what makes captures.request_id →
// requests(id) a real edge, so a capture can never outlive its facts.
func TestForeignKeyIsEnforced(t *testing.T) {
	s, _ := openTemp(t)

	_, err := s.db.Exec("INSERT INTO captures(request_id, request_gz) VALUES (?, ?)", 999999, []byte("x"))
	if err == nil {
		t.Fatal("inserted a capture with a dangling request_id; foreign_keys is not enforced")
	}
}

// A closed store must fail the insert, not panic and not half-write.
func TestInsertOnClosedStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokentracer.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := s.InsertExchange(sampleRow(), []byte(`{}`), nil); !errors.Is(err, sql.ErrConnDone) && err == nil {
		t.Error("InsertExchange on a closed store returned no error")
	}

	// And nothing landed.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer s2.Close()
	rows, err := s2.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("%d rows landed via a closed store, want 0", len(rows))
	}
}

// --- read side ---

// insertAt writes a minimal row at a given wall time, for the ordering and
// window-boundary tests.
func insertAt(t *testing.T, s *Store, ts time.Time, model string, in int64) int64 {
	t.Helper()
	r := sampleRow()
	r.TsMs = ts.UnixMilli()
	r.ModelReq = model
	r.InputTokens = &in
	id, err := s.InsertExchange(r, []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("InsertExchange: %v", err)
	}
	return id
}

func TestLifetime(t *testing.T) {
	s, _ := openTemp(t)

	base := time.Now().Add(-3 * time.Hour)
	insertAt(t, s, base, "claude-sonnet-5", 100)
	insertAt(t, s, base.Add(time.Hour), "claude-opus-4-8", 200)
	insertAt(t, s, base.Add(2*time.Hour), "claude-haiku-4-5", 300)

	life, err := s.Lifetime()
	if err != nil {
		t.Fatalf("Lifetime: %v", err)
	}
	if len(life) != 3 {
		t.Fatalf("Lifetime returned %d rows, want 3 (all rows, no window)", len(life))
	}

	// Ascending by ts, and every row carries the model it must be priced at.
	wantModels := []string{"claude-sonnet-5", "claude-opus-4-8", "claude-haiku-4-5"}
	wantIn := []int64{100, 200, 300}
	for i, u := range life {
		if u.ModelReq != wantModels[i] {
			t.Errorf("Lifetime[%d].ModelReq = %q, want %q", i, u.ModelReq, wantModels[i])
		}
		if u.In != wantIn[i] {
			t.Errorf("Lifetime[%d].In = %d, want %d", i, u.In, wantIn[i])
		}
		if u.TsMs == 0 {
			t.Errorf("Lifetime[%d].TsMs is zero — the fold prices each row at its own ts", i)
		}
	}
}

func TestWindowIncludesRowExactlyAtSince(t *testing.T) {
	s, _ := openTemp(t)

	since := time.Now().Add(-time.Hour).Truncate(time.Millisecond)

	before := insertAt(t, s, since.Add(-time.Millisecond), "before", 1)
	at := insertAt(t, s, since, "at", 2)
	after := insertAt(t, s, since.Add(time.Minute), "after", 3)

	rows, err := s.Window(since)
	if err != nil {
		t.Fatalf("Window: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("Window returned %d rows, want 2 (the boundary row is included, the earlier one is not)", len(rows))
	}
	if rows[0].ID != at {
		t.Errorf("Window[0].ID = %d, want %d (the row exactly at since)", rows[0].ID, at)
	}
	if rows[1].ID != after {
		t.Errorf("Window[1].ID = %d, want %d", rows[1].ID, after)
	}
	for _, r := range rows {
		if r.ID == before {
			t.Error("Window included the row before since")
		}
	}
	// Ascending by ts.
	if rows[0].TsMs > rows[1].TsMs {
		t.Errorf("Window is not ascending by ts: %d then %d", rows[0].TsMs, rows[1].TsMs)
	}

	// Full rows, not the slim projection.
	if rows[0].Endpoint == "" || rows[0].ToolsBytes == 0 {
		t.Errorf("Window returned a partial row: %+v", rows[0])
	}
}

func TestRecentOrderingAndLimit(t *testing.T) {
	s, _ := openTemp(t)

	base := time.Now().Add(-time.Hour)
	var ids []int64
	for i := 0; i < 5; i++ {
		ids = append(ids, insertAt(t, s, base.Add(time.Duration(i)*time.Minute), "m", int64(i)))
	}

	rows, err := s.Recent(3)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("Recent(3) returned %d rows, want 3", len(rows))
	}
	// Newest first.
	want := []int64{ids[4], ids[3], ids[2]}
	for i, r := range rows {
		if r.ID != want[i] {
			t.Errorf("Recent[%d].ID = %d, want %d (newest first)", i, r.ID, want[i])
		}
	}

	// n larger than the table is not an error.
	all, err := s.Recent(100)
	if err != nil {
		t.Fatalf("Recent(100): %v", err)
	}
	if len(all) != 5 {
		t.Errorf("Recent(100) returned %d rows, want all 5", len(all))
	}
}

func TestReadsOnEmptyStore(t *testing.T) {
	s, _ := openTemp(t)

	life, err := s.Lifetime()
	if err != nil {
		t.Fatalf("Lifetime on empty store: %v", err)
	}
	if len(life) != 0 {
		t.Errorf("Lifetime returned %d rows on an empty store", len(life))
	}

	win, err := s.Window(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("Window on empty store: %v", err)
	}
	if len(win) != 0 {
		t.Errorf("Window returned %d rows on an empty store", len(win))
	}

	recent, err := s.Recent(10)
	if err != nil {
		t.Fatalf("Recent on empty store: %v", err)
	}
	if len(recent) != 0 {
		t.Errorf("Recent returned %d rows on an empty store", len(recent))
	}
}
