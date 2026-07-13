package api

import (
	"sort"
	"time"

	"github.com/guydelarea/tokentracer/internal/billing"
	"github.com/guydelarea/tokentracer/internal/store"
)

// windowMin is the "now" window: what burn/hit/latency mean by "current".
const windowMin = 10

// timelineMin is the spend timeline: 60 one-minute buckets.
const timelineMin = 60

// statsView is the /api/stats contract. These json tags ARE the wire format —
// there is no translation layer, so the two cannot drift.
type statsView struct {
	Port         int         `json:"port"`
	Upstream     string      `json:"upstream"`
	Traced       int         `json:"traced"`
	Cost         float64     `json:"cost"`
	UnpricedReqs int         `json:"unpricedReqs"`
	Overview     overview    `json:"overview"`
	Recent       []recentRow `json:"recent"`
}

type overview struct {
	BurnNow   float64  `json:"burnNow"` // $/hr, extrapolated from the current window
	BurnAvg   float64  `json:"burnAvg"` // $/hr, lifetime
	ReqHr     int      `json:"reqHr"`
	WinReqs   int      `json:"winReqs"`
	AvgReq    float64  `json:"avgReq"`
	HitNow    float64  `json:"hitNow"` // cache read ÷ all input tokens
	HitAvg    float64  `json:"hitAvg"`
	PeakMin   float64  `json:"peakMin"`
	WindowMin int      `json:"windowMin"`
	Latency   latency  `json:"latency"`
	Timeline  []bucket `json:"timeline"`
}

type latency struct {
	P50Ttft int64 `json:"p50Ttft"`
	P95Ttft int64 `json:"p95Ttft"`
}

// bucket is one minute of the timeline: tokens by class, cost by class.
type bucket struct {
	T          int64   `json:"t"` // unix ms at the start of the minute
	N          int     `json:"n"`
	Input      int64   `json:"input"`
	CacheRead  int64   `json:"cacheRead"`
	CacheWrite int64   `json:"cacheWrite"`
	Output     int64   `json:"output"`
	CostIn     float64 `json:"costIn"`
	CostRead   float64 `json:"costRead"`
	CostWrite  float64 `json:"costWrite"`
	CostOut    float64 `json:"costOut"`
	Err        int     `json:"err"`
}

type recentRow struct {
	ID      int64     `json:"id"`
	Time    string    `json:"time"`
	Model   string    `json:"model"`
	Sid     string    `json:"sid"`
	Op      string    `json:"op"`
	Status  int       `json:"status"`
	Ms      int64     `json:"ms"`
	Ttft    int64     `json:"ttft"`
	Stop    string    `json:"stop"`
	Aborted bool      `json:"aborted"`
	Tok     tokens    `json:"tok"`
	Cost    costs     `json:"cost"`
	Priced  bool      `json:"priced"`
	Bytes   byteSplit `json:"bytes"`
	ErrType string    `json:"errType,omitempty"`
	ErrMsg  string    `json:"errMsg,omitempty"`
}

type tokens struct {
	In    int64 `json:"in"`
	Read  int64 `json:"read"`
	Write int64 `json:"write"`
	Out   int64 `json:"out"`
}

type costs struct {
	In    float64 `json:"in"`
	Read  float64 `json:"read"`
	Write float64 `json:"write"`
	Out   float64 `json:"out"`
}

type byteSplit struct {
	Total    int64 `json:"total"`
	Tools    int64 `json:"tools"`
	System   int64 `json:"system"`
	Messages int64 `json:"messages"`
}

// fold turns facts and rates into every number the dashboard shows. It is pure:
// `now` is a parameter, so bucket boundaries are testable, and nothing here
// reads a clock or a database.
//
// Every row is priced individually and only then summed. It cannot be done in
// SQL: the long-context tier means a request's rate depends on its own size, and
// time-windowed rates mean it depends on its own timestamp. A SUM … GROUP BY
// destroys exactly the facts that decide the price.
//
// ponytail: the lifetime pass is a full scan repriced on every 2s poll —
// milliseconds at v1 scale (a heavy year is ~10^5 rows). When it stops being
// free, cache the running totals keyed by max rowid: rows are immutable and
// append-only, and a rate-table change only happens at restart, which resets the
// cache anyway.
func fold(lifetime []store.UsageRow, window, recent []store.Row, rates []billing.Rate, now time.Time, cfg Config) statsView {
	v := statsView{
		Port:     cfg.Port,
		Upstream: cfg.Upstream,
		Traced:   len(lifetime),
		Recent:   make([]recentRow, 0, len(recent)),
		Overview: overview{
			WindowMin: windowMin,
			Timeline:  make([]bucket, timelineMin),
		},
	}

	// ---- lifetime: total cost, average burn, average cache hit rate ----
	var lifeIn, lifeRead, lifeWrite int64
	oldest := now
	for _, u := range lifetime {
		at := time.UnixMilli(u.TsMs)
		bill := billing.Compute(rates, billedModel(u.ModelReq, u.ModelServed), usageOf(u), at)
		if bill.Priced {
			v.Cost += bill.Total
		} else {
			v.UnpricedReqs++ // never a silent $0
		}
		lifeIn += u.In
		lifeRead += u.Read
		lifeWrite += u.W5m + u.W1h
		if at.Before(oldest) {
			oldest = at
		}
	}
	v.Overview.HitAvg = hitRate(lifeRead, lifeIn+lifeRead+lifeWrite)
	if hours := now.Sub(oldest).Hours(); hours > 0 && len(lifetime) > 0 {
		v.Overview.BurnAvg = v.Cost / hours
	}

	// ---- timeline: 60 one-minute buckets, oldest first ----
	firstMinute := now.Truncate(time.Minute).Add(-(timelineMin - 1) * time.Minute)
	for i := range v.Overview.Timeline {
		v.Overview.Timeline[i].T = firstMinute.Add(time.Duration(i) * time.Minute).UnixMilli()
	}

	// ---- window: burn now, hit now, latency, per-minute stacking ----
	winStart := now.Add(-windowMin * time.Minute)
	var winCost float64
	var winIn, winRead, winWrite int64
	var ttfts []int64

	for _, r := range window {
		at := time.UnixMilli(r.TsMs)
		u := billing.Usage{
			In:      deref(r.InputTokens),
			Read:    deref(r.CacheReadTokens),
			Write5m: deref(r.CacheW5mTokens),
			Write1h: deref(r.CacheW1hTokens),
			Out:     deref(r.OutputTokens),
		}
		bill := billing.Compute(rates, billedModel(r.ModelReq, r.ModelServed), u, at)

		if idx := int(at.Truncate(time.Minute).Sub(firstMinute) / time.Minute); idx >= 0 && idx < timelineMin {
			b := &v.Overview.Timeline[idx]
			b.N++
			b.Input += u.In
			b.CacheRead += u.Read
			b.CacheWrite += u.Write5m + u.Write1h
			b.Output += u.Out
			b.CostIn += bill.In
			b.CostRead += bill.Read
			b.CostWrite += bill.Write
			b.CostOut += bill.Out
			if r.Status >= 400 {
				b.Err++
			}
		}

		if at.Before(winStart) {
			continue
		}
		v.Overview.WinReqs++
		winCost += bill.Total
		winIn += u.In
		winRead += u.Read
		winWrite += u.Write5m + u.Write1h
		if r.TtftMs > 0 {
			ttfts = append(ttfts, r.TtftMs)
		}
	}

	for _, b := range v.Overview.Timeline {
		if cost := b.CostIn + b.CostRead + b.CostWrite + b.CostOut; cost > v.Overview.PeakMin {
			v.Overview.PeakMin = cost
		}
	}

	perHour := 60.0 / windowMin
	v.Overview.BurnNow = winCost * perHour
	v.Overview.ReqHr = int(float64(v.Overview.WinReqs) * perHour)
	if v.Overview.WinReqs > 0 {
		v.Overview.AvgReq = winCost / float64(v.Overview.WinReqs)
	}
	v.Overview.HitNow = hitRate(winRead, winIn+winRead+winWrite)
	v.Overview.Latency = latency{P50Ttft: percentile(ttfts, 0.50), P95Ttft: percentile(ttfts, 0.95)}

	// ---- the request log ----
	for _, r := range recent {
		at := time.UnixMilli(r.TsMs)
		u := billing.Usage{
			In:      deref(r.InputTokens),
			Read:    deref(r.CacheReadTokens),
			Write5m: deref(r.CacheW5mTokens),
			Write1h: deref(r.CacheW1hTokens),
			Out:     deref(r.OutputTokens),
		}
		model := billedModel(r.ModelReq, r.ModelServed)
		bill := billing.Compute(rates, model, u, at)

		v.Recent = append(v.Recent, recentRow{
			ID:      r.ID,
			Time:    at.Format(time.RFC3339),
			Model:   model,
			Sid:     r.SessionID,
			Op:      r.Op,
			Status:  r.Status,
			Ms:      r.DurationMs,
			Ttft:    r.TtftMs,
			Stop:    r.StopReason,
			Aborted: r.Aborted,
			Tok:     tokens{In: u.In, Read: u.Read, Write: u.Write5m + u.Write1h, Out: u.Out},
			Cost:    costs{In: bill.In, Read: bill.Read, Write: bill.Write, Out: bill.Out},
			Priced:  bill.Priced,
			Bytes: byteSplit{
				Total:    r.TotalBytes,
				Tools:    r.ToolsBytes,
				System:   r.SystemBytes,
				Messages: r.MessagesBytes,
			},
			ErrType: r.ErrType,
			ErrMsg:  r.ErrMsg,
		})
	}
	return v
}

// billedModel is the model the money is actually charged against: the one that
// served the request, when the response told us, and the requested one otherwise.
//
// These are not always the same string, and the difference is exactly where a
// silent mispricing hides — an alias served by a model we have no rate for must
// come back UNPRICED, not quietly billed at the rate of whatever was asked for.
// The row displays the same model it was priced against, so the number and the
// name on screen can never disagree.
func billedModel(requested, served string) string {
	if served != "" {
		return served
	}
	return requested
}

func usageOf(u store.UsageRow) billing.Usage {
	return billing.Usage{In: u.In, Read: u.Read, Write5m: u.W5m, Write1h: u.W1h, Out: u.Out}
}

// hitRate is cache reads over everything that could have been a cache read.
func hitRate(read, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(read) / float64(total)
}

// percentile uses nearest-rank on a sorted copy. n is tiny (one window of
// requests), so the sort is free and the exactness is worth more than cleverness.
func percentile(vals []int64, p float64) int64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]int64(nil), vals...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })

	rank := int(float64(len(s))*p+0.5) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(s) {
		rank = len(s) - 1
	}
	return s[rank]
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
