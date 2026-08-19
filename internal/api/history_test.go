package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/guydelarea/tokentracer/internal/billing"
	"github.com/guydelarea/tokentracer/internal/store"
)

// A fixed clock in the server's OWN zone: the day buckets are cut in local time,
// so a test asserting on midnight has to mean the same midnight the fold does.
var histNow = time.Date(2026, 7, 13, 12, 0, 30, 0, time.Local)

// histRow builds a lifetime row stamped at a wall-clock hour in July 2026.
func histRow(day, hour int, model string, in, out int64) store.UsageRow {
	return store.UsageRow{
		TsMs:     time.Date(2026, 7, day, hour, 0, 0, 0, time.Local).UnixMilli(),
		ModelReq: model,
		Status:   200,
		In:       in,
		Out:      out,
	}
}

func midnight(day int) int64 {
	return time.Date(2026, 7, day, 0, 0, 0, 0, time.Local).UnixMilli()
}

func bucketCost(b dayBucket) float64 { return b.Cost.In + b.Cost.Read + b.Cost.Write + b.Cost.Out }

func TestFoldHistoryBucketsByLocalDay(t *testing.T) {
	life := []store.UsageRow{
		histRow(12, 23, "test-model", 1_000_000, 0), // last thing on the 12th
		histRow(13, 0, "test-model", 1_000_000, 0),  // first thing on the 13th
		histRow(13, 9, "test-model", 0, 1_000_000),
	}
	v := foldHistory(life, testRates, histNow)

	if len(v.Days) != 2 {
		t.Fatalf("days = %d, want 2 — an hour either side of midnight is two days", len(v.Days))
	}
	if v.Days[0].T != midnight(12) || v.Days[1].T != midnight(13) {
		t.Errorf("day stamps = %d, %d, want local midnight %d, %d", v.Days[0].T, v.Days[1].T, midnight(12), midnight(13))
	}
	if v.Days[0].N != 1 || v.Days[1].N != 2 {
		t.Errorf("requests per day = %d, %d, want 1, 2", v.Days[0].N, v.Days[1].N)
	}
	close(t, "the 12th", bucketCost(v.Days[0]), 3)
	close(t, "the 13th", bucketCost(v.Days[1]), 3+15)
}

// The two reasons a day's spend cannot be a SQL SUM: whether a request crossed
// the long-context threshold is a fact about that request, and the rate that
// applied is a fact about the instant it arrived. Both are destroyed by grouping
// first and pricing second.
func TestFoldHistoryPricesEveryRowIndividually(t *testing.T) {
	t.Run("the long-context tier is per request, not per day", func(t *testing.T) {
		rates := []billing.Rate{{
			Key: "test-model", InPerM: 3, OutPerM: 15,
			LongCtxThreshold: 200_000, LongCtxInPerM: 6, LongCtxOutPerM: 22.50,
		}}
		under := histRow(13, 9, "test-model", 100_000, 0) // 0.1M × $3 = $0.30
		over := histRow(13, 10, "test-model", 300_000, 0) // 0.3M × $6 = $1.80
		v := foldHistory([]store.UsageRow{under, over}, rates, histNow)

		if len(v.Days) != 1 {
			t.Fatalf("days = %d, want 1", len(v.Days))
		}
		// Each row's own price, asserted against billing.Compute on that one row.
		close(t, "day cost", bucketCost(v.Days[0]), rowBill(rates, under)+rowBill(rates, over))

		// Summed first and priced second, 400K of input crosses the threshold and
		// the day would come to $2.40 instead.
		close(t, "day cost", bucketCost(v.Days[0]), 0.30+1.80)
	})

	t.Run("a rate that changed during the day applies to the rows on its own side", func(t *testing.T) {
		noon := time.Date(2026, 7, 13, 8, 0, 0, 0, time.Local)
		rates := []billing.Rate{
			{Key: "test-model", InPerM: 6, OutPerM: 30, From: noon},
			{Key: "test-model", InPerM: 3, OutPerM: 15, Until: noon},
		}
		before := histRow(13, 7, "test-model", 1_000_000, 0) // $3
		after := histRow(13, 9, "test-model", 1_000_000, 0)  // $6
		v := foldHistory([]store.UsageRow{before, after}, rates, histNow)

		close(t, "day cost", bucketCost(v.Days[0]), rowBill(rates, before)+rowBill(rates, after))
		close(t, "day cost", bucketCost(v.Days[0]), 9) // never 2×$3, and never 2×$6
	})
}

// rowBill is what billing says one row costs on its own — the figure the day is
// only allowed to be a sum of.
func rowBill(rates []billing.Rate, u store.UsageRow) float64 {
	return billing.Compute(rates, billedModel(u.ModelReq, u.ModelServed), usageOf(u), time.UnixMilli(u.TsMs)).Total
}

// The footgun the whole project exists to fix, at day grain: a model with no
// rate must never be quietly worth $0.
func TestFoldHistoryUnpricedRowIsCountedNotZeroed(t *testing.T) {
	life := []store.UsageRow{
		histRow(13, 9, "test-model", 1_000_000, 0),
		histRow(13, 10, "some-unreleased-model", 9_000_000, 5_000_000),
	}
	v := foldHistory(life, testRates, histNow)
	d := v.Days[0]

	if d.Unpriced != 1 {
		t.Errorf("unpriced = %d, want 1", d.Unpriced)
	}
	close(t, "cost", bucketCost(d), 3) // the unpriced row adds nothing, and says so
	if _, ok := d.Models["some-unreleased-model"]; ok {
		t.Error("a model with no rate got a per-model figure — there is no number to put against it")
	}
	close(t, "priced model", d.Models["test-model"], 3)

	// It still happened, and it still sent real tokens.
	if d.N != 2 {
		t.Errorf("requests = %d, want 2 — an unpriced request is still a request", d.N)
	}
	if d.Tok.In != 10_000_000 || d.Tok.Out != 5_000_000 {
		t.Errorf("tokens = %+v — an unpriced row still sent and received real ones", d.Tok)
	}
}

// Claude Code opens every session with a max_tokens:1 quota ping the API answers
// with a 429. Counted as an error it puts a red tick on every single day.
func TestFoldHistoryQuotaProbeIsNotAnError(t *testing.T) {
	probe := histRow(13, 9, "test-model", 0, 0)
	probe.Status, probe.MaxTokens, probe.ToolCount = 429, 1, 0

	real500 := histRow(13, 10, "test-model", 0, 0)
	real500.Status = 500

	// Being a probe forgives the 429 and nothing else: a 401 here means the key
	// is dead, which is the most useful thing a startup ping can tell you.
	deadKey := histRow(13, 11, "test-model", 0, 0)
	deadKey.Status, deadKey.MaxTokens, deadKey.ToolCount = 401, 1, 0

	v := foldHistory([]store.UsageRow{probe, real500, deadKey}, testRates, histNow)
	d := v.Days[0]

	if d.Err != 2 {
		t.Errorf("errors = %d, want 2 (the 500 and the probe's 401, never the probe's 429)", d.Err)
	}
	if d.N != 3 {
		t.Errorf("requests = %d, want 3 — a forgiven error is still a request", d.N)
	}
	// None of them billed anything, so none of them belongs in a ranking of
	// spend: a model that spent the day failing is not a $0.00 line item.
	if len(d.Models) != 0 {
		t.Errorf("models = %v, want none — nothing here cost anything", d.Models)
	}
}

func TestFoldHistoryCountsDistinctSessionsPerDay(t *testing.T) {
	sid := func(u store.UsageRow, id string) store.UsageRow { u.SessionID = id; return u }
	life := []store.UsageRow{
		sid(histRow(12, 9, "test-model", 1, 0), "a"),
		sid(histRow(12, 10, "test-model", 1, 0), "a"), // same session, same day
		sid(histRow(12, 11, "test-model", 1, 0), "b"),
		sid(histRow(13, 9, "test-model", 1, 0), "b"), // ran again the next day
		histRow(13, 10, "test-model", 1, 0),          // no session id at all
	}
	v := foldHistory(life, testRates, histNow)

	if v.Days[0].Sessions != 2 {
		t.Errorf("the 12th = %d sessions, want 2", v.Days[0].Sessions)
	}
	if v.Days[1].Sessions != 2 {
		t.Errorf("the 13th = %d sessions, want 2 — the unnamed requests count as one", v.Days[1].Sessions)
	}
}

// A day nobody worked still has to draw as $0, so the page's range always has an
// end. Without today's bucket, a quiet morning looks like a day that never was.
func TestFoldHistoryAlwaysCarriesToday(t *testing.T) {
	v := foldHistory(nil, testRates, histNow)

	if len(v.Days) != 1 || v.Days[0].T != midnight(13) {
		t.Fatalf("days = %+v, want today and nothing else", v.Days)
	}
	if bucketCost(v.Days[0]) != 0 || v.Days[0].N != 0 {
		t.Errorf("today on an empty database = %+v", v.Days[0])
	}
	// An empty history still has to be an array the page can walk.
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == "" {
		t.Error("empty history did not marshal")
	}
}

// The json tags of historyView ARE the /api/history contract — the same rule the
// stats and trace views are pinned by.
func TestHistoryEndpointJSONContract(t *testing.T) {
	h, st, _ := newServer(t)

	// Empty database: the first thing a user sees must still be valid JSON.
	rec := get(t, h, "/api/history", "127.0.0.1:1234")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty /api/history → %d", rec.Code)
	}
	var empty historyView
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil {
		t.Fatalf("empty /api/history is not valid JSON: %v", err)
	}
	if len(empty.Days) != 1 {
		t.Errorf("empty history = %d days, want today's", len(empty.Days))
	}

	in, out := int64(1000), int64(500)
	if _, err := st.InsertExchange(store.Row{
		TsMs: time.Now().UnixMilli(), Endpoint: "POST /v1/messages", ModelReq: "claude-sonnet-5",
		SessionID: "sess-9", Status: 200, InputTokens: &in, OutputTokens: &out,
	}, nil, nil); err != nil {
		t.Fatal(err)
	}

	rec = get(t, h, "/api/history", "127.0.0.1:1234")
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	days, ok := got["days"].([]any)
	if !ok || len(days) != 1 {
		t.Fatalf("days is not a 1-element array: %T", got["days"])
	}
	d, ok := days[0].(map[string]any)
	if !ok {
		t.Fatal("day bucket is not an object")
	}
	for _, k := range []string{"t", "cost", "tok", "n", "err", "sessions", "unpriced", "models"} {
		if _, ok := d[k]; !ok {
			t.Errorf("day bucket is missing the contract key %q", k)
		}
	}
	for _, sub := range []string{"cost", "tok"} {
		q, ok := d[sub].(map[string]any)
		if !ok {
			t.Fatalf("day %q is not an object", sub)
		}
		for _, k := range []string{"in", "read", "write", "out"} {
			if _, ok := q[k]; !ok {
				t.Errorf("day %s is missing the quartet key %q", sub, k)
			}
		}
	}
	// The fixture's model is in the seed rate table, so the day must price.
	models, ok := d["models"].(map[string]any)
	if !ok || len(models) != 1 {
		t.Errorf("models = %v, want the one model that ran", d["models"])
	}
}
