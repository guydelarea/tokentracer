package api

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/guydelarea/tokentracer/internal/anthropic"
	"github.com/guydelarea/tokentracer/internal/billing"
	"github.com/guydelarea/tokentracer/internal/store"
)

// The sessions table: the dashboard's front page, and the unit of work a person
// actually thinks in. A request is not a thing anyone did; a session is.
//
// It aggregates over every request ever recorded, not a recent window — a
// session that started this morning is still the one on screen — and it prices
// every row individually before summing, for the same reason fold() does: rates
// are time-windowed and the long-context tier is a per-request fact. A session
// total that disagreed with the overview's total would discredit both.

// liveWindow is how recently a session must have spoken to count as live. Past
// it, the row shows how long it has been quiet instead.
const liveWindow = 90 * time.Second

// noSessionID is the bucket a request lands in when its client never named a
// session. It is a display label, not an id: the rows themselves carry "", and
// dbSid maps it back before anything goes near a query. A request nobody can
// name still happened and still cost money, so it gets a row rather than a
// silent drop.
const noSessionID = "(no session id)"

// dbSid maps the display bucket back to the id the rows actually carry.
func dbSid(sid string) string {
	if sid == noSessionID {
		return ""
	}
	return sid
}

// statelessMinReqs is how many times a session must have spoken before "never
// cached" means anything. The first request cannot hit a cache and the second
// may be a probe; by the third, a session with no cache reads at all is telling
// you something — either the client sends no cache_control, or something dynamic
// at the head of the prompt poisons the prefix on every single call.
const statelessMinReqs = 3

// sessionRow is one row of the sessions table.
type sessionRow struct {
	ID    string `json:"id"`
	Label string `json:"label"` // what was asked first — the session's name
	Model string `json:"model"`

	Live bool   `json:"live"`
	Idle string `json:"idle"` // "12s", "4m", "2h" — how long it has been quiet
	Last string `json:"last"`

	Req int `json:"req"`
	Err int `json:"err"`

	Tok  tokens  `json:"tok"`
	Cost float64 `json:"cost"`

	// RateHr is the session's spend in the overview window, extrapolated to an
	// hour: what this session is costing you *now*, as opposed to what it has
	// cost. A finished session's rate is zero, which is correct and is why the
	// column reads "—" rather than its lifetime average.
	RateHr float64 `json:"rateHr"`
	Hit    float64 `json:"hit"`

	// Schemas shipped on every request and never once called. The bytes ride
	// inside the cached prefix, so they bill at the cache-read rate — cheap per
	// request, and paid again on every request for as long as the session lives.
	Unused      int     `json:"unused"`
	UnusedTok   int64   `json:"unusedTok"`
	UnusedBytes int64   `json:"unusedBytes"`
	WasteHr     float64 `json:"wasteHr"`

	Priced        bool  `json:"priced"`
	Stateless     bool  `json:"stateless"`
	ContextWindow int64 `json:"contextWindow"`

	// Agents is how many subagent sessions were folded into this row. A subagent
	// is not a thing anyone did either — it is part of the session that spawned
	// it, so its spend lands here rather than on a row of its own.
	Agents int `json:"agents"`
}

// toolset is a session's tool schemas, as of its latest request that shipped
// any. Resolved by the server from a capture (see toolsFor) and passed into the
// fold, which stays pure.
type toolset struct {
	Items []anthropic.ToolItem
}

// sessionAgg accumulates one session across the lifetime scan.
type sessionAgg struct {
	id, label, model string
	first, last      time.Time
	req, err         int
	tok              tokens
	cost             float64
	priced           bool
	called           map[string]bool // tools this session actually invoked
	winCost          float64         // spend inside the overview window
	winReqs          int
	parent           string // the session that spawned this one, "" for top-level
	agents           int    // subagent sessions folded into this row
}

// foldSessions folds the lifetime scan into the sessions table, newest activity
// first. tools carries each session's schemas; a session with none simply shows
// no waste.
func foldSessions(lifetime []store.UsageRow, tools map[string]toolset, rates []billing.Rate, now time.Time) []sessionRow {
	winStart := now.Add(-windowMin * time.Minute)

	agg := map[string]*sessionAgg{}
	var order []string // first-seen, so the sort below is stable against equal clocks

	for _, u := range lifetime {
		at := time.UnixMilli(u.TsMs)
		sid := u.SessionID
		if sid == "" {
			sid = noSessionID
		}

		a := agg[sid]
		if a == nil {
			a = &sessionAgg{id: sid, first: at, called: map[string]bool{}}
			agg[sid] = a
			order = append(order, sid)
		}
		// The session's name is the first thing actually ASKED of the model — and a
		// probe asks nothing. Claude Code opens every session with a max_tokens:1
		// ping whose text is the word "quota", so without this every session on the
		// page is called "quota" and the table names nothing at all.
		if a.label == "" && !probeUsage(u) {
			a.label = u.Label
		}
		if a.parent == "" && u.ParentSid != "" {
			a.parent = u.ParentSid
		}
		a.last = at
		a.req++
		a.model = billedModel(u.ModelReq, u.ModelServed)
		if n := calledTool(u.Op); n != "" {
			a.called[n] = true
		}

		// An error carries no usage and is never billed — the same rule the rest
		// of the fold uses. It still counts as a request, and as an error, unless
		// it is the 429 that answers a quota probe.
		if u.Status >= 400 {
			if !benignUsage(u) {
				a.err++
			}
			continue
		}

		bill := billing.Compute(rates, a.model, usageOf(u), at)
		if bill.Priced {
			a.cost += bill.Total
			a.priced = true
		}
		a.tok.In += u.In
		a.tok.Read += u.Read
		a.tok.Write += u.W5m + u.W1h
		a.tok.Out += u.Out

		if !at.Before(winStart) {
			a.winCost += bill.Total
			a.winReqs++
		}
	}

	// ---- fold subagents into the session that spawned them ----
	// A subagent's work is part of its parent's work: its money, tokens and
	// requests land on the parent row, and its own row disappears. What does NOT
	// merge is the toolset accounting — the parent's schemas ship on the parent's
	// requests, at the parent's cadence, so called/winReqs stay per-session.
	// rootOf follows the parent link to the top; the hop cap is cycle insurance,
	// not an expectation (a subagent cannot spawn subagents).
	rootOf := func(sid string) string {
		for hops := 0; hops < 4; hops++ {
			a := agg[sid]
			if a == nil || a.parent == "" || a.parent == sid || agg[a.parent] == nil {
				return sid
			}
			sid = a.parent
		}
		return sid
	}
	for _, sid := range order {
		root := rootOf(sid)
		if root == sid {
			continue
		}
		p, c := agg[root], agg[sid]
		p.agents++
		p.req += c.req
		p.err += c.err
		p.cost += c.cost
		p.priced = p.priced || c.priced
		p.tok.In += c.tok.In
		p.tok.Read += c.tok.Read
		p.tok.Write += c.tok.Write
		p.tok.Out += c.tok.Out
		p.winCost += c.winCost
		// A session with a subagent still running is a session still working.
		if c.last.After(p.last) {
			p.last = c.last
		}
	}

	perHour := 60.0 / windowMin
	out := make([]sessionRow, 0, len(order))
	for _, sid := range order {
		if rootOf(sid) != sid {
			continue // folded into its parent above
		}
		a := agg[sid]

		label := a.label
		if label == "" {
			label = "(no prompt captured)"
		}
		idle := now.Sub(a.last)

		s := sessionRow{
			ID: a.id, Label: label, Model: a.model,
			Live: idle < liveWindow, Idle: humanSince(idle),
			Last: a.last.Format(time.RFC3339),
			Req:  a.req, Err: a.err,
			Tok: a.tok, Cost: a.cost, Priced: a.priced,
			RateHr:        a.winCost * perHour,
			Hit:           hitRate(a.tok.Read, a.tok.In+a.tok.Read+a.tok.Write),
			Stateless:     a.req-a.err >= statelessMinReqs && a.tok.Read == 0,
			ContextWindow: billing.ContextWindow(a.model),
			Agents:        a.agents,
		}

		// The cut list, priced. A never-called schema costs its tokens at the
		// cache-read rate on every request that ships it — so what it costs per
		// hour is that, times the cadence this session is actually running at.
		// A session that has stopped is charged nothing per hour, because it is
		// no longer shipping anything.
		readPerTok := billing.ReadPerTok(rates, a.model, a.last)
		for _, t := range tools[sid].Items {
			if a.called[t.Name] {
				continue
			}
			s.Unused++
			s.UnusedBytes += int64(t.Bytes)
			s.UnusedTok += billing.EstTokens(int64(t.Bytes))
		}
		s.WasteHr = float64(s.UnusedTok) * readPerTok * float64(a.winReqs) * perHour

		out = append(out, s)
	}

	// Live sessions first, then most recently active. What is burning money now
	// is what you are here to look at.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Live != out[j].Live {
			return out[i].Live
		}
		return out[i].Last > out[j].Last // RFC3339 sorts lexically
	})
	return out
}

// calledTool is the tool named by a row's op string, or "" when the turn called
// nothing. op is "tool_use · Bash — git status"; the tool is what sits between
// the two separators.
//
// This is how a session learns which of its schemas it never used, and it works
// only because op is written once, by the response decoder, and read here.
func calledTool(op string) string {
	head, _, _ := strings.Cut(op, " — ")
	kind, name, ok := strings.Cut(head, " · ")
	if !ok || kind != "tool_use" {
		return ""
	}
	return name
}

// probeUsage and benignUsage are isProbe and benignErr for the slim lifetime
// projection — the same tests, against the columns that scan carries. A caller
// that wanted an answer would have left room for one.
func probeUsage(u store.UsageRow) bool {
	return u.MaxTokens == 1 && u.ToolCount == 0
}

func benignUsage(u store.UsageRow) bool {
	return probeUsage(u) && u.Status == 429
}

// humanSince words a duration the way a person reads a clock at a glance.
func humanSince(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}
