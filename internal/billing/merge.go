package billing

import (
	"math"
	"sort"
)

// MergeStats is what a merge actually changed, so the startup screen can say it
// out loud. A price that moves under the user rewrites every historical cost
// this table has ever produced — that is the point of pricing at read time, and
// exactly why it must never happen quietly.
type MergeStats struct {
	// Repriced are the embedded keys whose rate the published list moved.
	Repriced []string

	// Filled are the models the embedded table had no rate for at all.
	Filled []string
}

// Merge brings an embedded price table up to date from a published one.
//
// Two things happen, and they are different:
//
//   - REPRICE. An embedded row whose model the list also prices takes the
//     published numbers. This is what makes a vendor's price change land without
//     a release, and because cost is computed at read time it corrects history
//     as well as new traffic — the same property that lets a corrected rate table
//     fix the past instead of rewriting it.
//   - FILL. A model no embedded key covers is appended as a new row, matched on
//     its exact name.
//
// What a published list may never do is decide how this table is KEYED. A
// reprice moves numbers onto a hand-written row and touches nothing else: not
// the key, not its matching mode, not its rate window, and not whether the model
// has a long-context tier. The registry publishes bare family keys ("gpt-4",
// "gpt-5") and long-context tiers that were deliberately deleted from rates.go,
// and neither can arrive through here.
//
// Order is preserved and load-bearing. Embedded rows keep their most-specific-
// first sort and are scanned first, so the first-hit-wins loop in match reaches
// a filled row only for a model no embedded key covers. Among filled rows order
// cannot matter, because an exact key can never swallow another.
func Merge(embedded, fetched []Rate) ([]Rate, MergeStats) {
	published := make(map[string]Rate, len(fetched))
	for _, f := range fetched {
		published[normalize(f.Key)] = f
	}

	var stats MergeStats
	out := make([]Rate, 0, len(embedded)+len(fetched))
	for _, e := range embedded {
		// Key equality, not the substring rule. "Which published row is this row
		// about" has to have one answer, and half the list contains the key
		// "claude-opus-4-5".
		p, ok := published[normalize(e.Key)]
		if !ok {
			out = append(out, e)
			continue
		}
		r, moved := reprice(e, p)
		if moved {
			stats.Repriced = append(stats.Repriced, e.Key)
		}
		out = append(out, r)
	}

	filled := make([]Rate, 0, len(fetched))
	for _, f := range fetched {
		// Stored normalized, because match compares against a normalized model
		// name. Every key in the published list is already in this form, so today
		// this changes nothing — but a key that one day is not would otherwise
		// never match anything, and would do it silently.
		f.Key = normalize(f.Key)
		if f.Key == "" || pricedByAny(embedded, f.Key) {
			continue
		}
		// Set here rather than trusted from the caller. The guarantee that a filled
		// key never matches as a substring belongs to this seam, because this is the
		// one place every fetched row has to pass through.
		f.Exact = true
		filled = append(filled, f)
	}
	sort.Slice(filled, func(i, j int) bool { return filled[i].Key < filled[j].Key })
	for _, f := range filled {
		stats.Filled = append(stats.Filled, f.Key)
	}

	return append(out, filled...), stats
}

// reprice moves an embedded row onto the published price, and moves nothing else.
//
// The long-context tier is refreshed only when the embedded row ALREADY declares
// one. Whether a model has a premium tier is a hand-verified fact, and the two
// ways of getting it wrong are not symmetric:
//
//   - Adding a tier would undo a27c007, which stripped the sonnet-4 family's
//     above-200k rows as a beta that no longer exists. The list still publishes
//     them, so every boot would put them back.
//   - Dropping a tier would silently under-bill GPT-5.6 above 272k, which is a
//     tier this table verified and the request can genuinely reach.
//
// So the tier's EXISTENCE is never read from the list; only its numbers are.
// It reports whether anything actually moved, and leaves the hand-written number
// in place when nothing did — see moved.
func reprice(e, published Rate) (Rate, bool) {
	changed := false
	if moved(e.InPerM, published.InPerM) {
		e.InPerM, changed = published.InPerM, true
	}
	if moved(e.OutPerM, published.OutPerM) {
		e.OutPerM, changed = published.OutPerM, true
	}
	if e.LongCtxThreshold > 0 && published.LongCtxThreshold > 0 {
		if e.LongCtxThreshold != published.LongCtxThreshold ||
			moved(e.LongCtxInPerM, published.LongCtxInPerM) ||
			moved(e.LongCtxOutPerM, published.LongCtxOutPerM) {
			e.LongCtxThreshold = published.LongCtxThreshold
			e.LongCtxInPerM = published.LongCtxInPerM
			e.LongCtxOutPerM = published.LongCtxOutPerM
			changed = true
		}
	}
	return e, changed
}

// moved reports whether a published price differs from a written one by more
// than float noise.
//
// The list quotes dollars per TOKEN and this table holds dollars per MILLION, so
// a price that has not changed in months still arrives a few times 1e-17 away
// from the number written here: 0.2 comes back as 0.19999999999999998. Compared
// exactly, that is a reprice on every boot, reported to the user and written
// into the table. Published prices carry four significant decimals at most, so
// 1e-9 is far below any change a vendor can announce and far above the noise.
func moved(a, b float64) bool { return math.Abs(a-b) > 1e-9 }

// pricedByAny reports whether some embedded key already covers this model name.
//
// Rate windows are ignored on purpose. The question here is "does the table have
// an opinion about this model", and an expired row is still an opinion — so
// deferring to it leaves the model UNPRICED rather than quietly handing it to a
// published price. That is the safe direction, and the one a reader of rates.go
// would expect.
func pricedByAny(embedded []Rate, model string) bool {
	m := normalize(model)
	for _, e := range embedded {
		if e.matches(m) {
			return true
		}
	}
	return false
}
