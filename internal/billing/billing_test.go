package billing

import (
	"math"
	"strings"
	"testing"
	"time"
)

const eps = 1e-9

func closeTo(t *testing.T, got, want float64, what string) {
	t.Helper()
	if math.Abs(got-want) > eps {
		t.Errorf("%s = %.9f, want %.9f", what, got, want)
	}
}

// Fixed instants for the window tests. No relation to the seed table, which
// carries no history.
var (
	t2025 = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2026 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2027 = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		{"plain", "claude-sonnet-5", "claude-sonnet-5"},
		{"bedrock prefix", "anthropic.claude-sonnet-5", "claude-sonnet-5"},
		{"vertex date suffix", "claude-sonnet-4@20250514", "claude-sonnet-4"},
		{"prefix and suffix", "anthropic.claude-sonnet-4@20250514", "claude-sonnet-4"},
		{"bedrock version suffix", "anthropic.claude-opus-4-5-v1:0", "claude-opus-4-5"},
		// Only Vertex's @date is stripped, never the native -date suffix: the
		// seed table's keys carry those dates, so stripping would break matching.
		{"native dated name is left alone", "claude-opus-4-1-20250805", "claude-opus-4-1-20250805"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalize(tt.model); got != tt.want {
				t.Errorf("normalize(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

// A rate matches on substring, and the first hit in slice order wins — so a
// more specific key must be able to shadow a generic one placed after it.
func TestComputeMatchingFirstHitWins(t *testing.T) {
	rates := []Rate{
		{Key: "claude-sonnet-4-5", InPerM: 3, OutPerM: 15},
		{Key: "claude-sonnet-4", InPerM: 900, OutPerM: 900}, // generic: must lose to the key above
		{Key: "claude-opus", InPerM: 15, OutPerM: 75},
	}
	tests := []struct {
		name       string
		model      string
		wantPriced bool
		wantIn     float64 // cost of exactly 1M input tokens == the matched InPerM
	}{
		{"specific key wins over the generic one after it", "claude-sonnet-4-5", true, 3},
		{"dated variant of the specific key still wins", "claude-sonnet-4-5-20250929", true, 3},
		{"generic key catches what the specific one misses", "claude-sonnet-4-20250514", true, 900},
		{"substring match anywhere in the name", "anthropic.claude-opus-4-1@20250805", true, 15},
		{"no key matches", "gpt-5", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := Compute(rates, tt.model, Usage{In: 1_000_000}, t2026)
			if b.Priced != tt.wantPriced {
				t.Fatalf("Priced = %v, want %v", b.Priced, tt.wantPriced)
			}
			closeTo(t, b.In, tt.wantIn, "In")
		})
	}
}

// [From, Until): From is inclusive, Until is exclusive. Zero From means valid
// from the past; zero Until means open-ended.
func TestComputeRateWindows(t *testing.T) {
	rates := []Rate{
		{Key: "m", InPerM: 1, OutPerM: 1, From: t2025, Until: t2026}, // expired after 2026
		{Key: "m", InPerM: 2, OutPerM: 2, From: t2026, Until: t2027}, // current
		{Key: "m", InPerM: 3, OutPerM: 3, From: t2027},               // open-ended
	}
	tests := []struct {
		name       string
		at         time.Time
		wantPriced bool
		wantIn     float64
	}{
		{"before every window: no rate is in effect yet", t2025.Add(-time.Nanosecond), false, 0},
		{"From is inclusive", t2025, true, 1},
		{"inside the first window", t2025.Add(24 * time.Hour), true, 1},
		{"Until is exclusive: the instant before belongs to the old rate", t2026.Add(-time.Nanosecond), true, 1},
		{"Until is exclusive: at the boundary the next rate takes over", t2026, true, 2},
		{"open-ended window has no upper bound", t2027.AddDate(50, 0, 0), true, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := Compute(rates, "m", Usage{In: 1_000_000}, tt.at)
			if b.Priced != tt.wantPriced {
				t.Fatalf("Priced = %v, want %v", b.Priced, tt.wantPriced)
			}
			closeTo(t, b.In, tt.wantIn, "In")
		})
	}
}

// An expired rate must not shadow a live one that sits after it in the slice:
// the window is part of the match, not a filter applied after it.
func TestComputeSkipsExpiredRateForLaterMatch(t *testing.T) {
	rates := []Rate{
		{Key: "m", InPerM: 1, OutPerM: 1, From: t2025, Until: t2026},
		{Key: "m", InPerM: 2, OutPerM: 2, From: t2026},
	}
	b := Compute(rates, "m", Usage{In: 1_000_000}, t2026.AddDate(0, 6, 0))
	if !b.Priced {
		t.Fatal("Priced = false, want true")
	}
	closeTo(t, b.In, 2, "In")
}

// Cache multipliers ride on the input base rate: read 0.1x, write-5m 1.25x,
// write-1h 2.0x.
func TestComputeMultipliers(t *testing.T) {
	rates := []Rate{{Key: "m", InPerM: 3, OutPerM: 15}}
	u := Usage{
		In:      1_000_000,
		Read:    1_000_000,
		Write5m: 1_000_000,
		Write1h: 1_000_000,
		Out:     1_000_000,
	}
	b := Compute(rates, "m", u, t2026)
	if !b.Priced {
		t.Fatal("Priced = false, want true")
	}
	closeTo(t, b.In, 3, "In")              // 3 * 1.0
	closeTo(t, b.Read, 0.3, "Read")        // 3 * 0.1
	closeTo(t, b.Write, 3.75+6.0, "Write") // 5m: 3 * 1.25, 1h: 3 * 2.0, combined
	closeTo(t, b.Out, 15, "Out")
	closeTo(t, b.Total, 3+0.3+9.75+15, "Total")
}

func TestComputeTotalIsSumOfComponents(t *testing.T) {
	rates := []Rate{{Key: "m", InPerM: 3, OutPerM: 15}}
	u := Usage{In: 1234, Read: 98_765, Write5m: 4_321, Write1h: 777, Out: 5_000}
	b := Compute(rates, "m", u, t2026)
	closeTo(t, b.Total, b.In+b.Read+b.Write+b.Out, "Total")
}

// The long-context tier kicks in only ABOVE the threshold — exactly at it is
// still standard pricing.
func TestComputeLongContextTier(t *testing.T) {
	rates := []Rate{{
		Key: "m", InPerM: 3, OutPerM: 15,
		LongCtxThreshold: 200_000, LongCtxInPerM: 6, LongCtxOutPerM: 22.50,
	}}
	tests := []struct {
		name      string
		u         Usage
		wantIn    float64
		wantRead  float64
		wantWrite float64
		wantOut   float64
	}{
		{
			name:    "below the threshold: standard rates",
			u:       Usage{In: 100_000, Out: 1_000},
			wantIn:  0.100_000 * 3, // 0.3
			wantOut: 0.001 * 15,    // 0.015
		},
		{
			name:    "exactly at the threshold: still standard rates",
			u:       Usage{In: 200_000, Out: 1_000},
			wantIn:  0.2 * 3,    // 0.6
			wantOut: 0.001 * 15, // 0.015
		},
		{
			name:    "one token above the threshold: long-context rates",
			u:       Usage{In: 200_001, Out: 1_000},
			wantIn:  0.200_001 * 6, // 1.200006
			wantOut: 0.001 * 22.50, // 0.0225
		},
		{
			name: "threshold counts every input component, and the multipliers ride the long-context base rate",
			// 100_000 + 50_000 + 30_000 + 20_001 = 200_001 total input
			u:         Usage{In: 100_000, Read: 50_000, Write5m: 30_000, Write1h: 20_001, Out: 1_000},
			wantIn:    0.100_000 * 6,                      // 0.6
			wantRead:  0.050_000 * 6 * 0.1,                // 0.03
			wantWrite: 0.030_000*6*1.25 + 0.020_001*6*2.0, // 0.225 + 0.240012
			wantOut:   0.001 * 22.50,                      // 0.0225
		},
		{
			name: "cache reads alone can push a request over the threshold",
			// Claude Code's normal shape: a huge cached prefix, a tiny new turn.
			u:        Usage{In: 500, Read: 250_000, Out: 1_000},
			wantIn:   0.000_500 * 6,       // 0.003
			wantRead: 0.250_000 * 6 * 0.1, // 0.15
			wantOut:  0.001 * 22.50,       // 0.0225
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := Compute(rates, "m", tt.u, t2026)
			if !b.Priced {
				t.Fatal("Priced = false, want true")
			}
			closeTo(t, b.In, tt.wantIn, "In")
			closeTo(t, b.Read, tt.wantRead, "Read")
			closeTo(t, b.Write, tt.wantWrite, "Write")
			closeTo(t, b.Out, tt.wantOut, "Out")
			closeTo(t, b.Total, tt.wantIn+tt.wantRead+tt.wantWrite+tt.wantOut, "Total")
		})
	}
}

// A rate with no long-context tier (LongCtxThreshold == 0) never switches,
// however large the input gets.
func TestComputeNoLongContextTier(t *testing.T) {
	rates := []Rate{{Key: "m", InPerM: 3, OutPerM: 15}}
	b := Compute(rates, "m", Usage{In: 10_000_000, Out: 1_000_000}, t2026)
	closeTo(t, b.In, 30, "In")
	closeTo(t, b.Out, 15, "Out")
}

// The footgun this whole package exists to fix: an unknown model must never
// look like a priced $0 request.
func TestComputeUnknownModelIsUnpriced(t *testing.T) {
	rates := []Rate{{Key: "claude-sonnet-5", InPerM: 2, OutPerM: 10}}
	u := Usage{In: 500_000, Read: 1_000_000, Write5m: 10_000, Write1h: 5_000, Out: 20_000}
	for _, model := range []string{"gpt-5", "gemini-3-pro", "", "sonnet"} {
		t.Run(model, func(t *testing.T) {
			b := Compute(rates, model, u, t2026)
			if b.Priced {
				t.Fatalf("Priced = true, want false for unknown model %q", model)
			}
			if b.In != 0 || b.Read != 0 || b.Write != 0 || b.Out != 0 || b.Total != 0 {
				t.Errorf("unknown model must cost zero, got %+v", b)
			}
		})
	}
}

func TestComputeZeroUsageIsPricedZero(t *testing.T) {
	rates := []Rate{{Key: "m", InPerM: 3, OutPerM: 15}}
	b := Compute(rates, "m", Usage{}, t2026)
	if !b.Priced {
		t.Error("Priced = false, want true: a known model with no usage is priced, just free")
	}
	closeTo(t, b.Total, 0, "Total")
}

// The fixture in testdata/ is a claude-sonnet-5 request. If the seed table ever
// stops matching it, every cost in the dashboard silently goes unpriced — this
// test is the tripwire.
func TestSeedTablePricesTheFixtureModel(t *testing.T) {
	b := Compute(Rates, "claude-sonnet-5", Usage{In: 1_000, Out: 1_000}, time.Now())
	if !b.Priced {
		t.Fatal("claude-sonnet-5 is unpriced in the seed table; the fixture model must price")
	}
	if b.Total <= 0 {
		t.Fatalf("Total = %v, want > 0", b.Total)
	}
}

// The seed table must survive the names that actually arrive on the wire.
func TestSeedTablePricesRealModelNames(t *testing.T) {
	models := []string{
		"claude-sonnet-5",
		"claude-opus-4-5",
		"claude-haiku-4-5",
		"claude-sonnet-4-5-20250929",
		"anthropic.claude-sonnet-4-5",
		"claude-3-7-sonnet-20250219",
		// The launch-gap models. Every one of these was UNPRICED in the dashboard
		// until its row was added by hand, which is the failure this test pins.
		"claude-opus-5",
		"claude-opus-5[1m]",      // Claude Code's 1M-window spelling
		"claude-opus-5@20260601", // Vertex's spelling
		"claude-mythos-5",
	}
	for _, m := range models {
		t.Run(m, func(t *testing.T) {
			b := Compute(Rates, m, Usage{In: 1_000_000, Out: 1_000_000}, time.Now())
			if !b.Priced {
				t.Fatalf("%q is unpriced in the seed table", m)
			}
			// Sanity band: no Claude model is free, and none costs more than
			// $100/1M in or $500/1M out.
			if b.In <= 0 || b.In > 100 {
				t.Errorf("In = %v/1M, outside the plausible band", b.In)
			}
			if b.Out <= 0 || b.Out > 500 {
				t.Errorf("Out = %v/1M, outside the plausible band", b.Out)
			}
		})
	}
}

// The one dated price in the table. Sonnet 5's introductory rate ends on a
// published date, and a session traced either side of it must bill at what it
// actually cost — not at whichever number happened to be hard-coded.
func TestSeedTableHonoursTheSonnet5IntroWindow(t *testing.T) {
	oneM := Usage{In: 1_000_000, Out: 1_000_000}

	intro := Compute(Rates, "claude-sonnet-5", oneM, sonnet5StandardFrom.Add(-time.Hour))
	if !intro.Priced {
		t.Fatal("claude-sonnet-5 is unpriced inside the intro window")
	}
	closeTo(t, intro.In, 2, "intro In")
	closeTo(t, intro.Out, 10, "intro Out")

	std := Compute(Rates, "claude-sonnet-5", oneM, sonnet5StandardFrom)
	if !std.Priced {
		t.Fatal("claude-sonnet-5 is unpriced once the intro window closes — the standard row is missing")
	}
	closeTo(t, std.In, 3, "standard In")
	closeTo(t, std.Out, 15, "standard Out")
}

// Ordering invariant: the seed table is sorted most-specific-key-first, so no
// key may be a substring of a key that appears before it (that earlier, shorter
// key would swallow every model the later one is meant to catch).
func TestSeedTableOrderingIsMostSpecificFirst(t *testing.T) {
	for i, r := range Rates {
		for j := 0; j < i; j++ {
			if len(Rates[j].Key) < len(r.Key) && strings.Contains(r.Key, Rates[j].Key) {
				t.Errorf("Rates[%d] %q is shadowed by the more generic Rates[%d] %q that precedes it",
					i, r.Key, j, Rates[j].Key)
			}
		}
	}
}
