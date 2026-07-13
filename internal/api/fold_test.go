package api

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/guydelarea/tokentracer/internal/billing"
	"github.com/guydelarea/tokentracer/internal/store"
)

// A fixed clock makes every bucket boundary deterministic.
var now = time.Date(2026, 7, 13, 12, 0, 30, 0, time.UTC)

// A hand-built rate table: $3/1M in, $15/1M out. Cache multipliers are billing's
// (read 0.1, write-5m 1.25, write-1h 2.0), so 1M cache reads cost $0.30.
var testRates = []billing.Rate{
	{Key: "test-model", InPerM: 3, OutPerM: 15},
}

var testCfg = Config{Port: 8787, Upstream: "https://api.anthropic.com"}

func ptr(v int64) *int64 { return &v }

// row builds a fact row `minsAgo` minutes before `now`.
func row(id int64, minsAgo int, model string, in, read, w5m, w1h, out int64) store.Row {
	return store.Row{
		ID:              id,
		TsMs:            now.Add(-time.Duration(minsAgo) * time.Minute).UnixMilli(),
		Endpoint:        "POST /v1/messages",
		ModelReq:        model,
		Status:          200,
		Streamed:        true,
		DurationMs:      1000,
		TtftMs:          200,
		InputTokens:     ptr(in),
		CacheReadTokens: ptr(read),
		CacheW5mTokens:  ptr(w5m),
		CacheW1hTokens:  ptr(w1h),
		OutputTokens:    ptr(out),
	}
}

func usage(minsAgo int, model string, in, read, w5m, w1h, out int64) store.UsageRow {
	return store.UsageRow{
		TsMs:     now.Add(-time.Duration(minsAgo) * time.Minute).UnixMilli(),
		ModelReq: model,
		In:       in,
		Read:     read,
		W5m:      w5m,
		W1h:      w1h,
		Out:      out,
	}
}

func close(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// One million of each token class, priced against the test table.
func TestFoldLifetimeCost(t *testing.T) {
	life := []store.UsageRow{
		usage(5, "test-model", 1_000_000, 0, 0, 0, 0), // $3.00
		usage(5, "test-model", 0, 1_000_000, 0, 0, 0), // $0.30 (read = 0.1x)
		usage(5, "test-model", 0, 0, 1_000_000, 0, 0), // $3.75 (5m write = 1.25x)
		usage(5, "test-model", 0, 0, 0, 1_000_000, 0), // $6.00 (1h write = 2.0x)
		usage(5, "test-model", 0, 0, 0, 0, 1_000_000), // $15.00
	}
	v := fold(life, nil, nil, testRates, now, testCfg)

	if v.Traced != 5 {
		t.Errorf("Traced = %d, want 5", v.Traced)
	}
	close(t, "Cost", v.Cost, 3+0.30+3.75+6+15)
	if v.UnpricedReqs != 0 {
		t.Errorf("UnpricedReqs = %d, want 0", v.UnpricedReqs)
	}
	if v.Port != 8787 || v.Upstream != "https://api.anthropic.com" {
		t.Errorf("config not carried through: %+v", v)
	}
}

// The footgun this project exists to fix: a model with no rate must never be
// quietly worth $0.
func TestFoldUnpricedModelIsSurfacedNotZeroed(t *testing.T) {
	life := []store.UsageRow{
		usage(5, "test-model", 1_000_000, 0, 0, 0, 0), // $3
		usage(5, "some-unreleased-model", 9_000_000, 0, 0, 0, 5_000_000),
	}
	v := fold(life, nil, nil, testRates, now, testCfg)

	if v.UnpricedReqs != 1 {
		t.Errorf("UnpricedReqs = %d, want 1", v.UnpricedReqs)
	}
	close(t, "Cost", v.Cost, 3) // the unpriced row contributes nothing, and says so
}

// Regression, found by running the real binary: a request can ASK for a model we
// have a rate for and be SERVED by one we don't. Pricing the requested model
// while displaying the served one produced a row labelled "claude-imaginary-9"
// with a confident $0.14 next to it — the exact silent-mispricing footgun this
// project exists to kill. The bill follows the model that actually served it.
func TestFoldPricesTheModelThatServedTheRequest(t *testing.T) {
	t.Run("unknown served model is unpriced even when the requested model has a rate", func(t *testing.T) {
		life := []store.UsageRow{{
			TsMs: now.Add(-time.Minute).UnixMilli(),
			// asked for a model we price...
			ModelReq: "test-model",
			// ...and got one we have never heard of.
			ModelServed: "imaginary-9",
			In:          1_000_000,
		}}
		r := row(1, 1, "test-model", 1_000_000, 0, 0, 0, 0)
		r.ModelServed = "imaginary-9"

		v := fold(life, []store.Row{r}, []store.Row{r}, testRates, now, testCfg)

		if v.UnpricedReqs != 1 {
			t.Errorf("UnpricedReqs = %d, want 1", v.UnpricedReqs)
		}
		close(t, "Cost", v.Cost, 0)
		if v.Recent[0].Priced {
			t.Error("the row came back priced — a model we have no rate for was billed at another model's price")
		}
		close(t, "row cost", v.Recent[0].Cost.In, 0)
		// The name on screen and the number next to it must describe the same model.
		if v.Recent[0].Model != "imaginary-9" {
			t.Errorf("Model = %q, want the model that served it", v.Recent[0].Model)
		}
		close(t, "BurnNow", v.Overview.BurnNow, 0)
	})

	t.Run("falls back to the requested model when the response never said", func(t *testing.T) {
		life := []store.UsageRow{{TsMs: now.Add(-time.Minute).UnixMilli(), ModelReq: "test-model", In: 1_000_000}}
		v := fold(life, nil, nil, testRates, now, testCfg)

		if v.UnpricedReqs != 0 {
			t.Errorf("UnpricedReqs = %d, want 0 — an empty model_served must not make a row unpriced", v.UnpricedReqs)
		}
		close(t, "Cost", v.Cost, 3)
	})
}

func TestFoldRecentRowPricingAndFlags(t *testing.T) {
	rows := []store.Row{row(7, 1, "test-model", 1_000_000, 1_000_000, 0, 0, 1_000_000)}
	rows[0].SessionID = "sess-1"
	rows[0].Op = "tool_use · Bash"
	rows[0].StopReason = "tool_use"
	rows[0].ModelServed = "test-model-served" // matches the "test-model" rate key by substring
	rows[0].TotalBytes, rows[0].ToolsBytes, rows[0].SystemBytes, rows[0].MessagesBytes = 1000, 750, 150, 100

	v := fold(nil, nil, rows, testRates, now, testCfg)
	if len(v.Recent) != 1 {
		t.Fatalf("len(Recent) = %d", len(v.Recent))
	}
	r := v.Recent[0]

	if r.ID != 7 || r.Sid != "sess-1" || r.Op != "tool_use · Bash" || r.Stop != "tool_use" {
		t.Errorf("row fields = %+v", r)
	}
	if r.Model != "test-model-served" {
		t.Errorf("Model = %q, want the model that actually served it", r.Model)
	}
	if !r.Priced {
		t.Error("Priced = false, want true")
	}
	close(t, "cost.in", r.Cost.In, 3)
	close(t, "cost.read", r.Cost.Read, 0.30)
	close(t, "cost.out", r.Cost.Out, 15)
	if r.Tok.In != 1_000_000 || r.Tok.Read != 1_000_000 || r.Tok.Out != 1_000_000 {
		t.Errorf("tok = %+v", r.Tok)
	}
	if r.Bytes.Total != 1000 || r.Bytes.Tools != 750 {
		t.Errorf("bytes = %+v", r.Bytes)
	}
	if _, err := time.Parse(time.RFC3339, r.Time); err != nil {
		t.Errorf("Time = %q is not RFC3339: %v", r.Time, err)
	}
}

// An unparsed row carries NULL usage. It still prices (to zero) and still shows.
func TestFoldHandlesNullUsage(t *testing.T) {
	r := store.Row{ID: 1, TsMs: now.UnixMilli(), ModelReq: "test-model", Status: 200, ErrType: "parse"}
	v := fold(nil, []store.Row{r}, []store.Row{r}, testRates, now, testCfg)

	if len(v.Recent) != 1 {
		t.Fatalf("len(Recent) = %d", len(v.Recent))
	}
	if v.Recent[0].Tok != (tokens{}) {
		t.Errorf("tok = %+v, want all zeros for a row with no usage", v.Recent[0].Tok)
	}
	if v.Recent[0].ErrType != "parse" {
		t.Errorf("ErrType = %q", v.Recent[0].ErrType)
	}
}

func TestFoldBurnAndWindow(t *testing.T) {
	// Two requests inside the 10-minute window, one outside it.
	window := []store.Row{
		row(1, 30, "test-model", 1_000_000, 0, 0, 0, 0), // $3, outside the window
		row(2, 5, "test-model", 1_000_000, 0, 0, 0, 0),  // $3, inside
		row(3, 1, "test-model", 1_000_000, 0, 0, 0, 0),  // $3, inside
	}
	life := []store.UsageRow{
		usage(30, "test-model", 1_000_000, 0, 0, 0, 0),
		usage(5, "test-model", 1_000_000, 0, 0, 0, 0),
		usage(1, "test-model", 1_000_000, 0, 0, 0, 0),
	}
	v := fold(life, window, nil, testRates, now, testCfg)
	o := v.Overview

	if o.WinReqs != 2 {
		t.Errorf("WinReqs = %d, want 2 (the 30-minute-old row is outside the 10-minute window)", o.WinReqs)
	}
	// $6 in 10 minutes → $36/hr.
	close(t, "BurnNow", o.BurnNow, 36)
	if o.ReqHr != 12 {
		t.Errorf("ReqHr = %d, want 12 (2 requests in 10 min)", o.ReqHr)
	}
	close(t, "AvgReq", o.AvgReq, 3)
	if o.WindowMin != 10 {
		t.Errorf("WindowMin = %d, want 10", o.WindowMin)
	}

	// Lifetime burn spans from the oldest row (30 min ago) to now: $9 in 0.5h.
	close(t, "BurnAvg", o.BurnAvg, 18)
}

// A brand-new database has seen a handful of requests over a few seconds.
// Dividing by those seconds says "you are burning $191/hr", which is true and
// worthless. The average is floored at the window until real time has passed.
func TestFoldBurnAvgIsFlooredOnAFreshDatabase(t *testing.T) {
	var life []store.UsageRow
	for range 9 {
		// Nine requests, all within the same second — $3 each.
		life = append(life, store.UsageRow{
			TsMs:     now.Add(-time.Second).UnixMilli(),
			ModelReq: "test-model",
			In:       1_000_000,
		})
	}
	o := fold(life, nil, nil, testRates, now, testCfg).Overview

	// $27 over the 10-minute floor → $162/hr, not $27/second-extrapolated.
	close(t, "BurnAvg", o.BurnAvg, 27/(10.0/60))
	if o.BurnAvg > 1000 {
		t.Errorf("BurnAvg = %v — a few seconds of history extrapolated to an absurd hourly rate", o.BurnAvg)
	}
}

// Token totals are facts, not interpretations — they are the one number on the
// dashboard that survives the rate table being wrong.
func TestFoldTokenTotals(t *testing.T) {
	life := []store.UsageRow{
		usage(30, "test-model", 100, 200, 300, 400, 50), // outside the window
		usage(5, "test-model", 10, 20, 30, 40, 5),       // inside
		usage(1, "test-model", 1, 2, 3, 4, 1),           // inside
	}
	window := []store.Row{
		row(1, 30, "test-model", 100, 200, 300, 400, 50),
		row(2, 5, "test-model", 10, 20, 30, 40, 5),
		row(3, 1, "test-model", 1, 2, 3, 4, 1),
	}
	v := fold(life, window, nil, testRates, now, testCfg)

	// Lifetime counts every row.
	wantLife := tokens{In: 111, Read: 222, Write: 333 + 444, Out: 56}
	if v.Tokens != wantLife {
		t.Errorf("lifetime tokens = %+v, want %+v", v.Tokens, wantLife)
	}

	// The window counts only what is in it — the 30-minute-old row is not.
	wantWin := tokens{In: 11, Read: 22, Write: 33 + 44, Out: 6}
	if v.Overview.Tokens != wantWin {
		t.Errorf("window tokens = %+v, want %+v", v.Overview.Tokens, wantWin)
	}
}

// A row we could not price still counted real tokens. The token tiles must not
// inherit the rate table's ignorance.
func TestFoldCountsTokensOfUnpricedRows(t *testing.T) {
	life := []store.UsageRow{usage(1, "some-unreleased-model", 1000, 0, 0, 0, 250)}
	v := fold(life, nil, nil, testRates, now, testCfg)

	if v.UnpricedReqs != 1 || v.Cost != 0 {
		t.Fatalf("expected an unpriced row: unpriced=%d cost=%v", v.UnpricedReqs, v.Cost)
	}
	if v.Tokens.In != 1000 || v.Tokens.Out != 250 {
		t.Errorf("tokens = %+v — an unpriced row still sent and received real tokens", v.Tokens)
	}
}

func TestFoldCacheHitRates(t *testing.T) {
	// 750k cache reads out of 1M total input → 75%.
	life := []store.UsageRow{usage(5, "test-model", 150_000, 750_000, 50_000, 50_000, 1000)}
	window := []store.Row{row(1, 5, "test-model", 150_000, 750_000, 50_000, 50_000, 1000)}

	o := fold(life, window, nil, testRates, now, testCfg).Overview
	close(t, "HitAvg", o.HitAvg, 0.75)
	close(t, "HitNow", o.HitNow, 0.75)
}

func TestFoldEmptyDatabase(t *testing.T) {
	v := fold(nil, nil, nil, testRates, now, testCfg)

	if v.Traced != 0 || v.Cost != 0 || v.UnpricedReqs != 0 {
		t.Errorf("empty fold = %+v", v)
	}
	if len(v.Overview.Timeline) != 60 {
		t.Errorf("len(Timeline) = %d, want 60 buckets even when empty", len(v.Overview.Timeline))
	}
	o := v.Overview
	if o.BurnNow != 0 || o.BurnAvg != 0 || o.HitNow != 0 || o.HitAvg != 0 || o.PeakMin != 0 {
		t.Errorf("empty overview = %+v", o)
	}
	if o.Latency.P50Ttft != 0 || o.Latency.P95Ttft != 0 {
		t.Errorf("empty latency = %+v", o.Latency)
	}
	// The page must be able to draw this without a single guard.
	if v.Recent == nil {
		t.Error("Recent = nil, want an empty array so the page never sees null")
	}
	if b, err := json.Marshal(v); err != nil {
		t.Fatal(err)
	} else if bytes := string(b); bytes == "" {
		t.Error("empty stats did not marshal")
	}
}

func TestFoldTimelineBuckets(t *testing.T) {
	window := []store.Row{
		row(1, 0, "test-model", 1_000_000, 0, 0, 0, 0),  // this minute → last bucket
		row(2, 59, "test-model", 1_000_000, 0, 0, 0, 0), // 59 min ago → first bucket
		row(3, 30, "test-model", 2_000_000, 0, 0, 0, 0), // 30 min ago → the peak
		row(4, 90, "test-model", 9_000_000, 0, 0, 0, 0), // outside the timeline entirely
	}
	window[0].Status = 500 // an error in the newest bucket

	o := fold(nil, window, nil, testRates, now, testCfg).Overview
	tl := o.Timeline

	if len(tl) != 60 {
		t.Fatalf("len(Timeline) = %d, want 60", len(tl))
	}
	if tl[59].N != 1 || tl[59].Input != 1_000_000 {
		t.Errorf("newest bucket = %+v, want this minute's request", tl[59])
	}
	if tl[59].Err != 1 {
		t.Errorf("newest bucket Err = %d, want 1", tl[59].Err)
	}
	if tl[0].N != 1 {
		t.Errorf("oldest bucket = %+v, want the 59-minute-old request", tl[0])
	}
	if tl[29].N != 1 || tl[29].Input != 2_000_000 {
		t.Errorf("bucket 29 (30 min ago) = %+v", tl[29])
	}
	// The 90-minute-old row is out of range and must not have landed anywhere.
	var total int
	for _, b := range tl {
		total += b.N
	}
	if total != 3 {
		t.Errorf("Σ bucket counts = %d, want 3 — a row outside the hour leaked into the timeline", total)
	}

	// Peak minute is the most expensive bucket: 2M input tokens = $6.
	close(t, "PeakMin", o.PeakMin, 6)

	// Buckets are one minute apart, oldest first.
	if tl[1].T-tl[0].T != int64(time.Minute/time.Millisecond) {
		t.Errorf("bucket spacing = %dms, want 60000", tl[1].T-tl[0].T)
	}
	if last := time.UnixMilli(tl[59].T).UTC(); !last.Equal(now.Truncate(time.Minute)) {
		t.Errorf("last bucket = %v, want the current minute %v", last, now.Truncate(time.Minute))
	}
}

func TestFoldLatencyPercentiles(t *testing.T) {
	var window []store.Row
	for i, ttft := range []int64{100, 200, 300, 400, 500, 600, 700, 800, 900, 1000} {
		r := row(int64(i+1), 1, "test-model", 1000, 0, 0, 0, 100)
		r.TtftMs = ttft
		window = append(window, r)
	}
	// A row with no TTFT (never streamed a byte) must not drag the percentile down.
	noTTFT := row(99, 1, "test-model", 1000, 0, 0, 0, 0)
	noTTFT.TtftMs = 0
	window = append(window, noTTFT)

	o := fold(nil, window, nil, testRates, now, testCfg).Overview
	if o.Latency.P50Ttft != 500 {
		t.Errorf("P50Ttft = %d, want 500", o.Latency.P50Ttft)
	}
	if o.Latency.P95Ttft != 1000 {
		t.Errorf("P95Ttft = %d, want 1000", o.Latency.P95Ttft)
	}
}

// The long-context tier is why pricing cannot be a SQL SUM: whether a request
// crossed 200K input is a per-request fact that GROUP BY destroys.
func TestFoldPricesLongContextPerRow(t *testing.T) {
	rates := []billing.Rate{{
		Key: "test-model", InPerM: 3, OutPerM: 15,
		LongCtxThreshold: 200_000, LongCtxInPerM: 6, LongCtxOutPerM: 22.50,
	}}
	life := []store.UsageRow{
		usage(5, "test-model", 100_000, 0, 0, 0, 0), // under: 0.1M × $3 = $0.30
		usage(5, "test-model", 300_000, 0, 0, 0, 0), // over:  0.3M × $6 = $1.80
	}
	v := fold(life, nil, nil, rates, now, testCfg)

	// Summed first and priced second, these two rows (400K total) would price at
	// the long-context rate and come to $2.40. Priced per row, they are $2.10.
	close(t, "Cost", v.Cost, 0.30+1.80)
}

// The json tags of statsView ARE the /api/stats contract. This pins them against
// contracts/http-api.md so the two cannot drift apart silently.
func TestStatsViewJSONContract(t *testing.T) {
	v := fold(
		[]store.UsageRow{usage(1, "test-model", 1, 1, 1, 1, 1)},
		[]store.Row{row(1, 1, "test-model", 1, 1, 1, 1, 1)},
		[]store.Row{row(1, 1, "test-model", 1, 1, 1, 1, 1)},
		testRates, now, testCfg,
	)
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}

	for _, k := range []string{"port", "upstream", "traced", "cost", "unpricedReqs", "tokens", "overview", "recent"} {
		if _, ok := got[k]; !ok {
			t.Errorf("/api/stats is missing the contract key %q", k)
		}
	}

	ov, ok := got["overview"].(map[string]any)
	if !ok {
		t.Fatal("overview is not an object")
	}
	for _, k := range []string{"burnNow", "burnAvg", "reqHr", "winReqs", "avgReq", "hitNow", "hitAvg", "peakMin", "latency", "timeline", "windowMin", "tokens"} {
		if _, ok := ov[k]; !ok {
			t.Errorf("overview is missing the contract key %q", k)
		}
	}
	lat, ok := ov["latency"].(map[string]any)
	if !ok {
		t.Fatal("overview.latency is not an object")
	}
	for _, k := range []string{"p50Ttft", "p95Ttft"} {
		if _, ok := lat[k]; !ok {
			t.Errorf("overview.latency is missing the contract key %q", k)
		}
	}

	for _, path := range []struct {
		name string
		obj  map[string]any
	}{{"tokens", got}, {"overview.tokens", ov}} {
		q, ok := path.obj["tokens"].(map[string]any)
		if !ok {
			t.Fatalf("%s is not an object", path.name)
		}
		for _, k := range []string{"in", "read", "write", "out"} {
			if _, ok := q[k]; !ok {
				t.Errorf("%s is missing the quartet key %q", path.name, k)
			}
		}
	}

	tl, ok := ov["timeline"].([]any)
	if !ok || len(tl) != 60 {
		t.Fatalf("overview.timeline is not 60 buckets: %T len=%d", ov["timeline"], len(tl))
	}
	buck, ok := tl[0].(map[string]any)
	if !ok {
		t.Fatal("timeline bucket is not an object")
	}
	for _, k := range []string{"t", "n", "input", "cacheRead", "cacheWrite", "output", "costIn", "costRead", "costWrite", "costOut", "err"} {
		if _, ok := buck[k]; !ok {
			t.Errorf("timeline bucket is missing the key %q the dashboard draws", k)
		}
	}

	rec, ok := got["recent"].([]any)
	if !ok || len(rec) != 1 {
		t.Fatalf("recent is not a 1-element array: %T", got["recent"])
	}
	r0, ok := rec[0].(map[string]any)
	if !ok {
		t.Fatal("recent row is not an object")
	}
	for _, k := range []string{"id", "time", "model", "sid", "op", "status", "ms", "ttft", "stop", "tok", "cost", "priced", "bytes"} {
		if _, ok := r0[k]; !ok {
			t.Errorf("recent row is missing the contract key %q", k)
		}
	}
	for _, sub := range []string{"tok", "cost"} {
		q, ok := r0[sub].(map[string]any)
		if !ok {
			t.Fatalf("recent row %q is not an object", sub)
		}
		for _, k := range []string{"in", "read", "write", "out"} {
			if _, ok := q[k]; !ok {
				t.Errorf("recent row %s is missing the quartet key %q", sub, k)
			}
		}
	}
	by, ok := r0["bytes"].(map[string]any)
	if !ok {
		t.Fatal("recent row bytes is not an object")
	}
	for _, k := range []string{"total", "tools", "system", "messages"} {
		if _, ok := by[k]; !ok {
			t.Errorf("recent row bytes is missing the key %q", k)
		}
	}
}

// The quota probe Claude Code opens every session with. A 429 is the API's
// ANSWER to it, not a failure of it — so it must not land in the error count,
// or every session starts with a red row and the error rate is never zero.
func probeRow(id int64, minsAgo int) store.Row {
	return store.Row{
		ID:        id,
		TsMs:      now.Add(-time.Duration(minsAgo) * time.Minute).UnixMilli(),
		Endpoint:  "POST /v1/messages",
		ModelReq:  "test-model",
		Status:    429,
		MaxTokens: 1, // the tell: nothing that wants an answer asks for one token
		ToolCount: 0,
		Turns:     1,
		ErrType:   "rate_limit_error",
	}
}

func TestFoldQuotaProbeIsNotAnError(t *testing.T) {
	probe := probeRow(1, 2)
	real429 := row(2, 2, "test-model", 0, 0, 0, 0, 0)
	real429.Status = 429
	real429.ErrType = "rate_limit_error"
	real429.MaxTokens = 32_000 // a genuine request that genuinely failed
	real429.ToolCount = 40

	// Being a probe forgives the 429 and NOTHING else. A probe that 401s means the
	// key is dead — the single most useful thing a startup ping can tell you — and
	// swallowing it would turn a safety feature into a blindfold.
	deadKey := probeRow(3, 2)
	deadKey.Status = 401
	deadKey.ErrType = "authentication_error"

	rows := []store.Row{probe, real429, deadKey}
	v := fold(nil, rows, rows, testRates, now, testCfg)

	var errs int
	for _, b := range v.Overview.Timeline {
		errs += b.Err
	}
	if errs != 2 {
		t.Errorf("timeline errors = %d, want 2 (the real 429 and the probe's 401, never the probe's 429)", errs)
	}

	if !v.Recent[0].Probe {
		t.Error("the max_tokens:1 row should be flagged Probe so the page can draw it grey")
	}
	if v.Recent[1].Probe {
		t.Error("a real 429 from a real request must never be written off as a probe")
	}
	if !benignErr(probe) {
		t.Error("a probe's 429 is the answer to it, not a failure of it")
	}
	if benignErr(deadKey) {
		t.Error("a probe's 401 is a dead key and must stay loud")
	}
}

// BurnAvg is floored at one window on a fresh database, which pins it to BurnNow
// and makes their ratio exactly 1. Every trend the page could read off that ratio
// is then an artifact — most dangerously the reassuring one. ColdStart is what
// stops it claiming "steady" over a first-turn cache write that will never recur.
func TestFoldColdStartRefusesToClaimATrend(t *testing.T) {
	fresh := []store.UsageRow{usage(2, "test-model", 1_000_000, 0, 0, 0, 0)}
	v := fold(fresh, nil, nil, testRates, now, testCfg)
	if !v.Overview.ColdStart {
		t.Error("ColdStart should be true when the oldest row is younger than the window")
	}
	close(t, "burnNow≡burnAvg on a cold start", v.Overview.BurnAvg, v.Cost/(windowMin/60.0))

	// An hour of history is no longer a cold start, and the ratio means something.
	old := []store.UsageRow{usage(90, "test-model", 1_000_000, 0, 0, 0, 0)}
	if v := fold(old, nil, nil, testRates, now, testCfg); v.Overview.ColdStart {
		t.Error("ColdStart should be false once we have more than a window of history")
	}
}
