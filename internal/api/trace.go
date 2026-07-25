package api

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/guydelarea/tokentracer/internal/anthropic"
	"github.com/guydelarea/tokentracer/internal/billing"
	"github.com/guydelarea/tokentracer/internal/store"
)

// The session trace: one session, everything it did, and what it should have
// done instead.
//
// This is where the interpretations are made — which requests broke the cache
// and why, what each break re-billed, what kind of work the session was doing,
// and which advice to give. Not in the browser. Every dollar figure on a "Do
// next" card is the advice this proxy exists to give, and advice nothing can
// test is advice nobody should take.

// What a request's cache did.
const (
	evHit   = "hit"   // the prefix matched — read at ReadMult
	evPrime = "prime" // first write on this session; there was nothing to hit yet
	evBreak = "break" // primed, and then re-written
	evFresh = "fresh" // billed fresh with no cache in play at all
	evNone  = "none"  // nothing cached, nothing fresh — an empty or unusual turn
	evErr   = "err"   // errored; nothing billed
)

// Why a primed prefix was re-written. "gap" is the one the prefix hashes cannot
// explain: the bytes were identical, the TTL simply ran out.
const (
	causeGap    = "gap"
	causeTools  = "tools"
	causeSystem = "system"
	causeMsg    = "msg"
)

// A context that collapses to under this share of the request before it, from a
// context already big enough to be worth compacting, is a compaction. Nothing on
// the wire announces one — the conversation just gets suddenly, dramatically
// shorter, which is a thing that does not otherwise happen.
const (
	compactDropShare = 0.6
	compactMinCtx    = 20_000
)

// Thresholds for the advice. Each one is a judgement call, so each one is named
// and lives here rather than inside a conditional in the browser.
const (
	exploreAdviceBytes = 50_000 // exploration output worth mentioning at all
	exploreAdviceShare = 0.4    // …as a share of everything the tools returned
	thinkAdviceShare   = 0.22   // thinking as a share of output tokens
	breakAdviceUsd     = 0.02   // re-billed spend worth a card
	compactAtShare     = 0.39   // context, as a share of the model's window
	compactableShare   = 0.7    // …of which a compact would plausibly drop
)

// traceBuckets is how many columns the trace's request/error/latency charts have.
const traceBuckets = 44

// cacheEvent is one request's cache story. Rebill is what it cost ABOVE what a
// hit would have cost — the number the "re-billed" figure sums, and the only
// honest way to price a cache break.
type cacheEvent struct {
	ID     int64   `json:"id"`
	Class  string  `json:"class"`
	Cause  string  `json:"cause,omitempty"`
	BadIdx int     `json:"badIdx"` // prefix segment that diverged; -1 when there is none to name
	GapMs  int64   `json:"gapMs"`  // idle time before this request
	Rebill float64 `json:"rebill,omitempty"`
}

// activityStat is what a session's money went on, by the kind of work each turn
// was doing.
type activityStat struct {
	Kind string  `json:"kind"`
	N    int     `json:"n"`
	Cost float64 `json:"cost"`
}

// outShape is the session's output tokens by block type, plus how its replies
// ended.
type outShape struct {
	Think     int64          `json:"think"`
	Text      int64          `json:"text"`
	Tool      int64          `json:"tool"`
	Total     int64          `json:"total"`
	Truncated int            `json:"truncated"` // replies cut off at max_tokens
	Stops     map[string]int `json:"stops"`
}

// traceBucket is one column of the request / error / latency strips.
type traceBucket struct {
	N    int   `json:"n"`
	Err  int   `json:"err"`
	Ms   int64 `json:"ms"`   // mean duration
	Ttft int64 `json:"ttft"` // mean time to first token
}

// toolRow is one schema the session ships, with what it costs to keep shipping it.
type toolRow struct {
	Name   string  `json:"name"`
	Bytes  int64   `json:"bytes"`
	Tokens int64   `json:"tokens"`
	Unused bool    `json:"unused"`
	Usd    float64 `json:"usd"` // what re-shipping it costs per hour at this session's cadence
}

// An insight is a fact with a price on it, not a sentence: the page words it.
// Kind selects the wording, Usd ranks it, PerHr says whether Usd is a rate, and
// N is whatever the wording needs to count.
const (
	insToolset  = "toolset"  // schemas shipped and never called
	insExplore  = "explore"  // exploration output is filling the context
	insThinking = "thinking" // thinking is a large share of output
	insTruncate = "truncate" // replies cut off at max_tokens
	insCache    = "cache"    // cache breaks are re-billing the history
	insHistory  = "history"  // the context is big enough to be worth compacting
)

type insight struct {
	Kind  string  `json:"kind"`
	Usd   float64 `json:"usd"`
	PerHr bool    `json:"perHr"`
	N     int     `json:"n"`
}

// traceView is the /api/trace contract.
type traceView struct {
	Sid   string `json:"sid"`
	Label string `json:"label"`
	Model string `json:"model"`
	Live  bool   `json:"live"`
	Idle  string `json:"idle"`
	First string `json:"first"`
	Last  string `json:"last"`
	DurMs int64  `json:"durMs"`

	Req    int     `json:"req"`
	Err    int     `json:"err"`
	Cost   float64 `json:"cost"`
	AvgReq float64 `json:"avgReq"`
	Priced bool    `json:"priced"`
	Tok    tokens  `json:"tok"`
	Hit    float64 `json:"hit"`

	// The context as it stands on the latest request that actually spoke — never
	// on an errored one. A 429 carries no usage, so reading the context off it
	// reports a session with no context at all, which is what a session whose last
	// request was a quota probe would otherwise show.
	Ctx           int64     `json:"ctx"`
	ContextWindow int64     `json:"contextWindow"`
	CtxBytes      byteSplit `json:"ctxBytes"`

	Rows      []recentRow  `json:"rows"`  // chronological
	Flow      []flowTurn   `json:"flow"`  // causal request → tool → result chain
	Cache     []cacheEvent `json:"cache"` // parallel to Rows, one per request
	Breaks    int          `json:"breaks"`
	BreakCost float64      `json:"breakCost"`
	Compacted []int        `json:"compacted"` // indexes into Rows where the context collapsed

	Out      outShape       `json:"out"`
	Activity []activityStat `json:"activity"` // costliest first
	Buckets  []traceBucket  `json:"buckets"`

	Tools        []toolRow              `json:"tools"` // largest schema first
	Cut          []toolRow              `json:"cut"`   // …the never-called ones: the cut list
	UnusedTok    int64                  `json:"unusedTok"`
	CutUsd       float64                `json:"cutUsd"`
	Results      []anthropic.ResultItem `json:"results"` // tool output sitting in the context
	ResultBytes  int64                  `json:"resultBytes"`
	ExploreBytes int64                  `json:"exploreBytes"`
	ExploreCalls int                    `json:"exploreCalls"`

	// Stateless: the session has spoken enough times for the cache to have paid
	// off, and has never once had a cache read. Either the client sends no
	// cache_control at all, or something dynamic at the head of the prompt poisons
	// the prefix on every call — and in both cases the whole context bills fresh,
	// every single request. It changes the cache advice from "you broke it
	// sometimes" to "it has never worked once".
	Stateless bool `json:"stateless"`

	Insights []insight `json:"insights"`

	// The subagents this session spawned, one summary each. Their rows are NOT
	// mixed into Rows/Cache above — each agent is its own conversation with its
	// own cache story, and interleaving them would invent breaks that never
	// happened. Their money is reported alongside instead: the Spent the page
	// shows is Cost + AgentCost, which is what the sessions table shows too.
	Agents    []agentRow `json:"agents"`
	AgentCost float64    `json:"agentCost"`
	AgentReq  int        `json:"agentReq"`

	// The latest capture was deleted, so the schemas and the tool-result rows
	// cannot be read. Everything derived from the fact rows still stands.
	CaptureGone bool `json:"captureGone"`
}

// agentRow is one subagent session, summarized for the trace's drill-down list.
type agentRow struct {
	Sid    string  `json:"sid"`
	Label  string  `json:"label"`
	Model  string  `json:"model"`
	Req    int     `json:"req"`
	Err    int     `json:"err"`
	Cost   float64 `json:"cost"`
	Priced bool    `json:"priced"`
	Tok    tokens  `json:"tok"`
	Live   bool    `json:"live"`
	Last   string  `json:"last"`
}

// flowTurn is the causal version of a request row: what the user asked, which
// tools the reply invoked, and which results the next request carried back.
type flowTurn struct {
	ID       int64        `json:"id"`
	Time     string       `json:"time"`
	Ask      string       `json:"ask,omitempty"`
	Status   int          `json:"status"`
	Captured bool         `json:"captured"`
	Calls    []flowCall   `json:"calls"`
	Results  []flowResult `json:"results"`
}

type flowCall struct {
	ID         string `json:"id,omitempty"`
	Name       string `json:"name"`
	Summary    string `json:"summary,omitempty"`
	Spawn      bool   `json:"spawn,omitempty"`
	AgentSid   string `json:"agentSid,omitempty"`
	AgentLabel string `json:"agentLabel,omitempty"`
}

type flowResult struct {
	ToolUseID string `json:"toolUseId,omitempty"`
	Name      string `json:"name"`
	Bytes     int    `json:"bytes"`
}

func flowInput(raw json.RawMessage) (summary, prompt string) {
	var in struct {
		Command     string `json:"command"`
		Path        string `json:"path"`
		Query       string `json:"query"`
		Prompt      string `json:"prompt"`
		Description string `json:"description"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return "", ""
	}
	prompt = strings.TrimSpace(in.Prompt)
	for _, s := range []string{in.Command, in.Path, in.Query, prompt, in.Description} {
		if s = strings.Join(strings.Fields(s), " "); s != "" {
			if len([]rune(s)) > 120 {
				return string([]rune(s)[:120]) + "…", prompt
			}
			return s, prompt
		}
	}
	return "", prompt
}

func flowCalls(body []byte) []flowCall {
	var resp anthropic.Response
	if json.Unmarshal(body, &resp) != nil {
		return nil
	}
	var out []flowCall
	for _, b := range resp.Content {
		if b.Type != "tool_use" || b.Name == "" {
			continue
		}
		summary, _ := flowInput(b.Input)
		out = append(out, flowCall{ID: b.ID, Name: b.Name, Summary: summary, Spawn: b.Name == "Task" || b.Name == "Agent"})
	}
	return out
}

// What kind of work each tool does — and, for exploreTools, whose output lands
// back in the context and is re-read by every request after it.
//
// A name this table does not know falls through to "reply", which is the one
// attribution that cannot be wrong for a turn that called nothing. It IS wrong
// for a turn that called something, so a new client's tools belong here.
var exploreTools = map[string]bool{
	"Read": true, "Grep": true, "Glob": true, "WebFetch": true, "WebSearch": true,
}

var editTools = map[string]bool{
	"Edit": true, "MultiEdit": true, "Write": true, "NotebookEdit": true,
}

var runTools = map[string]bool{
	"Bash": true, "BashOutput": true, "KillShell": true,
}

var planTools = map[string]bool{
	"TodoWrite": true, "Task": true, "ExitPlanMode": true, "Skill": true,
}

// activityOf is the category a request's operation belongs to.
func activityOf(op string) string {
	name := calledTool(op)
	switch {
	case name == "":
		return "reply"
	case strings.HasPrefix(name, "mcp__"):
		return "mcp"
	case exploreTools[name]:
		return "explore"
	case editTools[name]:
		return "edit"
	case runTools[name]:
		return "run"
	case planTools[name]:
		return "plan"
	}
	return "tool"
}

// foldTrace folds one session's rows into everything the trace screen draws.
// Pure: `now`, the schemas and the latest capture's results are all parameters.
//
// rows arrive chronological, and must: every cache event is defined against the
// request before it.
func foldTrace(sid string, rows []store.Row, ts toolset, results []anthropic.ResultItem, captureGone bool,
	rates []billing.Rate, now time.Time) traceView {

	t := traceView{
		Sid:         sid,
		Out:         outShape{Stops: map[string]int{}},
		Rows:        make([]recentRow, 0, len(rows)),
		Cache:       make([]cacheEvent, 0, len(rows)),
		Compacted:   []int{},
		Results:     results,
		CaptureGone: captureGone,
	}
	if len(rows) == 0 {
		return t
	}

	first := time.UnixMilli(rows[0].TsMs)
	last := time.UnixMilli(rows[len(rows)-1].TsMs)
	t.First = first.Format(time.RFC3339)
	t.Last = last.Format(time.RFC3339)
	t.DurMs = last.Sub(first).Milliseconds()
	t.Idle = humanSince(now.Sub(last))
	t.Live = now.Sub(last) < liveWindow
	t.Model = billedModel(rows[len(rows)-1].ModelReq, rows[len(rows)-1].ModelServed)

	// ---- the rows, and the totals they add up to ----
	called := map[string]bool{}
	act := map[string]*activityStat{}
	var order []string
	var okCost float64
	var okReqs int

	for _, r := range rows {
		v := rowView(r, rates)
		t.Rows = append(t.Rows, v)

		// The session's name is the first thing actually ASKED — and a probe asks
		// nothing. Same rule as the sessions table, and for the same reason: without
		// it every trace on the page is titled "quota".
		if t.Label == "" && !isProbe(r) {
			t.Label = r.Label
		}
		if n := calledTool(r.Op); n != "" {
			called[n] = true
		}
		t.Req++

		isErr := r.Status >= 400 && !benignErr(r)
		if isErr {
			t.Err++
		}

		// Output shape counts every reply that produced tokens, error or not —
		// tokens billed are tokens billed. Cost, stops and activity count only the
		// requests that actually worked.
		t.Out.Think += r.ThinkTokens
		t.Out.Text += r.TextTokens
		t.Out.Tool += r.ToolTokens
		t.Out.Total += deref(r.OutputTokens)

		if r.Status >= 400 {
			continue
		}
		cost := v.Cost.In + v.Cost.Read + v.Cost.Write + v.Cost.Out
		t.Cost += cost
		if v.Priced {
			t.Priced = true
		}
		okCost += cost
		okReqs++

		t.Tok.In += v.Tok.In
		t.Tok.Read += v.Tok.Read
		t.Tok.Write += v.Tok.Write
		t.Tok.Out += v.Tok.Out

		if r.StopReason != "" {
			t.Out.Stops[r.StopReason]++
		}
		if r.StopReason == "max_tokens" {
			t.Out.Truncated++
		}

		k := activityOf(r.Op)
		a := act[k]
		if a == nil {
			a = &activityStat{Kind: k}
			act[k] = a
			order = append(order, k)
		}
		a.N++
		a.Cost += cost
	}

	if okReqs > 0 {
		t.AvgReq = okCost / float64(okReqs)
	}
	t.Hit = hitRate(t.Tok.Read, t.Tok.In+t.Tok.Read+t.Tok.Write)
	t.ContextWindow = billing.ContextWindow(t.Model)

	for _, k := range order {
		t.Activity = append(t.Activity, *act[k])
	}
	sort.SliceStable(t.Activity, func(i, j int) bool { return t.Activity[i].Cost > t.Activity[j].Cost })

	// ---- the context, as it stands now ----
	// The latest request that actually spoke, not simply the latest.
	latest := rows[len(rows)-1]
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Status < 400 {
			latest = rows[i]
			break
		}
	}
	t.Ctx = deref(latest.InputTokens) + deref(latest.CacheReadTokens) +
		deref(latest.CacheW5mTokens) + deref(latest.CacheW1hTokens)
	t.CtxBytes = byteSplit{
		Total:    latest.TotalBytes,
		Tools:    latest.ToolsBytes,
		System:   latest.SystemBytes,
		Messages: latest.MessagesBytes,
	}

	// ---- cache events ----
	// A break is a re-write of a prefix that had already been primed. The prefix
	// hashes name the segment that changed; when they are identical, the only
	// thing that can have broken it is the TTL.
	t.Stateless = t.Req-t.Err >= statelessMinReqs && t.Tok.Read == 0
	stateless := t.Stateless
	primed := false
	for i, r := range rows {
		e := cacheEvent{ID: r.ID, BadIdx: -1}
		if i > 0 {
			e.GapMs = r.TsMs - rows[i-1].TsMs
		}

		read := deref(r.CacheReadTokens)
		write := deref(r.CacheW5mTokens) + deref(r.CacheW1hTokens)
		in := deref(r.InputTokens)

		switch {
		case r.Status >= 400:
			e.Class = evErr
		case read > 0:
			e.Class = evHit
			primed = true
		case write > 0:
			if primed {
				e.Class = evBreak
			} else {
				e.Class = evPrime
			}
			primed = true
		case in > 0 && !stateless:
			e.Class = evFresh
		default:
			e.Class = evNone
		}

		v := t.Rows[i]
		switch e.Class {
		case evBreak:
			e.Cause = causeMsg
			switch {
			case i > 0 && time.Duration(e.GapMs)*time.Millisecond > billing.CacheTTL:
				// Nothing changed. The prefix just went cold.
				e.Cause = causeGap
			case i > 0:
				e.BadIdx = anthropic.FirstDiff(prefixOf(rows[i-1]), prefixOf(r))
				switch e.BadIdx {
				case 0:
					e.Cause = causeTools
				case 1:
					e.Cause = causeSystem
				}
			}
			e.Rebill = v.Cost.Write * billing.RebillWriteShare
		case evFresh:
			e.Rebill = v.Cost.In * billing.RebillFreshShare
		}
		if e.Rebill > 0 {
			t.BreakCost += e.Rebill
			t.Breaks++
		}
		t.Cache = append(t.Cache, e)

		// A compaction: the context did not grow, it collapsed.
		if i > 0 {
			prev := ctxOf(rows[i-1])
			if cur := ctxOf(r); prev > compactMinCtx && float64(cur) < float64(prev)*compactDropShare {
				t.Compacted = append(t.Compacted, i)
			}
		}
	}

	// ---- the charts ----
	t.Buckets = bucketize(rows, traceBuckets)

	// ---- the toolset, and the cut list ----
	readPerTok := billing.ReadPerTok(rates, t.Model, last)
	// A live session is priced at the cadence it is running at; a finished one at
	// what it already spent. Advice on a dead session is a receipt, not a warning.
	cadence := float64(t.Req)
	if t.Live {
		cadence = reqPerHour(rows, now)
	}
	for _, it := range ts.Items {
		row := toolRow{
			Name:   it.Name,
			Bytes:  int64(it.Bytes),
			Tokens: billing.EstTokens(int64(it.Bytes)),
			Unused: !called[it.Name],
		}
		row.Usd = float64(row.Tokens) * readPerTok * cadence
		t.Tools = append(t.Tools, row)
		if row.Unused {
			t.Cut = append(t.Cut, row)
			t.UnusedTok += row.Tokens
			t.CutUsd += row.Usd
		}
	}

	for _, r := range results {
		t.ResultBytes += int64(r.Bytes)
		if exploreTools[r.Name] {
			t.ExploreBytes += int64(r.Bytes)
			t.ExploreCalls += r.N
		}
	}

	t.Insights = insightsOf(t, readPerTok, cadence)
	return t
}

// foldAgents summarizes the subagent sessions a parent spawned, first-spawned
// first. rows is every child row in one chronological pass (see AgentRows);
// grouping by the child's own session id happens here. Pricing is per row, at
// the row's own timestamp — the same rule as everywhere else, for the same
// reason: a group total that disagreed with the rest of the page would
// discredit both.
func foldAgents(rows []store.Row, rates []billing.Rate, now time.Time) []agentRow {
	agg := map[string]*agentRow{}
	last := map[string]time.Time{}
	var order []string

	for _, r := range rows {
		sid := r.SessionID
		if sid == "" {
			continue
		}
		a := agg[sid]
		if a == nil {
			a = &agentRow{Sid: sid}
			agg[sid] = a
			order = append(order, sid)
		}
		at := time.UnixMilli(r.TsMs)
		last[sid] = at
		if a.Label == "" && !isProbe(r) {
			a.Label = r.Label
		}
		a.Model = billedModel(r.ModelReq, r.ModelServed)
		a.Req++
		if r.Status >= 400 {
			if !benignErr(r) {
				a.Err++
			}
			continue
		}
		u := billing.Usage{
			In:      deref(r.InputTokens),
			Read:    deref(r.CacheReadTokens),
			Write5m: deref(r.CacheW5mTokens),
			Write1h: deref(r.CacheW1hTokens),
			Out:     deref(r.OutputTokens),
		}
		bill := billing.Compute(rates, a.Model, u, at)
		if bill.Priced {
			a.Cost += bill.Total
			a.Priced = true
		}
		a.Tok.In += u.In
		a.Tok.Read += u.Read
		a.Tok.Write += u.Write5m + u.Write1h
		a.Tok.Out += u.Out
	}

	out := make([]agentRow, 0, len(order))
	for _, sid := range order {
		a := agg[sid]
		if a.Label == "" {
			a.Label = "(no prompt captured)"
		}
		a.Live = now.Sub(last[sid]) < liveWindow
		a.Last = last[sid].Format(time.RFC3339)
		out = append(out, *a)
	}
	return out
}

// prefixOf decodes a row's stored prefix chain. A row from before the column
// existed has none, and FirstDiff on an empty chain returns -1 — no segment
// named, which is the truthful answer rather than a guessed one.
func prefixOf(r store.Row) []string {
	if r.Prefix == "" {
		return nil
	}
	var out []string
	if json.Unmarshal([]byte(r.Prefix), &out) != nil {
		return nil
	}
	return out
}

// ctxOf is the context a request carried: everything it shipped in, cached or not.
func ctxOf(r store.Row) int64 {
	return deref(r.InputTokens) + deref(r.CacheReadTokens) +
		deref(r.CacheW5mTokens) + deref(r.CacheW1hTokens)
}

// reqPerHour is the session's current cadence, measured over the same window the
// overview calls "now". Zero requests in the window means a session that has
// stopped, and a rate of zero is the truth about it.
func reqPerHour(rows []store.Row, now time.Time) float64 {
	winStart := now.Add(-windowMin * time.Minute).UnixMilli()
	n := 0
	for _, r := range rows {
		if r.TsMs >= winStart {
			n++
		}
	}
	return float64(n) * (60.0 / windowMin)
}

// bucketize spreads a session's requests over n columns of equal time. Equal
// TIME, not equal count: the gaps are the point — a session that sat idle for
// twenty minutes should look like it sat idle for twenty minutes.
func bucketize(rows []store.Row, n int) []traceBucket {
	out := make([]traceBucket, n)
	if len(rows) == 0 {
		return out
	}
	first, last := rows[0].TsMs, rows[len(rows)-1].TsMs
	span := last - first
	if span <= 0 {
		span = 1 // one instant: everything lands in bucket 0
	}

	var ms, ttft [][]int64 = make([][]int64, n), make([][]int64, n)
	for _, r := range rows {
		i := int((r.TsMs - first) * int64(n) / span)
		if i >= n {
			i = n - 1 // the last row lands exactly on the boundary
		}
		out[i].N++
		if r.Status >= 400 && !benignErr(r) {
			out[i].Err++
		}
		if r.DurationMs > 0 {
			ms[i] = append(ms[i], r.DurationMs)
		}
		if r.TtftMs > 0 {
			ttft[i] = append(ttft[i], r.TtftMs)
		}
	}
	for i := range out {
		out[i].Ms = mean(ms[i])
		out[i].Ttft = mean(ttft[i])
	}
	return out
}

func mean(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	var sum int64
	for _, x := range v {
		sum += x
	}
	return sum / int64(len(v))
}

// insightsOf ranks the session's advice by what it costs. Every card is priced
// against a fact already in the fold — nothing here is a heuristic with a dollar
// sign glued on afterwards.
func insightsOf(t traceView, readPerTok, cadence float64) []insight {
	var out []insight

	// Schemas that shipped on every request and were never called. They ride
	// inside the cached prefix, so they bill at the cache-read rate.
	if t.UnusedTok > 0 && t.CutUsd > 0 {
		out = append(out, insight{Kind: insToolset, Usd: t.CutUsd, PerHr: t.Live, N: len(t.Cut)})
	}

	// Exploration output that every later request re-reads. Priced at what the
	// session's cache reads actually cost, times exploration's share of them.
	if t.ResultBytes > 0 && t.ExploreBytes > exploreAdviceBytes &&
		float64(t.ExploreBytes)/float64(t.ResultBytes) > exploreAdviceShare {
		share := float64(t.ExploreBytes) / float64(t.ResultBytes)
		out = append(out, insight{Kind: insExplore, Usd: share * costOfReads(t), N: int(t.ExploreBytes / 1024)})
	}

	// Thinking bills at the output rate and never caches.
	if t.Out.Total > 0 && float64(t.Out.Think)/float64(t.Out.Total) > thinkAdviceShare {
		out = append(out, insight{
			Kind: insThinking, N: int(100 * t.Out.Think / t.Out.Total),
			Usd: costOfThinking(t),
		})
	}

	// A truncated reply is a turn that has to run again — so it costs what a turn
	// costs.
	if t.Out.Truncated > 0 {
		out = append(out, insight{Kind: insTruncate, Usd: float64(t.Out.Truncated) * t.AvgReq, N: t.Out.Truncated})
	}

	if t.BreakCost > breakAdviceUsd {
		out = append(out, insight{Kind: insCache, Usd: t.BreakCost, N: t.Breaks})
	}

	// A context this big is re-read on every request; the slope is the cost of
	// not compacting it.
	if t.Live && t.ContextWindow > 0 && float64(t.Ctx) > float64(t.ContextWindow)*compactAtShare {
		out = append(out, insight{
			Kind: insHistory, PerHr: true,
			N:   int(100 * t.Ctx / t.ContextWindow),
			Usd: float64(t.Ctx) * compactableShare * readPerTok * cadence,
		})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Usd > out[j].Usd })
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

// costOfReads and costOfThinking sum the session's rows rather than re-derive a
// rate: the components are already priced, one row at a time, and summing what
// was priced can never disagree with it.
func costOfReads(t traceView) float64 {
	var c float64
	for _, r := range t.Rows {
		c += r.Cost.Read
	}
	return c
}

func costOfThinking(t traceView) float64 {
	if t.Out.Total <= 0 {
		return 0
	}
	var outCost float64
	for _, r := range t.Rows {
		outCost += r.Cost.Out
	}
	return outCost * float64(t.Out.Think) / float64(t.Out.Total)
}
