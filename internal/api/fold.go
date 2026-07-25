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
	Port         int      `json:"port"`
	Upstream     string   `json:"upstream"`
	Traced       int      `json:"traced"`
	Cost         float64  `json:"cost"`
	UnpricedReqs int      `json:"unpricedReqs"`
	Tokens       tokens   `json:"tokens"` // lifetime
	Overview     overview `json:"overview"`

	// UnpricedModels names them. A count alone tells you the total is wrong and
	// nothing about how to fix it; the model string is the whole fix — it is the
	// row that has to be added to the rate table. Sorted, so the badge does not
	// reshuffle on every 2s poll.
	UnpricedModels []string `json:"unpricedModels"`

	// The front page is the sessions table. There is no flat request log here any
	// more: a request is not a thing anyone did, and the twenty of them that made
	// up one turn are noise until you have picked the session they belong to. The
	// requests live one level down, inside the trace of the session that made them.
	Sessions []sessionRow `json:"sessions"`

	// Storage is not folded from the rows — it is the state of the capture table
	// and the retention setting governing it, filled in by the handler. It rides
	// on /api/stats because the page already polls it, and a second endpoint for
	// two integers would be a second poll.
	Storage storage `json:"storage"`
}

// storage is what the captures cost and the window that bounds them: the two
// halves of the retention control.
type storage struct {
	CaptureBytes int64  `json:"captureBytes"`
	Retention    string `json:"retention"` // off | 24h | 7d | 30d
}

type overview struct {
	BurnNow   float64  `json:"burnNow"`   // $/hr, extrapolated from the current window
	BurnAvg   float64  `json:"burnAvg"`   // $/hr, lifetime
	TodayCost float64  `json:"todayCost"` // $ since local midnight, priced rows only
	ReqHr     int      `json:"reqHr"`
	WinReqs   int      `json:"winReqs"`
	AvgReq    float64  `json:"avgReq"`
	HitNow    float64  `json:"hitNow"` // cache read ÷ all input tokens
	HitAvg    float64  `json:"hitAvg"`
	PeakMin   float64  `json:"peakMin"`
	WindowMin int      `json:"windowMin"`
	Tokens    tokens   `json:"tokens"` // the current window
	Latency   latency  `json:"latency"`
	Timeline  []bucket `json:"timeline"`

	// What you are paying, right now, for tool schemas nothing has called — summed
	// over the live sessions, at the cadence they are actually running at. The one
	// number on the page that is not a measurement but a claim: this is the money
	// you could stop spending without changing what you are doing.
	WasteHr     float64 `json:"wasteHr"`
	UnusedCount int     `json:"unusedCount"` // …across how many schemas
	WorstSid    string  `json:"worstSid"`    // …and the session shipping the most of them

	// ColdStart says we have been watching for less than one window, which is
	// exactly when BurnAvg's floor (see below) pins it to BurnNow. The ratio
	// between them is then 1 by construction, and any trend read off it — most
	// of all a reassuring one — is an artifact of the arithmetic, not a fact
	// about the spend. The page must not claim a trend it cannot have.
	ColdStart bool `json:"coldStart"`
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
	ID   int64  `json:"id"`
	Time string `json:"time"`

	// Label is the request's own first words — what was ASKED. Op is what came
	// back. Without the ask, a log of three identical `end_turn` rows cannot tell
	// you that one of them was your prompt and the other was Claude Code quietly
	// paying a model to name the session. The store has carried this since v1; it
	// simply never reached the wire.
	Label string `json:"label"`

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
	Shape   shape     `json:"shape"`
	Bytes   byteSplit `json:"bytes"`
	ErrType string    `json:"errType,omitempty"`
	ErrMsg  string    `json:"errMsg,omitempty"`
	Probe   bool      `json:"probe,omitempty"`
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

// shape is where a reply's output tokens went. An estimate — see
// anthropic.SplitOutput — and the only one on the wire.
type shape struct {
	Think int64 `json:"think"`
	Text  int64 `json:"text"`
	Tool  int64 `json:"tool"`
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
func fold(lifetime []store.UsageRow, window []store.Row, tools map[string]toolset, rates []billing.Rate, now time.Time, cfg Config) statsView {
	v := statsView{
		Port:     cfg.Port,
		Upstream: cfg.Upstream,
		Traced:   len(lifetime),
		Overview: overview{
			WindowMin: windowMin,
			Timeline:  make([]bucket, timelineMin),
		},
	}

	// ---- lifetime: total cost, average burn, average cache hit rate ----
	var lifeIn, lifeRead, lifeWrite, lifeOut int64
	oldest := now
	unpriced := map[string]bool{}
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for _, u := range lifetime {
		at := time.UnixMilli(u.TsMs)
		model := billedModel(u.ModelReq, u.ModelServed)
		bill := billing.Compute(rates, model, usageOf(u), at)
		if bill.Priced {
			v.Cost += bill.Total
			if !at.Before(dayStart) {
				v.Overview.TodayCost += bill.Total
			}
		} else {
			v.UnpricedReqs++ // never a silent $0
			if model == "" {
				model = "(unnamed)" // neither the path nor the body named one
			}
			unpriced[model] = true
		}
		lifeIn += u.In
		lifeRead += u.Read
		lifeWrite += u.W5m + u.W1h
		lifeOut += u.Out
		if at.Before(oldest) {
			oldest = at
		}
	}
	v.Tokens = tokens{In: lifeIn, Read: lifeRead, Write: lifeWrite, Out: lifeOut}
	v.UnpricedModels = make([]string, 0, len(unpriced))
	for m := range unpriced {
		v.UnpricedModels = append(v.UnpricedModels, m)
	}
	sort.Strings(v.UnpricedModels)
	v.Overview.HitAvg = hitRate(lifeRead, lifeIn+lifeRead+lifeWrite)
	if len(lifetime) > 0 {
		// Spread lifetime spend over how long we have been watching — but never
		// over less than the window. A fresh database has recorded a burst of
		// requests over a few seconds, and dividing by those seconds extrapolates
		// to a $191/hr "average" that is arithmetically true and completely
		// useless. The floor decays out of the way within the first ten minutes.
		watched := now.Sub(oldest)
		hours := max(watched, windowMin*time.Minute).Hours()
		v.Overview.BurnAvg = v.Cost / hours
		v.Overview.ColdStart = watched < windowMin*time.Minute
	}

	// ---- timeline: 60 one-minute buckets, oldest first ----
	firstMinute := now.Truncate(time.Minute).Add(-(timelineMin - 1) * time.Minute)
	for i := range v.Overview.Timeline {
		v.Overview.Timeline[i].T = firstMinute.Add(time.Duration(i) * time.Minute).UnixMilli()
	}

	// ---- window: burn now, hit now, latency, per-minute stacking ----
	winStart := now.Add(-windowMin * time.Minute)
	var winCost float64
	var winIn, winRead, winWrite, winOut int64
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
			if r.Status >= 400 && !benignErr(r) {
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
		winOut += u.Out
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
	v.Overview.Tokens = tokens{In: winIn, Read: winRead, Write: winWrite, Out: winOut}
	v.Overview.HitNow = hitRate(winRead, winIn+winRead+winWrite)
	v.Overview.Latency = latency{P50Ttft: percentile(ttfts, 0.50), P95Ttft: percentile(ttfts, 0.95)}

	// ---- the sessions table, and the waste it is shipping ----
	v.Sessions = foldSessions(lifetime, tools, rates, now)

	// "Could have saved" counts LIVE sessions only, and deliberately. A schema that
	// went uncalled in a session that ended two hours ago is not money you can stop
	// spending — it is money already gone. Advice you cannot act on, priced to the
	// cent, is the fastest way to teach someone to ignore the number.
	worst := int64(0)
	for _, s := range v.Sessions {
		if !s.Live || s.Unused == 0 {
			continue
		}
		v.Overview.WasteHr += s.WasteHr
		v.Overview.UnusedCount += s.Unused
		if s.UnusedBytes > worst {
			worst, v.Overview.WorstSid = s.UnusedBytes, s.ID
		}
	}
	return v
}

// rowView projects one fact row into the wire row the trace's list and charts
// draw. The trace is the only thing that carries request rows now, so this is
// the one place a request becomes JSON.
func rowView(r store.Row, rates []billing.Rate) recentRow {
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

	return recentRow{
		ID:      r.ID,
		Time:    at.Format(time.RFC3339),
		Label:   r.Label,
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
		Shape:   shape{Think: r.ThinkTokens, Text: r.TextTokens, Tool: r.ToolTokens},
		Bytes: byteSplit{
			Total:    r.TotalBytes,
			Tools:    r.ToolsBytes,
			System:   r.SystemBytes,
			Messages: r.MessagesBytes,
		},
		ErrType: r.ErrType,
		ErrMsg:  r.ErrMsg,
		Probe:   isProbe(r),
	}
}

// isProbe reports whether a row is a client asking a question about the account
// rather than asking the model for anything.
//
// Claude Code opens every session with `{"max_tokens":1,"messages":[{"role":
// "user","content":"quota"}]}`. Anthropic answers a depleted quota with a 429,
// the client reads that as its answer and carries on, and nothing about the
// exchange is a failure. Counted as an error it is worse than noise: it puts a
// red row and a non-zero error rate on the dashboard at the start of every
// single session, which is precisely how a real error learns to look normal.
//
// max_tokens:1 with no tools is the test, because it is the *semantics* of a
// probe — a caller that wanted an answer would leave room for one. Matching the
// literal string "quota" would fit this month's client and quietly stop working
// on the next one. A row from before the max_tokens column existed has 0 here
// and is never a probe, which is the safe way to be wrong: it shows an error
// that isn't one, rather than hiding one that is.
func isProbe(r store.Row) bool {
	return r.MaxTokens == 1 && r.ToolCount == 0
}

// benignErr reports a 4xx that is not a failure: the 429 the API answers a quota
// probe with, which is the probe working exactly as intended.
//
// The narrowness is the point. Being a probe does not make a request's errors
// uninteresting — a 401 there means the key is dead and a 500 means the upstream
// is down, and those are the first things you would want a startup ping to tell
// you. Only the 429 is an answer rather than a fault, so only the 429 is forgiven.
func benignErr(r store.Row) bool {
	return isProbe(r) && r.Status == 429
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
