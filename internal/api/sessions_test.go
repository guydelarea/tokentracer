package api

import (
	"testing"
	"time"

	"github.com/guydelarea/tokentracer/internal/anthropic"
	"github.com/guydelarea/tokentracer/internal/store"
)

// su builds one row of the lifetime scan, the projection the sessions table
// folds over.
func su(sid string, ageMin int, in, read, write, out int64) store.UsageRow {
	return store.UsageRow{
		TsMs:      now.Add(-time.Duration(ageMin) * time.Minute).UnixMilli(),
		ModelReq:  "test-model",
		SessionID: sid,
		Status:    200,
		In:        in, Read: read, W5m: write, Out: out,
	}
}

// The session's name is the first thing asked of the model. Claude Code opens
// every session with a max_tokens:1 ping whose text is the word "quota" — so a
// fold that takes the first label it sees names every session on the page
// "quota", which is how a table full of names manages to name nothing.
func TestSessionLabelIsNeverTheQuotaProbe(t *testing.T) {
	probe := su("s1", 9, 0, 0, 0, 0)
	probe.Status = 429
	probe.MaxTokens = 1
	probe.Label = "quota"

	real := su("s1", 8, 1000, 0, 0, 100)
	real.Label = "port the design"
	real.ToolCount = 3

	v := foldSessions([]store.UsageRow{probe, real}, nil, testRates, now)

	if len(v) != 1 {
		t.Fatalf("sessions = %d, want 1", len(v))
	}
	if v[0].Label != "port the design" {
		t.Errorf("Label = %q, want the first real prompt", v[0].Label)
	}
	// The probe still counts as a request — it happened — but its 429 is the
	// answer to it, not a failure of it.
	if v[0].Req != 2 || v[0].Err != 0 {
		t.Errorf("req/err = %d/%d, want 2/0 — the probe's own 429 is not an error", v[0].Req, v[0].Err)
	}
}

// A schema that ships on every request and is never called is the cut list, and
// the number next to it is what it costs to keep shipping it.
func TestSessionCutList(t *testing.T) {
	called := su("s1", 5, 1000, 0, 0, 100)
	called.Op = "tool_use · Bash — git status"
	called.ToolCount = 2

	quiet := su("s1", 4, 1000, 0, 0, 100)
	quiet.ToolCount = 2

	tools := map[string]toolset{"s1": {Items: []anthropic.ToolItem{
		{Name: "Bash", Bytes: 400},
		{Name: "NeverCalled", Bytes: 4000},
	}}}

	v := foldSessions([]store.UsageRow{called, quiet}, tools, testRates, now)[0]

	if v.Unused != 1 || v.UnusedBytes != 4000 {
		t.Errorf("unused = %d schemas / %d bytes, want the one nothing called", v.Unused, v.UnusedBytes)
	}
	if v.UnusedTok != 1000 { // 4000 bytes at the 4:1 approximation
		t.Errorf("UnusedTok = %d, want 1000", v.UnusedTok)
	}
	// Both requests are inside the window, so the cadence is 2 × (60/10) = 12/hr.
	// test-model's input rate is $3/MTok and a cache read bills at 0.1× of it:
	// 1000 tok × $3e-6 × 0.1 × 12 = $0.0036/hr.
	close(t, "WasteHr", v.WasteHr, 0.0036)

	// The tool it actually called is not on the list, however big it is.
	if v.Unused == 2 {
		t.Error("a tool the session called landed on the cut list")
	}
}

// A session is live if it spoke recently; otherwise the row says how long it has
// been quiet, and its $/hr is zero because it is no longer spending anything.
func TestSessionLiveness(t *testing.T) {
	v := foldSessions([]store.UsageRow{
		su("live", 0, 1_000_000, 0, 0, 0),
		su("done", 120, 1_000_000, 0, 0, 0),
	}, nil, testRates, now)

	if len(v) != 2 {
		t.Fatalf("sessions = %d", len(v))
	}
	// Live first: what is burning money now is what you are here to look at.
	if v[0].ID != "live" || !v[0].Live {
		t.Errorf("first row = %q live=%v, want the live session first", v[0].ID, v[0].Live)
	}
	if v[1].Live {
		t.Error("a session that has been quiet for two hours is not live")
	}
	if v[1].RateHr != 0 {
		t.Errorf("RateHr = %v for a finished session, want 0 — it is not spending anything", v[1].RateHr)
	}
	if v[1].Idle != "2h" {
		t.Errorf("Idle = %q, want 2h", v[1].Idle)
	}
}

// The sessions table and the overview total are folded from the same rows, and
// priced the same way. If they disagreed, neither could be believed.
func TestSessionCostsSumToTheOverviewTotal(t *testing.T) {
	life := []store.UsageRow{
		su("a", 5, 1_000_000, 0, 0, 1_000_000),
		su("b", 4, 500_000, 2_000_000, 0, 100_000),
		su("a", 3, 250_000, 0, 100_000, 50_000),
	}
	v := fold(life, nil, nil, testRates, now, testCfg)

	var sum float64
	for _, s := range v.Sessions {
		sum += s.Cost
	}
	close(t, "Σ session costs", sum, v.Cost)
}
