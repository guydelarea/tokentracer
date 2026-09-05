package billing

import "sort"

// Merge layers a fetched price list underneath the embedded one.
//
// The rule, and the whole reason this is a function rather than an append:
// EMBEDDED ROWS ALWAYS WIN. A fetched row survives only when no embedded row
// would have priced that model at all, and it is forced to exact matching on the
// way in. A fetch can therefore turn an UNPRICED model into a priced one; it can
// never move a price someone verified against a vendor page, and it cannot
// reintroduce a tier that was deliberately taken out of the table.
//
// That last clause is not caution for its own sake. The registry these rows come
// from still publishes above-200k tiers for the sonnet-4 family that rates.go
// dropped as a beta that no longer exists. Under "fetched wins" every boot would
// restore them, and the correction would have to be made again after each one.
//
// Order is preserved and load-bearing. Embedded rows keep their most-specific-
// first sort and are scanned first, so the first-hit-wins loop in match reaches a
// fetched row only for a model no embedded key covers. Among fetched rows order
// cannot matter, because an exact key can never swallow another.
func Merge(embedded, fetched []Rate) []Rate {
	filled := make([]Rate, 0, len(fetched))
	for _, f := range fetched {
		if f.Key == "" || pricedByAny(embedded, f.Key) {
			continue
		}
		// Set here rather than trusted from the caller. The guarantee that a fetched
		// key never matches as a substring belongs to this seam, because this is the
		// one place every fetched row has to pass through.
		f.Exact = true
		filled = append(filled, f)
	}
	sort.Slice(filled, func(i, j int) bool { return filled[i].Key < filled[j].Key })

	out := make([]Rate, 0, len(embedded)+len(filled))
	out = append(out, embedded...)
	return append(out, filled...)
}

// pricedByAny reports whether some embedded key already covers this model name.
//
// Rate windows are ignored on purpose. The question here is "does the table have
// an opinion about this model", and an expired row is still an opinion — so
// deferring to it leaves the model UNPRICED rather than quietly handing it to a
// fetched price. That is the safe direction, and the one a reader of rates.go
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
