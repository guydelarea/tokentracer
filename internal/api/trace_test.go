package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/guydelarea/tokentracer/internal/billing"
	"github.com/guydelarea/tokentracer/internal/store"
)

// tr builds one row of a session, ageMin minutes ago, with a prefix chain.
func tr(id int64, ageMin int, in, read, write, out int64, prefix []string) store.Row {
	r := row(id, 0, "test-model", in, read, write, 0, out)
	r.TsMs = now.Add(-time.Duration(ageMin) * time.Minute).UnixMilli()
	r.SessionID = "s"
	if prefix != nil {
		b, _ := json.Marshal(prefix)
		r.Prefix = string(b)
	}
	return r
}

// foldAgents summarizes each subagent session out of one chronological pass of
// child rows: one entry per agent, first-spawned first, priced row by row.
func TestFoldAgents(t *testing.T) {
	a1 := tr(1, 9, 1000, 0, 0, 100, nil)
	a1.SessionID = "agent-1"
	a1.Label = "scan the repo"
	a2 := tr(2, 8, 500, 0, 0, 50, nil)
	a2.SessionID = "agent-2"
	a2.Label = "audit the proxy"
	a3 := tr(3, 7, 1000, 0, 0, 100, nil)
	a3.SessionID = "agent-1"
	fail := tr(4, 6, 0, 0, 0, 0, nil)
	fail.SessionID = "agent-2"
	fail.Status = 500

	got := foldAgents([]store.Row{a1, a2, a3, fail}, testRates, now)
	if len(got) != 2 {
		t.Fatalf("agents = %d, want 2", len(got))
	}
	if got[0].Sid != "agent-1" || got[1].Sid != "agent-2" {
		t.Errorf("order = %q, %q — want first-spawned first", got[0].Sid, got[1].Sid)
	}
	if got[0].Req != 2 || got[0].Err != 0 || got[0].Label != "scan the repo" {
		t.Errorf("agent-1 = %+v", got[0])
	}
	if got[1].Req != 2 || got[1].Err != 1 {
		t.Errorf("agent-2 req/err = %d/%d, want 2/1", got[1].Req, got[1].Err)
	}
	if !got[0].Priced || got[0].Cost <= 0 {
		t.Errorf("agent-1 cost = %v priced=%v, want a real figure", got[0].Cost, got[0].Priced)
	}
	if got[0].Tok.In != 2000 || got[0].Tok.Out != 200 {
		t.Errorf("agent-1 tok = %+v", got[0].Tok)
	}
}

func TestLinkSubagentsLeavesDuplicatePromptsUnlinked(t *testing.T) {
	turns := []flowTurn{
		{Calls: []flowCall{{Name: "Task", Agent: true, prompt: "same task"}}},
		{Calls: []flowCall{{Name: "Task", Agent: true, prompt: "same task"}}},
	}
	children := map[string][]agentRow{"same task": {{Sid: "child-1"}, {Sid: "child-2"}}}

	linkSubagents(turns, children)
	for i, turn := range turns {
		if turn.Calls[0].Spawn || turn.Calls[0].AgentSid != "" {
			t.Errorf("duplicate task %d was linked as a definite spawn: %+v", i, turn.Calls[0])
		}
	}
}

// The claim the trace exists to make: this request broke the cache, and THIS is
// what broke it. The prefix chain is cumulative over tools → system → messages,
// so the first index at which two requests diverge is not a hint about the cause
// — it is the cause.
func TestTraceNamesWhatBrokeTheCache(t *testing.T) {
	tests := []struct {
		name      string
		prevPfx   []string
		curPfx    []string
		gapMin    int
		wantCause string
		wantIdx   int
	}{
		{
			name:      "the toolset changed",
			prevPfx:   []string{"tools-A", "sys", "m0"},
			curPfx:    []string{"tools-B", "sys2", "m0'"},
			gapMin:    1,
			wantCause: causeTools,
			wantIdx:   0,
		},
		{
			name:      "the system prompt changed",
			prevPfx:   []string{"tools", "sys-A", "m0"},
			curPfx:    []string{"tools", "sys-B", "m0'"},
			gapMin:    1,
			wantCause: causeSystem,
			wantIdx:   1,
		},
		{
			name:      "a message in the history changed",
			prevPfx:   []string{"tools", "sys", "m0", "m1-A"},
			curPfx:    []string{"tools", "sys", "m0", "m1-B"},
			gapMin:    1,
			wantCause: causeMsg,
			wantIdx:   3,
		},
		{
			// The one the hashes cannot explain: nothing changed, the prefix simply
			// went cold. Blaming the messages here would send someone hunting for a
			// bug in a conversation that never had one.
			name:      "nothing changed — the TTL ran out",
			prevPfx:   []string{"tools", "sys", "m0"},
			curPfx:    []string{"tools", "sys", "m0"},
			gapMin:    30,
			wantCause: causeGap,
			wantIdx:   -1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// A hit first, so the prefix is primed and the re-write that follows is a
			// BREAK rather than the first write of the session. gapMin is the idle
			// time before the break — the only thing separating "you changed
			// something" from "it just went cold".
			rows := []store.Row{
				tr(1, tc.gapMin+1, 100, 0, 50_000, 10, tc.prevPfx), // prime
				tr(2, tc.gapMin, 100, 50_000, 0, 10, tc.prevPfx),   // hit
				tr(3, 0, 100, 0, 50_000, 10, tc.curPfx),            // …and re-written
			}

			v := foldTrace("s", rows, toolset{}, nil, false, testRates, now)

			if len(v.Cache) != 3 {
				t.Fatalf("cache events = %d, want one per request", len(v.Cache))
			}
			if v.Cache[0].Class != evPrime {
				t.Errorf("first write class = %q, want %q — there was nothing to hit yet", v.Cache[0].Class, evPrime)
			}
			if v.Cache[1].Class != evHit {
				t.Errorf("class = %q, want %q", v.Cache[1].Class, evHit)
			}

			got := v.Cache[2]
			if got.Class != evBreak {
				t.Fatalf("class = %q, want %q — a primed prefix was re-written", got.Class, evBreak)
			}
			if got.Cause != tc.wantCause {
				t.Errorf("cause = %q, want %q", got.Cause, tc.wantCause)
			}
			if got.BadIdx != tc.wantIdx {
				t.Errorf("badIdx = %d, want %d", got.BadIdx, tc.wantIdx)
			}

			// And what it cost: everything above what a hit would have billed.
			wantRebill := v.Rows[2].Cost.Write * billing.RebillWriteShare
			close(t, "rebill", got.Rebill, wantRebill)
			if v.Breaks != 1 {
				t.Errorf("breaks = %d, want 1", v.Breaks)
			}
			close(t, "BreakCost", v.BreakCost, wantRebill)
		})
	}
}

// A row recorded before the prefix column existed has no chain. It must not
// invent a cause: an unexplained break is the truth, and a wrong explanation is
// worse than none.
func TestTraceWithoutPrefixNamesNoSegment(t *testing.T) {
	rows := []store.Row{
		tr(1, 3, 100, 0, 50_000, 10, nil),
		tr(2, 2, 100, 50_000, 0, 10, nil),
		tr(3, 1, 100, 0, 50_000, 10, nil),
	}
	v := foldTrace("s", rows, toolset{}, nil, false, testRates, now)

	got := v.Cache[2]
	if got.Class != evBreak {
		t.Fatalf("class = %q, want a break", got.Class)
	}
	if got.BadIdx != -1 {
		t.Errorf("badIdx = %d, want -1 — there is no chain to name a segment from", got.BadIdx)
	}
}

// A compaction is not announced on the wire. The conversation simply collapses,
// which is a thing that does not otherwise happen.
func TestTraceSpotsACompaction(t *testing.T) {
	rows := []store.Row{
		tr(1, 5, 1000, 90_000, 0, 10, nil),
		tr(2, 4, 1000, 95_000, 0, 10, nil),
		tr(3, 3, 1000, 20_000, 0, 10, nil), // ← the history was compacted away
		tr(4, 2, 1000, 25_000, 0, 10, nil),
	}
	v := foldTrace("s", rows, toolset{}, nil, false, testRates, now)

	if len(v.Compacted) != 1 || v.Compacted[0] != 2 {
		t.Errorf("compacted = %v, want [2] — the request whose context collapsed", v.Compacted)
	}
}

// The context is read off the latest request that actually SPOKE. A 429 carries
// no usage, so reading it off the latest request full stop reports a session
// with no context at all — which every session ending on a quota probe would.
func TestTraceContextIgnoresErroredTail(t *testing.T) {
	real := tr(1, 2, 1000, 99_000, 0, 10, nil)
	dead := tr(2, 1, 0, 0, 0, 0, nil)
	dead.Status = 429
	dead.InputTokens, dead.CacheReadTokens = nil, nil

	v := foldTrace("s", []store.Row{real, dead}, toolset{}, nil, false, testRates, now)

	if v.Ctx != 100_000 {
		t.Errorf("ctx = %d, want 100000 — the context of the last request that spoke", v.Ctx)
	}
}

// Advice is priced, ranked by what it costs, and capped at three cards. A card
// nobody can act on is how a dashboard teaches you to ignore it.
func TestTraceInsightsAreRankedByCost(t *testing.T) {
	rows := []store.Row{
		tr(1, 3, 100, 0, 50_000, 10, nil),
		tr(2, 2, 100, 50_000, 0, 10, nil),
		tr(3, 1, 100, 0, 50_000, 10, nil), // a break: re-bills the write
	}
	rows[2].StopReason = "max_tokens" // …and a truncated reply on top

	v := foldTrace("s", rows, toolset{}, nil, false, testRates, now)

	if len(v.Insights) < 2 {
		t.Fatalf("insights = %+v, want the cache break and the truncation", v.Insights)
	}
	for i := 1; i < len(v.Insights); i++ {
		if v.Insights[i].Usd > v.Insights[i-1].Usd {
			t.Errorf("insights are not ranked by cost: %+v", v.Insights)
		}
	}
	if len(v.Insights) > 3 {
		t.Errorf("insights = %d, want at most 3", len(v.Insights))
	}
}
