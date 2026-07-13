package record

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guydelarea/tokentracer/internal/anthropic"
	"github.com/guydelarea/tokentracer/internal/store"
)

// Every test drives the Recorder through its real interface — Record(Exchange)
// against a real temp store. modernc.org/sqlite makes the real store the
// cheapest fake there is.
func newRecorder(t *testing.T) (*Recorder, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st), st
}

// rowsOf drains the recorder and reads back everything it wrote.
func rowsOf(t *testing.T, r *Recorder, st *store.Store) []store.Row {
	t.Helper()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	rows, err := st.Recent(100)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

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

// goodExchange is a normal, complete, successful turn.
func goodExchange(t *testing.T) Exchange {
	t.Helper()
	return Exchange{
		Start:    time.Now(),
		TTFT:     210 * time.Millisecond,
		Duration: 1234 * time.Millisecond,
		Method:   "POST",
		Path:     "/v1/messages",
		Status:   200,
		Streamed: true,
		ReqBody:  fixtureRequest(t),
		RespBody: replaySSE(t),
	}
}

func TestRecordHappyPath(t *testing.T) {
	r, st := newRecorder(t)
	r.Record(goodExchange(t))
	rows := rowsOf(t, r, st)

	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]

	// Request-side facts.
	if row.ModelReq != "claude-sonnet-5" {
		t.Errorf("ModelReq = %q", row.ModelReq)
	}
	if row.SessionID != "210b4cd3-be09-484a-a703-d00f5c6e855f" {
		t.Errorf("SessionID = %q", row.SessionID)
	}
	if row.Label != "hello world!" {
		t.Errorf("Label = %q", row.Label)
	}
	if row.Turns != 7 || row.ToolCount != 119 {
		t.Errorf("Turns = %d, ToolCount = %d, want 7 and 119", row.Turns, row.ToolCount)
	}
	if row.TotalBytes != 308535 || row.ToolsBytes != 232586 || row.SystemBytes != 29910 || row.MessagesBytes != 45445 {
		t.Errorf("byte columns = %d/%d/%d/%d", row.TotalBytes, row.ToolsBytes, row.SystemBytes, row.MessagesBytes)
	}

	// Response-side facts: usage verbatim, merged later-wins.
	if got := deref(row.InputTokens); got != 1234 {
		t.Errorf("InputTokens = %d, want 1234", got)
	}
	if got := deref(row.OutputTokens); got != 213 {
		t.Errorf("OutputTokens = %d, want 213 (message_delta wins over message_start's 1)", got)
	}
	if got := deref(row.CacheReadTokens); got != 71234 {
		t.Errorf("CacheReadTokens = %d, want 71234", got)
	}
	if got := deref(row.CacheW5mTokens); got != 2048 {
		t.Errorf("CacheW5mTokens = %d, want 2048", got)
	}
	if got := deref(row.CacheW1hTokens); got != 29105 {
		t.Errorf("CacheW1hTokens = %d, want 29105", got)
	}

	if row.ModelServed != "claude-sonnet-5" {
		t.Errorf("ModelServed = %q", row.ModelServed)
	}
	if row.StopReason != "tool_use" {
		t.Errorf("StopReason = %q", row.StopReason)
	}
	if row.Op != "tool_use · DesignSync" {
		t.Errorf("Op = %q", row.Op)
	}
	if row.Endpoint != "POST /v1/messages" || row.Status != 200 || !row.Streamed {
		t.Errorf("endpoint/status/streamed = %q/%d/%v", row.Endpoint, row.Status, row.Streamed)
	}
	if row.TtftMs != 210 || row.DurationMs != 1234 {
		t.Errorf("TtftMs = %d, DurationMs = %d", row.TtftMs, row.DurationMs)
	}
	if row.TtftMs >= row.DurationMs {
		t.Errorf("ttft (%d) >= duration (%d)", row.TtftMs, row.DurationMs)
	}
	if row.ErrType != "" {
		t.Errorf("ErrType = %q, want empty on a clean exchange", row.ErrType)
	}
	if row.Aborted {
		t.Error("Aborted = true, want false")
	}

	// The capture: verbatim request, assembled response.
	reqJSON, respJSON, err := st.Capture(row.ID)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if string(reqJSON) != string(fixtureRequest(t)) {
		t.Error("request capture is not the verbatim body")
	}
	var resp anthropic.Response
	if err := json.Unmarshal(respJSON, &resp); err != nil {
		t.Fatalf("response capture is not valid JSON: %v", err)
	}
	if len(resp.Content) != 3 || resp.Content[0].Thinking == "" {
		t.Errorf("assembled response capture lost content: %+v", resp.Content)
	}
	if resp.Usage.Out != 213 {
		t.Errorf("assembled response capture lost usage: %+v", resp.Usage)
	}
}

// Degradation is per side. A request body we cannot read must not cost us the
// usage facts from a response we could.
func TestRecordBadRequestGoodResponse(t *testing.T) {
	r, st := newRecorder(t)
	ex := goodExchange(t)
	ex.ReqBody = []byte(`{"this is": not json`)
	r.Record(ex)
	rows := rowsOf(t, r, st)

	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 — a bad request body must still land", len(rows))
	}
	row := rows[0]

	if row.ErrType != "parse" {
		t.Errorf("ErrType = %q, want parse", row.ErrType)
	}
	if got := row.ErrMsg; len(got) < 9 || got[:9] != "request: " {
		t.Errorf("ErrMsg = %q, want it to name the broken side (\"request: …\")", got)
	}

	// The good side survived, whole.
	if got := deref(row.OutputTokens); got != 213 {
		t.Errorf("OutputTokens = %d — response facts were lost to a request-side failure", got)
	}
	if row.ModelServed != "claude-sonnet-5" || row.StopReason != "tool_use" {
		t.Errorf("response facts lost: served=%q stop=%q", row.ModelServed, row.StopReason)
	}
	// Facts that need no parser survive on every rung.
	if row.TotalBytes != int64(len(ex.ReqBody)) {
		t.Errorf("TotalBytes = %d, want %d", row.TotalBytes, len(ex.ReqBody))
	}
	if row.DurationMs != 1234 {
		t.Errorf("DurationMs = %d", row.DurationMs)
	}

	// And the capture is kept — it is the only way to find out what we got wrong.
	if _, _, err := st.Capture(row.ID); err != nil {
		t.Errorf("capture was dropped on a parse failure: %v", err)
	}
}

func TestRecordGoodRequestBrokenResponse(t *testing.T) {
	r, st := newRecorder(t)
	ex := goodExchange(t)
	ex.RespBody = []byte("this is not an SSE stream at all")
	r.Record(ex)
	rows := rowsOf(t, r, st)

	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]

	if row.ErrType != "parse" {
		t.Errorf("ErrType = %q, want parse", row.ErrType)
	}
	if got := row.ErrMsg; len(got) < 10 || got[:10] != "response: " {
		t.Errorf("ErrMsg = %q, want it to name the broken side (\"response: …\")", got)
	}

	// The request side survived, whole.
	if row.ModelReq != "claude-sonnet-5" || row.ToolCount != 119 {
		t.Errorf("request facts lost: model=%q tools=%d", row.ModelReq, row.ToolCount)
	}
	if row.InputTokens != nil {
		t.Errorf("InputTokens = %d, want NULL — we never learned it, and 0 would be a lie", *row.InputTokens)
	}

	reqJSON, respJSON, err := st.Capture(row.ID)
	if err != nil {
		t.Fatalf("capture dropped: %v", err)
	}
	if len(reqJSON) == 0 {
		t.Error("request capture is empty")
	}
	if string(respJSON) != "this is not an SSE stream at all" {
		t.Errorf("the unparseable response was not kept verbatim: %q", respJSON)
	}
}

// A panic in the parser is a bug in us. It must produce a row, keep the blob
// that reproduces it, and leave the worker alive.
func TestRecordParsePanicKeepsWorkerAlive(t *testing.T) {
	orig := parseRequest
	t.Cleanup(func() { parseRequest = orig })

	// Keyed on the body, not on a flag the worker races us to read.
	parseRequest = func(body []byte) (anthropic.RequestFacts, error) {
		if bytes.Contains(body, []byte("BOOM")) {
			panic("synthetic parser explosion")
		}
		return orig(body)
	}

	r, st := newRecorder(t)

	bad := goodExchange(t)
	bad.ReqBody = []byte(`{"model":"claude-sonnet-5","BOOM":true}`)
	r.Record(bad) // panics inside the worker

	next := goodExchange(t)
	next.TTFT = 99 * time.Millisecond
	r.Record(next) // must still be recorded: the worker survived

	rows := rowsOf(t, r, st)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 — the worker died on the panic", len(rows))
	}

	// Recent() is newest-first: rows[1] is the one that panicked.
	panicked, healthy := rows[1], rows[0]

	if panicked.ErrType != "panic" {
		t.Errorf("ErrType = %q, want panic", panicked.ErrType)
	}
	if !strings.HasPrefix(panicked.ErrMsg, "request: ") {
		t.Errorf("ErrMsg = %q, want it to name the broken side", panicked.ErrMsg)
	}
	// The response side still parsed — the panic was on the request side only.
	if got := deref(panicked.OutputTokens); got != 213 {
		t.Errorf("OutputTokens = %d, want the response facts to survive a request-side panic", got)
	}
	// The panic blob IS the repro case.
	if _, _, err := st.Capture(panicked.ID); err != nil {
		t.Errorf("the panic capture was dropped — the repro case is gone: %v", err)
	}

	if healthy.ErrType != "" || healthy.TtftMs != 99 {
		t.Errorf("the exchange after the panic did not land cleanly: %+v", healthy)
	}
}

// The tee gave up, so the capture is a head and the decode is partial. The row
// says so — and keeps whatever it could still read.
func TestRecordOversize(t *testing.T) {
	r, st := newRecorder(t)
	ex := goodExchange(t)
	full := replaySSE(t)
	ex.RespBody = full[:len(full)*2/3] // the head we kept
	ex.RespTruncated = true
	r.Record(ex)
	rows := rowsOf(t, r, st)

	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]

	if row.ErrType != "oversize" {
		t.Errorf("ErrType = %q, want oversize", row.ErrType)
	}
	// Truncated, but the input-side usage arrived in message_start and survives.
	if got := deref(row.InputTokens); got != 1234 {
		t.Errorf("InputTokens = %d, want the facts that did arrive to survive truncation", got)
	}
	if row.ModelReq != "claude-sonnet-5" {
		t.Errorf("request facts lost: %q", row.ModelReq)
	}
	if _, respJSON, err := st.Capture(row.ID); err != nil || len(respJSON) == 0 {
		t.Errorf("the head capture was dropped: err=%v len=%d", err, len(respJSON))
	}
}

// An esc'd generation is billed spend that no JSONL-reading tool can see. The
// row is otherwise completely normal — the hangup is just one more fact.
func TestRecordClientAborted(t *testing.T) {
	r, st := newRecorder(t)
	ex := goodExchange(t)
	ex.ClientAborted = true
	r.Record(ex)
	rows := rowsOf(t, r, st)

	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]

	if !row.Aborted {
		t.Error("Aborted = false, want true")
	}
	if row.ErrType != "" {
		t.Errorf("ErrType = %q — an abort is not an error, the tokens were still billed", row.ErrType)
	}
	if got := deref(row.OutputTokens); got != 213 {
		t.Errorf("OutputTokens = %d, want the drained usage recorded in full", got)
	}
}

// Upstream wins the err_type column: a fact outranks an interpretation.
func TestRecordUpstreamErrorPrecedence(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantErrType string
	}{
		{"structured error body", 429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, "rate_limit_error"},
		{"malformed error body falls back to the status", 529, `<html>overloaded</html>`, "http_529"},
		{"empty error body", 500, ``, "http_500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, st := newRecorder(t)
			ex := goodExchange(t)
			ex.Status = tc.status
			ex.RespBody = []byte(tc.body)
			ex.Streamed = false
			r.Record(ex)
			rows := rowsOf(t, r, st)

			if len(rows) != 1 {
				t.Fatalf("got %d rows, want 1", len(rows))
			}
			if rows[0].ErrType != tc.wantErrType {
				t.Errorf("ErrType = %q, want %q", rows[0].ErrType, tc.wantErrType)
			}
			if rows[0].Status != tc.status {
				t.Errorf("Status = %d", rows[0].Status)
			}
			// The error body is evidence: keep it.
			if _, respJSON, err := st.Capture(rows[0].ID); err != nil {
				t.Errorf("error-body capture dropped: %v", err)
			} else if string(respJSON) != tc.body {
				t.Errorf("error body not kept verbatim: %q", respJSON)
			}
		})
	}
}

// Upstream error + a request we couldn't parse: the upstream's word still wins
// the column.
func TestRecordUpstreamBeatsRecorderRungs(t *testing.T) {
	r, st := newRecorder(t)
	ex := goodExchange(t)
	ex.Status = 529
	ex.RespBody = []byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	ex.Streamed = false
	ex.ReqBody = []byte(`{"broken": not json`) // would be a 'parse' rung on its own
	ex.RespTruncated = true                    // would be an 'oversize' rung on its own
	r.Record(ex)
	rows := rowsOf(t, r, st)

	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].ErrType != "overloaded_error" {
		t.Errorf("ErrType = %q, want the upstream's own error to win the column", rows[0].ErrType)
	}
}

// Close drains unconditionally. The tail of a session is where the interesting
// requests are.
func TestCloseDrainsEverythingQueued(t *testing.T) {
	r, st := newRecorder(t)

	const n = 50
	for i := range n {
		ex := goodExchange(t)
		ex.TTFT = time.Duration(i+1) * time.Millisecond
		r.Record(ex)
	}
	rows := rowsOf(t, r, st) // Close() then read

	if len(rows) != n {
		t.Fatalf("got %d rows, want all %d queued exchanges flushed", len(rows), n)
	}
	if err := r.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// Ladder bottom: the insert fails, twice. No panic, no crash, no row — and a
// loud complaint on stderr with the facts of what was lost.
func TestRecordSurvivesADeadStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dead.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	r := New(st)
	st.Close() // every insert from here on fails

	r.Record(goodExchange(t)) // must not panic
	if err := r.Close(); err != nil {
		t.Fatalf("Close on a dead store: %v", err)
	}

	// Reopen and confirm the obvious: nothing landed, and the process is fine.
	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	rows, err := st2.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("got %d rows from a store that was closed, want 0", len(rows))
	}
}
