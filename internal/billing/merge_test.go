package billing

import (
	"math"
	"strings"
	"testing"
	"time"
)

var epoch = time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)

// priceOf reads a model's STANDARD-tier input rate, as dollars per 1M tokens.
//
// It bills 100k tokens and scales the answer up rather than billing a round 1M,
// because 1M is over every long-context threshold in the table — a test written
// the obvious way silently asserts against the premium rate instead.
func priceOf(t *testing.T, rates []Rate, model string) Bill {
	t.Helper()
	const n = 100_000
	b := Compute(rates, model, Usage{In: n}, epoch)
	b.In *= 1e6 / n
	b.Total *= 1e6 / n
	return b
}

// approx compares dollars. priceOf scales, so exact equality would fail on noise.
func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func mergeOf(t *testing.T, embedded, fetched []Rate) []Rate {
	t.Helper()
	got, _ := Merge(embedded, fetched)
	return got
}

// The point of the refresh: a vendor's price change lands without a release, and
// because cost is computed at read time it corrects history too.
func TestMergeRepricesAnEmbeddedRow(t *testing.T) {
	embedded := []Rate{{Key: "claude-opus-4-5", InPerM: 5, OutPerM: 25}}
	published := []Rate{{Key: "claude-opus-4-5", InPerM: 3, OutPerM: 15}}

	if got := priceOf(t, mergeOf(t, embedded, published), "claude-opus-4-5"); !approx(got.In, 3) {
		t.Errorf("in = $%v/1M, want $3 — the published price did not land", got.In)
	}
}

// A reprice restates what every recorded exchange on that model cost, so it may
// never happen quietly. The startup screen is the one place it can be noticed.
func TestMergeReportsWhatItRepriced(t *testing.T) {
	embedded := []Rate{
		{Key: "claude-opus-4-5", InPerM: 5, OutPerM: 25},
		{Key: "claude-sonnet-5", InPerM: 2, OutPerM: 10},
	}
	published := []Rate{
		{Key: "claude-opus-4-5", InPerM: 3, OutPerM: 15}, // moved
		{Key: "claude-sonnet-5", InPerM: 2, OutPerM: 10}, // agrees
		{Key: "claude-neue-9", InPerM: 4, OutPerM: 20},   // new
	}

	_, stats := Merge(embedded, published)

	if len(stats.Repriced) != 1 || stats.Repriced[0] != "claude-opus-4-5" {
		t.Errorf("repriced = %v, want just claude-opus-4-5 — a row the list agrees with is not a change",
			stats.Repriced)
	}
	if len(stats.Filled) != 1 || stats.Filled[0] != "claude-neue-9" {
		t.Errorf("filled = %v, want just claude-neue-9", stats.Filled)
	}
}

// Commit a27c007 stripped the sonnet-4 family's above-200k tiers as a beta that
// no longer exists. The published list still carries them. Repricing a row must
// never become a way of asserting a tier that was deliberately deleted.
func TestMergeCannotIntroduceALongContextTier(t *testing.T) {
	embedded := []Rate{{Key: "claude-sonnet-4-5", InPerM: 3, OutPerM: 15}}
	published := []Rate{{
		Key: "claude-sonnet-4-5", InPerM: 3, OutPerM: 15,
		LongCtxThreshold: 200_000, LongCtxInPerM: 6, LongCtxOutPerM: 22.5,
	}}

	// Well past the tier the list would have applied: $0.90 standard, $1.80 tiered.
	over := Compute(mergeOf(t, embedded, published), "claude-sonnet-4-5", Usage{In: 300_000}, epoch)
	if want := 300_000.0 / 1e6 * 3; math.Abs(over.In-want) > 1e-9 {
		t.Errorf("in = $%v, want $%v — a removed long-context tier came back through the refresh", over.In, want)
	}
}

// The same rule pointing the other way. GPT-5.6's tier is real and reachable, so
// a published row that happens not to carry one must not silently delete it.
func TestMergeCannotDropALongContextTier(t *testing.T) {
	embedded := []Rate{{
		Key: "gpt-5.6-sol", InPerM: 4, OutPerM: 20,
		LongCtxThreshold: 272_000, LongCtxInPerM: 8, LongCtxOutPerM: 30,
	}}
	published := []Rate{{Key: "gpt-5.6-sol", InPerM: 4, OutPerM: 20}} // no tier published

	over := Compute(mergeOf(t, embedded, published), "gpt-5.6-sol", Usage{In: 300_000}, epoch)
	if want := 300_000.0 / 1e6 * 8; math.Abs(over.In-want) > 1e-9 {
		t.Errorf("in = $%v, want $%v — a verified long-context tier was dropped", over.In, want)
	}
}

// A tier the table already declares gets its numbers refreshed like any price.
func TestMergeRefreshesAnExistingLongContextTier(t *testing.T) {
	embedded := []Rate{{
		Key: "gpt-5.6-sol", InPerM: 4, OutPerM: 20,
		LongCtxThreshold: 272_000, LongCtxInPerM: 8, LongCtxOutPerM: 30,
	}}
	published := []Rate{{
		Key: "gpt-5.6-sol", InPerM: 4, OutPerM: 20,
		LongCtxThreshold: 272_000, LongCtxInPerM: 6, LongCtxOutPerM: 24,
	}}

	over := Compute(mergeOf(t, embedded, published), "gpt-5.6-sol", Usage{In: 300_000}, epoch)
	if want := 300_000.0 / 1e6 * 6; math.Abs(over.In-want) > 1e-9 {
		t.Errorf("in = $%v, want $%v — the tier's numbers did not refresh", over.In, want)
	}
}

// A published list is a price source, not a statement about how this table is
// keyed. Repricing must move numbers and nothing else.
func TestMergeRepriceDoesNotChangeKeying(t *testing.T) {
	until := epoch.Add(24 * time.Hour)
	embedded := []Rate{{Key: "claude-opus-4-5", InPerM: 5, OutPerM: 25, Until: until}}
	published := []Rate{{Key: "claude-opus-4-5", Exact: true, InPerM: 3, OutPerM: 15}}

	got := mergeOf(t, embedded, published)[0]

	if got.Exact {
		t.Error("reprice turned a hand-written substring key into an exact one")
	}
	if got.Key != "claude-opus-4-5" || !got.Until.Equal(until) {
		t.Errorf("row = %+v, want the hand-written key and window intact", got)
	}
	// Still substring-matched, which is what covers the Bedrock spelling.
	if bill := priceOf(t, mergeOf(t, embedded, published), "anthropic.claude-opus-4-5-v1:0"); !approx(bill.In, 3) {
		t.Errorf("bedrock spelling = $%v/1M, want the repriced $3", bill.In)
	}
}

// A model the embedded table has no opinion about is what the refresh adds.
func TestMergeFillsAHole(t *testing.T) {
	const model = "claude-neue-9-20270101"

	if priceOf(t, Rates, model).Priced {
		t.Fatalf("%s is already priced — pick a model the embedded table does not cover", model)
	}
	got := priceOf(t, mergeOf(t, Rates, []Rate{{Key: model, InPerM: 4, OutPerM: 20}}), model)
	if !got.Priced || !approx(got.In, 4) {
		t.Errorf("bill = %+v, want priced at $4/1M", got)
	}
}

// The whole reason filled rows are exact-matched. The list publishes bare family
// keys — "gpt-4", "gpt-5" — and as substring keys they would price an unreleased
// sibling at the family's rate. rates.go bans exactly this for hand-written rows;
// a refresh must not smuggle it back in.
func TestMergedFetchedKeyDoesNotSwallowAnUnreleasedSibling(t *testing.T) {
	merged := mergeOf(t, Rates, []Rate{{Key: "gpt-4", InPerM: 30, OutPerM: 60}})

	if got := priceOf(t, merged, "gpt-4"); !got.Priced {
		t.Error("gpt-4 itself came out unpriced — the filled row should cover its own name")
	}
	if got := priceOf(t, merged, "gpt-4-whatever-ships-next"); got.Priced {
		t.Errorf("gpt-4-whatever-ships-next priced at $%v/1M, want UNPRICED — "+
			"a bare family key from the refresh swallowed a model nobody priced", got.In)
	}
}

// The same failure, but the one that was actually live: "gpt-5.6" is a published
// alias for sol, and as a substring key it billed gpt-5.6-cyber ($12.50/$75) at
// sol's $4/$20. rates.go asked for this the moment a variant shipped.
func TestGPT56AliasDoesNotSwallowItsVariants(t *testing.T) {
	if got := priceOf(t, Rates, "gpt-5.6"); !got.Priced || !approx(got.In, 4) {
		t.Errorf("gpt-5.6 = %+v, want sol's $4/1M — the alias must still price itself", got)
	}
	if got := priceOf(t, Rates, "gpt-5.6-cyber"); got.Priced {
		t.Errorf("gpt-5.6-cyber priced at $%v/1M off the alias row, want UNPRICED so the "+
			"refresh can fill in its real price", got.In)
	}
}

// A gateway puts its route in front of the model, and that prefix reaches the row
// whenever the response never named the model it served (the LiteLLM e2e case,
// where ModelReq is "anthropic/claude-sonnet-5").
func TestMergedFetchedKeyMatchesThroughARoutePrefix(t *testing.T) {
	merged := mergeOf(t, Rates, []Rate{{Key: "claude-neue-9", InPerM: 4, OutPerM: 20}})

	if got := priceOf(t, merged, "anthropic/claude-neue-9"); !got.Priced || !approx(got.In, 4) {
		t.Errorf("bill = %+v, want priced at $4/1M through the route prefix", got)
	}
	// ...but the prefix is not a licence to match loosely.
	if got := priceOf(t, merged, "anthropic/claude-neue-9-turbo"); got.Priced {
		t.Errorf("claude-neue-9-turbo priced at $%v/1M, want UNPRICED", got.In)
	}
}

// Merge owns the exact-matching guarantee, so it must hold even for a caller that
// hands over substring rows.
func TestMergeForcesExactOnEveryFilledRow(t *testing.T) {
	merged := mergeOf(t, nil, []Rate{{Key: "gpt-9", InPerM: 1, OutPerM: 2, Exact: false}})

	if len(merged) != 1 || !merged[0].Exact {
		t.Fatalf("merged = %+v, want one row with Exact set", merged)
	}
}

// A refresh that returns nothing — disabled, failed, or filtered to empty — must
// leave the running table exactly as it was.
func TestMergeWithNoFetchChangesNothing(t *testing.T) {
	merged, stats := Merge(Rates, nil)

	if len(merged) != len(Rates) {
		t.Fatalf("merged = %d rows, want the embedded %d", len(merged), len(Rates))
	}
	for i := range Rates {
		if merged[i] != Rates[i] {
			t.Errorf("row %d = %+v, want %+v", i, merged[i], Rates[i])
		}
	}
	if len(stats.Repriced) != 0 || len(stats.Filled) != 0 {
		t.Errorf("stats = %+v, want nothing reported", stats)
	}
}

// The seed table's ordering invariant, re-asserted on a merged table: filled rows
// are appended after every embedded row, so they can never shadow one, and being
// exact they cannot shadow each other either.
func TestMergedTableKeepsTheOrderingInvariant(t *testing.T) {
	fetched := []Rate{
		{Key: "gpt-4", InPerM: 30, OutPerM: 60},
		{Key: "claude-neue-9", InPerM: 4, OutPerM: 20},
		{Key: "gpt-9", InPerM: 1, OutPerM: 2},
	}
	merged := mergeOf(t, Rates, fetched)

	for i, r := range merged[len(Rates):] {
		if !r.Exact {
			t.Errorf("filled row %d %q is not Exact", i, r.Key)
			// Being exact is the whole defence: a filled key that DOES contain an
			// embedded one is fine, because it only ever matches its own full name.
			for j := range Rates {
				if strings.Contains(r.Key, Rates[j].Key) {
					t.Errorf("  ...and %q is shadowed by the embedded %q", r.Key, Rates[j].Key)
				}
			}
		}
	}
}

// Substring matching is what makes one hand-written key cover the Bedrock and
// Vertex spellings of a model. Merging must not quietly narrow it.
func TestMergeLeavesEmbeddedSubstringMatchingIntact(t *testing.T) {
	merged := mergeOf(t, Rates, []Rate{{Key: "gpt-9", InPerM: 1, OutPerM: 2}})

	for _, spelling := range []string{
		"anthropic.claude-opus-4-5-v1:0",
		"claude-opus-4-5",
		"claude-opus-4-5-20251101",
	} {
		if got := priceOf(t, merged, spelling); !got.Priced || !approx(got.In, 5) {
			t.Errorf("%s = %+v, want priced at $5/1M", spelling, got)
		}
	}
}

// The published list quotes dollars per token; this table holds dollars per
// million. A price that has not changed still arrives ~1e-17 away from the one
// written here, and compared exactly that is a reprice reported on every boot —
// and a hand-written number quietly replaced by a noisier one.
func TestMergeIgnoresFloatNoiseInAnUnchangedPrice(t *testing.T) {
	embedded := []Rate{{Key: "gpt-5.6-luna", InPerM: 0.2, OutPerM: 1.2}}
	// Exactly what 0.0000002 * 1e6 produces.
	published := []Rate{{Key: "gpt-5.6-luna", InPerM: 0.0000002 * 1e6, OutPerM: 0.0000012 * 1e6}}

	merged, stats := Merge(embedded, published)

	if len(stats.Repriced) != 0 {
		t.Errorf("repriced = %v, want nothing — the price did not change", stats.Repriced)
	}
	if merged[0].InPerM != 0.2 {
		t.Errorf("InPerM = %v, want the hand-written 0.2 left alone", merged[0].InPerM)
	}
}

// ...but a real change, however small a vendor could announce it, still lands.
func TestMergeStillCatchesASmallRealPriceChange(t *testing.T) {
	embedded := []Rate{{Key: "gpt-5.6-luna", InPerM: 0.2, OutPerM: 1.2}}
	published := []Rate{{Key: "gpt-5.6-luna", InPerM: 0.19, OutPerM: 1.2}}

	merged, stats := Merge(embedded, published)

	if len(stats.Repriced) != 1 || merged[0].InPerM != 0.19 {
		t.Errorf("merged = %v / stats = %v, want the $0.19 price to land", merged[0].InPerM, stats.Repriced)
	}
}
