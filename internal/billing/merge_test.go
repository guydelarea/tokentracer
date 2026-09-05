package billing

import (
	"math"
	"strings"
	"testing"
	"time"
)

var epoch = time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)

func priceOf(t *testing.T, rates []Rate, model string) Bill {
	t.Helper()
	return Compute(rates, model, Usage{In: 1_000_000}, epoch)
}

// The rule, stated as a test: a fetched row may not move a price a human
// verified. The registry agreed with the hand-verified table on the day this was
// written, and the point of the rule is that it does not have to keep agreeing.
func TestMergeEmbeddedPriceAlwaysWins(t *testing.T) {
	embedded := []Rate{{Key: "claude-opus-4-5", InPerM: 5, OutPerM: 25}}
	fetched := []Rate{{Key: "claude-opus-4-5", InPerM: 15, OutPerM: 75}}

	got := priceOf(t, Merge(embedded, fetched), "claude-opus-4-5")
	if got.In != 5 {
		t.Errorf("in = $%v/1M, want $5 — the fetched list overrode a verified price", got.In)
	}
}

// Commit a27c007 stripped the sonnet-4 family's above-200k tiers as a beta that
// no longer exists. The registry still publishes them. This is the test that the
// correction survives a refresh — under "fetched wins" it would be undone on
// every boot, and would have to be made again after each one.
func TestMergeCannotReintroduceARemovedLongContextTier(t *testing.T) {
	fetched := []Rate{{
		Key: "claude-sonnet-4-5-20250929", InPerM: 3, OutPerM: 15,
		LongCtxThreshold: 200_000, LongCtxInPerM: 6, LongCtxOutPerM: 22.5,
	}}
	merged := Merge(Rates, fetched)

	// Well past the tier the registry would have applied: at the standard rate
	// this is $0.90, at the tier's $6/1M it would be $1.80.
	over := Compute(merged, "claude-sonnet-4-5-20250929", Usage{In: 300_000}, epoch)
	if want := 300_000.0 / 1e6 * 3; math.Abs(over.In-want) > 1e-9 {
		t.Errorf("in = $%v, want $%v — a long-context tier came back through the fetch", over.In, want)
	}
}

// A model the embedded table has no opinion about is exactly what the refresh is
// for: UNPRICED before, priced after.
func TestMergeFillsAHole(t *testing.T) {
	const model = "claude-neue-9-20270101"

	if priceOf(t, Rates, model).Priced {
		t.Fatalf("%s is already priced — pick a model the embedded table does not cover", model)
	}
	got := priceOf(t, Merge(Rates, []Rate{{Key: model, InPerM: 4, OutPerM: 20}}), model)
	if !got.Priced || got.In != 4 {
		t.Errorf("bill = %+v, want priced at $4/1M", got)
	}
}

// The whole reason fetched rows are exact-matched. The registry publishes bare
// family keys — "gpt-4", "gpt-5", "gpt-4.1" — and as substring keys they would
// price an unreleased sibling at the family's rate. rates.go bans exactly this
// for hand-written rows; a fetch must not smuggle it back in.
func TestMergedFetchedKeyDoesNotSwallowAnUnreleasedSibling(t *testing.T) {
	merged := Merge(Rates, []Rate{{Key: "gpt-4", InPerM: 30, OutPerM: 60}})

	if got := priceOf(t, merged, "gpt-4"); !got.Priced {
		t.Error("gpt-4 itself came out unpriced — the fetched row should cover its own name")
	}
	if got := priceOf(t, merged, "gpt-4-whatever-ships-next"); got.Priced {
		t.Errorf("gpt-4-whatever-ships-next priced at $%v/1M, want UNPRICED — "+
			"a bare family key from the fetch swallowed a model nobody priced", got.In)
	}
}

// A gateway puts its route in front of the model, and that prefix reaches the row
// whenever the response never named the model it served (see the LiteLLM e2e
// case, where ModelReq is "anthropic/claude-sonnet-5"). Exact matching still has
// to find the model inside it.
func TestMergedFetchedKeyMatchesThroughARoutePrefix(t *testing.T) {
	merged := Merge(Rates, []Rate{{Key: "claude-neue-9", InPerM: 4, OutPerM: 20}})

	if got := priceOf(t, merged, "anthropic/claude-neue-9"); !got.Priced || got.In != 4 {
		t.Errorf("bill = %+v, want priced at $4/1M through the route prefix", got)
	}
	// ...but the prefix is not a licence to match loosely.
	if got := priceOf(t, merged, "anthropic/claude-neue-9-turbo"); got.Priced {
		t.Errorf("claude-neue-9-turbo priced at $%v/1M, want UNPRICED", got.In)
	}
}

// Merge owns the exact-matching guarantee, so it must hold even for a caller that
// hands over substring rows.
func TestMergeForcesExactOnEveryFetchedRow(t *testing.T) {
	merged := Merge(nil, []Rate{{Key: "gpt-9", InPerM: 1, OutPerM: 2, Exact: false}})

	if len(merged) != 1 || !merged[0].Exact {
		t.Fatalf("merged = %+v, want one row with Exact set", merged)
	}
}

// A refresh that returns nothing — disabled, failed, or filtered to empty — must
// leave the running table exactly as it was.
func TestMergeWithNoFetchChangesNothing(t *testing.T) {
	merged := Merge(Rates, nil)

	if len(merged) != len(Rates) {
		t.Fatalf("merged = %d rows, want the embedded %d", len(merged), len(Rates))
	}
	for i := range Rates {
		if merged[i] != Rates[i] {
			t.Errorf("row %d = %+v, want %+v", i, merged[i], Rates[i])
		}
	}
}

// The seed table's ordering invariant, re-asserted on a merged table: fetched
// rows are appended after every embedded row, so they can never shadow one, and
// being exact they cannot shadow each other either.
func TestMergedTableKeepsTheOrderingInvariant(t *testing.T) {
	fetched := []Rate{
		{Key: "gpt-4", InPerM: 30, OutPerM: 60},
		{Key: "claude-neue-9", InPerM: 4, OutPerM: 20},
		{Key: "gpt-9", InPerM: 1, OutPerM: 2},
	}
	merged := Merge(Rates, fetched)

	for i, r := range merged {
		if i < len(Rates) {
			if r.Exact {
				t.Errorf("merged[%d] %q is an embedded row but became Exact", i, r.Key)
			}
			continue
		}
		if !r.Exact {
			t.Errorf("merged[%d] %q is a fetched row that is not Exact", i, r.Key)
		}
		// A fetched row that is not exact would be shadowed by, or would shadow,
		// an embedded key. Exact rows are immune, which is what makes appending safe.
		for j := 0; j < len(Rates); j++ {
			if strings.Contains(r.Key, Rates[j].Key) && !r.Exact {
				t.Errorf("merged[%d] %q is shadowed by the embedded %q", i, r.Key, Rates[j].Key)
			}
		}
	}
}

// Substring matching is what makes one hand-written key cover the Bedrock and
// Vertex spellings of a model. Merging must not quietly narrow it.
func TestMergeLeavesEmbeddedSubstringMatchingIntact(t *testing.T) {
	merged := Merge(Rates, []Rate{{Key: "gpt-9", InPerM: 1, OutPerM: 2}})

	for _, spelling := range []string{
		"anthropic.claude-opus-4-5-v1:0",
		"claude-opus-4-5",
		"claude-opus-4-5-20251101",
	} {
		if got := priceOf(t, merged, spelling); !got.Priced || got.In != 5 {
			t.Errorf("%s = %+v, want priced at $5/1M", spelling, got)
		}
	}
}
