package api

import (
	"sort"
	"time"

	"github.com/guydelarea/tokentracer/internal/billing"
	"github.com/guydelarea/tokentracer/internal/store"
)

// The history view: what this has cost over time, a local day at a time.
//
// The day is the largest bucket the server folds, and deliberately so. A month
// is the sum of days that were each priced one request at a time, and summing
// already-priced days IS additive — so the page rolls months up itself rather
// than the server shipping the same money twice at two grains.
//
// What is not negotiable is the order of those two steps. Pricing is
// non-additive: the long-context tier depends on a request's own size and rates
// depend on its own timestamp, so every row is priced at its own instant and
// only then added to its day. A SUM … GROUP BY date would be fast and wrong —
// see the package comment on store/queries.go.

// historyView is the /api/history contract. These json tags ARE the wire format.
type historyView struct {
	Days []dayBucket `json:"days"`
}

// dayBucket is one local day of spend.
//
// Only days that recorded something get a bucket — plus today, always, so the
// range the page counts back from has an end it did not have to infer. The gaps
// between buckets are days nobody worked, which the page draws as the $0 they
// were.
type dayBucket struct {
	T        int64  `json:"t"` // unix ms at local midnight
	Cost     costs  `json:"cost"`
	Tok      tokens `json:"tok"`
	N        int    `json:"n"`
	Err      int    `json:"err"`
	Sessions int    `json:"sessions"` // distinct session ids; the unnamed ones count as one
	Unpriced int    `json:"unpriced"` // rows with no rate for their model — never a silent $0

	// Models is the day's spend per billed model. Unpriced rows are absent by
	// construction: there is no number to put against them.
	Models map[string]float64 `json:"models"`
}

// foldHistory buckets the lifetime scan by local day. Pure, like fold(): `now`
// is a parameter, so the day boundaries a test asserts on are the ones it chose.
func foldHistory(lifetime []store.UsageRow, rates []billing.Rate, now time.Time) historyView {
	buckets := map[int64]*dayBucket{}
	sids := map[int64]map[string]bool{}
	var order []int64

	// Local, always: the rows are bucketed by the day the person had, not by UTC.
	day := func(at time.Time) *dayBucket {
		at = at.Local()
		t := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location()).UnixMilli()
		b := buckets[t]
		if b == nil {
			b = &dayBucket{T: t, Models: map[string]float64{}}
			buckets[t] = b
			sids[t] = map[string]bool{}
			order = append(order, t)
		}
		return b
	}
	// Today has a bucket even when nothing has run on it. A missing one reads as
	// a day that never happened rather than one that cost nothing.
	day(now)

	for _, u := range lifetime {
		at := time.UnixMilli(u.TsMs)
		b := day(at)

		b.N++
		b.Tok.In += u.In
		b.Tok.Read += u.Read
		b.Tok.Write += u.W5m + u.W1h
		b.Tok.Out += u.Out
		sids[b.T][u.SessionID] = true
		// The same forgiveness the rest of the fold gives: a probe's 429 is the
		// answer to it, not a failure of it.
		if u.Status >= 400 && !benignUsage(u) {
			b.Err++
		}

		model := billedModel(u.ModelReq, u.ModelServed)
		bill := billing.Compute(rates, model, usageOf(u), at)
		if !bill.Priced {
			b.Unpriced++ // never a silent $0
			continue
		}
		b.Cost.In += bill.In
		b.Cost.Read += bill.Read
		b.Cost.Write += bill.Write
		b.Cost.Out += bill.Out
		if bill.Total > 0 {
			// Only what actually cost something. An error bills nothing, so a model
			// that spent the day failing would otherwise take a $0.00 row in a
			// ranking of spend — a figure that says "this was free" about something
			// that was not, which is the one number this page will not print.
			b.Models[model] += bill.Total
		}
	}

	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]dayBucket, 0, len(order))
	for _, t := range order {
		b := buckets[t]
		b.Sessions = len(sids[t])
		out = append(out, *b)
	}
	return historyView{Days: out}
}
