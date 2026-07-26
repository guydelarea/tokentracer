// @ts-check
/* TokenTracer dashboard. Vanilla JS, no build step, no external requests.
 *
 * The layout, the palette and the copy are synced from the Claude Design
 * project — that file is the source of truth for how this looks. What is HERE is
 * the wiring: the same three screens, drawn from the real API instead of a
 * simulation.
 *
 *   Overview  → what every session is costing you, and what it is wasting.
 *   Trace     → one session: where its money went and what to do about it.
 *   Inspector → one request, down to the bytes.
 *
 * Three rules carried over from tokentrace, all load-bearing:
 *
 *  1. Everything untrusted goes through esc() before it becomes HTML. Log content
 *     is whatever the model and the tools emitted — treat it as attacker-influenced.
 *  2. Numbers are folded server-side in Go; this file only words and draws them.
 *     No cost, burn rate, cache verdict, percentile or aggregate is computed here.
 *     The exceptions are presentation and presentation only: bar widths, the
 *     percentages beside them, and the running sum behind the cumulative-$ line —
 *     each a redrawing of figures Go already decided.
 *  3. The advice is not written here either. Go decides WHICH cards to show and
 *     what each one costs; this file only knows how to word them.
 *
 * The types below are checked, not compiled: `tsc -p web` reads them and emits
 * nothing. This file ships to the browser exactly as written, because that is the
 * file go:embed bakes into the binary. There is no build step and must not be one.
 */

/* ---------- the wire contract ----------
 * The Go json tags ARE the contract. The server hands this page JSON and there is
 * no translation layer between the two, so a field renamed on one side and not the
 * other has to break somewhere — and this is where, at `tsc`, instead of silently
 * on the dashboard as a zero.
 *
 *   statsView & below  → internal/api/fold.go, sessions.go   (GET /api/stats)
 *   traceView & below  → internal/api/trace.go               (GET /api/trace?sid=)
 *   captureView        → internal/api/api.go                 (GET /api/capture?id=)
 *   Breakdown & below  → internal/anthropic/anthropic.go
 */

/**
 * @typedef {object} tokens
 * @property {number} in    fresh input
 * @property {number} read  cache reads
 * @property {number} write cache writes, 5m and 1h summed server-side
 * @property {number} out
 */

/**
 * @typedef {object} costs
 * @property {number} in
 * @property {number} read
 * @property {number} write
 * @property {number} out
 */

/**
 * @typedef {object} byteSplit
 * @property {number} total
 * @property {number} tools
 * @property {number} system
 * @property {number} messages
 */

/**
 * Where a reply's output tokens went. An estimate — the API bills one output
 * figure and never says which block spent it — split by block bytes in Go.
 * @typedef {object} shape
 * @property {number} think
 * @property {number} text
 * @property {number} tool
 */

/** @typedef {object} latency
 * @property {number} p50Ttft
 * @property {number} p95Ttft
 */

/**
 * One minute of the spend timeline: tokens by class, cost by class.
 * @typedef {object} bucket
 * @property {number} t          unix ms at the start of the minute
 * @property {number} n
 * @property {number} input
 * @property {number} cacheRead
 * @property {number} cacheWrite
 * @property {number} output
 * @property {number} costIn
 * @property {number} costRead
 * @property {number} costWrite
 * @property {number} costOut
 * @property {number} err
 */

/**
 * @typedef {object} reqRow
 * @property {number} id
 * @property {string} time      RFC3339
 * @property {string} label     what was ASKED: the first user text, ≤64 chars
 * @property {string} model     the model the money was billed against
 * @property {string} sid
 * @property {string} op
 * @property {number} status
 * @property {number} ms
 * @property {number} ttft
 * @property {string} stop
 * @property {boolean} aborted
 * @property {tokens} tok
 * @property {costs} cost
 * @property {boolean} priced
 * @property {shape} shape
 * @property {byteSplit} bytes
 * @property {string} [errType] omitempty
 * @property {string} [errMsg]  omitempty
 * @property {boolean} [probe]  omitempty
 */

/**
 * One row of the sessions table — the front page, and the unit of work a person
 * actually thinks in.
 * @typedef {object} sessionRow
 * @property {string} id
 * @property {string} label
 * @property {string} model
 * @property {boolean} live
 * @property {string} idle          "12s", "4m", "2h"
 * @property {string} last          RFC3339
 * @property {number} req
 * @property {number} err
 * @property {tokens} tok
 * @property {number} cost
 * @property {number} rateHr        spend in the window, per hour — 0 once it stops
 * @property {number} hit
 * @property {number} unused        schemas shipped and never called
 * @property {number} unusedTok
 * @property {number} unusedBytes
 * @property {number} wasteHr
 * @property {boolean} priced
 * @property {boolean} stateless
 * @property {number} contextWindow
 * @property {number} agents        subagent sessions folded into this row
 */

/**
 * @typedef {object} overview
 * @property {number} burnNow   $/hr, extrapolated from the current window
 * @property {number} burnAvg   $/hr, lifetime
 * @property {number} todayCost $ since local midnight, priced rows only
 * @property {number} reqHr
 * @property {number} winReqs
 * @property {number} avgReq
 * @property {number} hitNow
 * @property {number} hitAvg
 * @property {number} peakMin
 * @property {number} windowMin
 * @property {tokens} tokens    the current window
 * @property {latency} latency
 * @property {bucket[]} timeline
 * @property {number} wasteHr     never-called schemas, live sessions, at current cadence
 * @property {number} unusedCount
 * @property {string} worstSid
 * @property {boolean} coldStart under one window of history: no trend can be read off it
 */

/**
 * @typedef {object} statsView
 * @property {number} port
 * @property {string} upstream
 * @property {number} traced
 * @property {number} cost
 * @property {number} unpricedReqs
 * @property {string[]} unpricedModels
 * @property {tokens} tokens    lifetime
 * @property {overview} overview
 * @property {sessionRow[]} sessions
 * @property {storage} storage
 */

/** What the captures cost on disk, and the window that bounds them.
 * @typedef {object} storage
 * @property {number} captureBytes
 * @property {string} retention    off | 24h | 7d | 30d
 */

/**
 * One local day of spend, priced one request at a time in Go and only then
 * added up. Months are summed from these on this side — see byMonth.
 * @typedef {object} dayBucket
 * @property {number} t         unix ms at local midnight
 * @property {costs} cost
 * @property {tokens} tok
 * @property {number} n
 * @property {number} err
 * @property {number} sessions  distinct session ids that day
 * @property {number} unpriced  rows with no rate for their model — never a silent $0
 * @property {Record<string, number>} models  billed model → its spend that day
 */

/** @typedef {object} historyView
 * @property {dayBucket[]} days  oldest first; a day with no traffic has no bucket, except today
 */

/** What one request's cache did, and what it cost.
 * @typedef {object} cacheEvent
 * @property {number} id
 * @property {string} class    hit | prime | break | fresh | none | err
 * @property {string} [cause]  gap | tools | system | msg
 * @property {number} badIdx   prefix segment that diverged; -1 when there is none to name
 * @property {number} gapMs    idle time before this request
 * @property {number} [rebill] what it cost ABOVE what a hit would have cost
 */

/** @typedef {object} activityStat
 * @property {string} kind
 * @property {number} n
 * @property {number} cost
 */

/** @typedef {object} outShape
 * @property {number} think
 * @property {number} text
 * @property {number} tool
 * @property {number} total
 * @property {number} truncated
 * @property {Record<string, number>} stops
 */

/** @typedef {object} traceBucket
 * @property {number} n
 * @property {number} err
 * @property {number} ms
 * @property {number} ttft
 */

/** One schema the session ships, and what it costs to keep shipping it.
 * @typedef {object} toolRow
 * @property {string} name
 * @property {number} bytes
 * @property {number} tokens
 * @property {boolean} unused
 * @property {number} usd
 */

/** A fact with a price on it. Go ranks them; this file words them.
 * @typedef {object} insight
 * @property {string} kind   toolset | explore | thinking | truncate | cache | history
 * @property {number} usd
 * @property {boolean} perHr
 * @property {number} n
 */

/** @typedef {object} resultItem
 * @property {string} name
 * @property {number} bytes
 * @property {number} n
 */

/** One subagent session a trace's session spawned — its own conversation, its
 * own cache story, summarized here and traceable by clicking through.
 * @typedef {object} agentRow
 * @property {string} sid
 * @property {string} label
 * @property {string} model
 * @property {number} req
 * @property {number} err
 * @property {number} cost
 * @property {boolean} priced
 * @property {tokens} tok
 * @property {boolean} live
 * @property {string} last
 */

/** One tool invocation emitted by a reply in the causal session flow.
 * @typedef {object} flowCall
 * @property {string} [id]
 * @property {string} name
 * @property {string} [summary]
 * @property {boolean} [agent]
 * @property {boolean} [spawn]
 * @property {string} [agentSid]
 * @property {string} [agentLabel]
 */

/** One tool result carried back in a request.
 * @typedef {object} flowResult
 * @property {string} [toolUseId]
 * @property {string} name
 * @property {number} bytes
 */

/** One causal request → tools → next-request-results turn.
 * @typedef {object} flowTurn
 * @property {number} id
 * @property {string} time
 * @property {string} [ask]
 * @property {number} status
 * @property {boolean} captured
 * @property {flowCall[]} calls
 * @property {flowResult[]} results
 */

/**
 * @typedef {object} traceView
 * @property {string} sid
 * @property {string} label
 * @property {string} model
 * @property {boolean} live
 * @property {string} idle
 * @property {string} first
 * @property {string} last
 * @property {number} durMs
 * @property {number} req
 * @property {number} err
 * @property {number} cost
 * @property {number} avgReq
 * @property {boolean} priced
 * @property {tokens} tok
 * @property {number} hit
 * @property {number} ctx
 * @property {number} contextWindow
 * @property {byteSplit} ctxBytes
 * @property {reqRow[]} rows
 * @property {flowTurn[]} flow
 * @property {cacheEvent[]} cache
 * @property {number} breaks
 * @property {number} breakCost
 * @property {number[]} compacted
 * @property {outShape} out
 * @property {activityStat[]} activity
 * @property {traceBucket[]} buckets
 * @property {toolRow[]} tools
 * @property {toolRow[]} cut
 * @property {number} unusedTok
 * @property {number} cutUsd
 * @property {resultItem[]} results
 * @property {number} resultBytes
 * @property {number} exploreBytes
 * @property {number} exploreCalls
 * @property {boolean} stateless
 * @property {insight[]} insights
 * @property {agentRow[]} agents    subagents this session spawned; their rows are not mixed into rows/cache
 * @property {number} agentCost
 * @property {number} agentReq
 * @property {boolean} captureGone
 */

/** @typedef {object} ToolItem @property {string} name @property {number} bytes */
/** @typedef {object} SystemItem @property {number} bytes @property {string} cacheControl */
/** @typedef {object} MessageItem @property {string} role @property {number} bytes @property {string[]} blockKinds */
/** @typedef {object} Flags @property {boolean} thinking @property {boolean} contextManagement @property {boolean} outputConfig */
/**
 * @typedef {object} Breakdown
 * @property {ToolItem[]} tools
 * @property {SystemItem[]} system
 * @property {MessageItem[]} messages
 * @property {Flags} flags
 */

/**
 * The /api/capture contract, plus the one field the server never sends: `missing`
 * is MISSING, the sentinel this page substitutes when the fetch 404s.
 * @typedef {object} captureView
 * @property {any} [request]
 * @property {any} [response]
 * @property {Breakdown} [breakdown]
 * @property {boolean} [missing]     client-side only
 */

/** What a row's op string says the reply did.
 * @typedef {object} opInfo
 * @property {string} tag
 * @property {string} tagC
 * @property {string} name
 * @property {string} args
 */

var MRD = '#2fbf87', MIN = '#5aa2f7', MWR = '#d9a04e', MOU = '#ededed', MER = '#ff5a5a', MTH = '#a78bfa';
var CTOOL = '#c9c9c9', CSYS = '#7a7a7a', CHIST = '#454545';

/* The first frame is drawn before the first poll lands, so D starts as a real
   statsView full of zeros rather than a half-populated one. Every field the page
   reads exists from the first paint, which is why nothing downstream has to guard
   against a shape that never occurs. */
/** @returns {overview} */
function zeroOverview() {
  return {
    burnNow: 0, burnAvg: 0, todayCost: 0, reqHr: 0, winReqs: 0, avgReq: 0, hitNow: 0, hitAvg: 0,
    peakMin: 0, windowMin: 10, coldStart: false,
    tokens: { in: 0, read: 0, write: 0, out: 0 },
    latency: { p50Ttft: 0, p95Ttft: 0 },
    timeline: [],
    wasteHr: 0, unusedCount: 0, worstSid: ''
  };
}

/** @type {statsView} */
var D = {
  port: 0, upstream: '', traced: 0, cost: 0, unpricedReqs: 0, unpricedModels: [],
  tokens: { in: 0, read: 0, write: 0, out: 0 },
  overview: zeroOverview(),
  sessions: [],
  storage: { captureBytes: 0, retention: 'off' }
};
/** @type {traceView|null} */
var T = null;
/** The day buckets, oldest first. Empty until /api/history first lands.
 * @type {dayBucket[]} */
var H = [];

/* A session is a page, not just an in-memory selection. Keeping its id in the
   query string makes traces refreshable and shareable without asking the Go
   server to serve a second copy of the dashboard at every possible path. */
/** @returns {string|null} */
function sessionFromURL() {
  var sid = new URL(window.location.href).searchParams.get('session');
  return sid || null;
}

/* And a view is a page for the same reason. The URL is the state here rather
   than a field on S: nothing has to keep the two in sync, so a back button, a
   bookmark and a click cannot disagree about which screen you are on.
   ?session= wins over both — it names one thing to look at. */
/** @returns {string} overview | history */
function viewFromURL() {
  return new URL(window.location.href).searchParams.get('view') === 'history' ? 'history' : 'overview';
}

/** @param {string} view @returns {string} */
function viewURL(view) {
  var url = new URL(window.location.href);
  url.searchParams.delete('session'); // switching screens leaves the trace behind
  if (view === 'history') url.searchParams.set('view', 'history');
  else url.searchParams.delete('view');
  return url.pathname + url.search + url.hash;
}

/** @param {string|null} sid @returns {string} */
function sessionURL(sid) {
  var url = new URL(window.location.href);
  if (sid) url.searchParams.set('session', sid);
  else url.searchParams.delete('session');
  return url.pathname + url.search + url.hash;
}

/** The whole of the page's state: which screen, which session, which request.
 * `open` is the inspector's expand/collapse state — one keyed map rather than a
 * flag per thing, because the set of expandable things is data-driven. It is
 * reset whenever a new request is opened. `xrow` is the same idea for the trace
 * list's unfolded rows, keyed by request id so it survives the 2s re-render.
 * `range` is the history screen's span in days; 365 is the one that means months.
 * @type {{sid: string|null, id: number|null, tab: string, toolsAll: boolean, cutAll: boolean, graph: boolean, open: Record<string, boolean|undefined>, rawSide: string, xrow: Record<number, boolean|undefined>, range: number}} */
var S = { sid: sessionFromURL(), id: null, tab: 'request', toolsAll: false, cutAll: false, graph: false, open: {}, rawSide: 'request', xrow: {}, range: 7 };

var PV = /** @type {Record<string, number|undefined>} */ ({}), PVT = /** @type {Record<string, number|undefined>} */ ({});
/** @type {captureView|null} */
var CAP = null;                // the open inspector's fetched capture, cached across tab switches
/** @type {captureView} */
var MISSING = { missing: true }; // /api/capture said 404 — the capture row was deleted

/* Every id passed to $ is one this file or index.html writes, so querySelector
   cannot miss — with two exceptions, #toolsall and #cutall, which exist only once
   their table overflows, and whose callers check for them. */
/** @param {string} s @returns {HTMLElement} */
function $(s) { return /** @type {HTMLElement} */ (document.querySelector(s)); }
/** @param {*} s @returns {string} */
function esc(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}
/** @param {number|undefined} n @returns {string} */
function fT(n) { n = n || 0; if (n < 1000) return String(Math.round(n)); if (n < 1e6) return (n / 1e3).toFixed(1) + 'k'; return (n / 1e6).toFixed(2) + 'M'; }
/** @param {number|undefined} b @returns {string} */
function fK(b) { b = b || 0; if (b < 1024) return b + ' B'; if (b < 1048576) return (b / 1024).toFixed(1) + ' KB'; return (b / 1048576).toFixed(1) + ' MB'; }
/** @param {number|undefined} n @returns {string} */
function fC(n) { return String(Math.round(n || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ','); }
/** @param {number|undefined} v @param {number} d @returns {string} */
function fM(v, d) { return '$' + (v || 0).toFixed(d); }

/* A figure that rounds to $0.00 is worse than no figure: it says "this is free"
   about something that is not. Anything under a cent keeps the digits that make
   it a number — which is most of a cut list, one schema at a time. */
/** @param {number|undefined} v @returns {string} */
function fUsd(v) {
  v = v || 0;
  if (v > 0 && v < 0.01) return fM(v, 4);
  return fM(v, 2);
}
/** @param {number} n @returns {string} */
function p2(n) { return (n < 10 ? '0' : '') + n; }
/** @param {number} t @returns {string} */
function clock(t) { var d = new Date(t); return p2(d.getHours()) + ':' + p2(d.getMinutes()) + ':' + p2(d.getSeconds()); }
/** @param {number} t @returns {string} */
function clockHM(t) { var d = new Date(t); return p2(d.getHours()) + ':' + p2(d.getMinutes()); }
/** @param {string} s @param {number} n @returns {string} */
function trunc(s, n) { s = String(s || ''); return s.length > n ? s.slice(0, n - 1) + '…' : s; }
/** @param {string} m @returns {string} */
function shortModel(m) { return String(m || '').replace('claude-', '').replace(/-\d{8}$/, ''); }
/** @param {number} v @param {number} t @returns {string} */
function pctOf(v, t) { return t > 0 ? ((v / t) * 100).toFixed(1) + '%' : '0%'; }
/** @param {number} v @param {number} t @returns {string} */
function pct0(v, t) { return t > 0 ? Math.round((v / t) * 100) + '%' : '0%'; }
/** @param {string} s @returns {number} */
function tms(s) { return new Date(s).getTime(); }
/** @param {reqRow} r @returns {boolean} */
function ok(r) { return r.status < 400; }
/** @param {number} ms @returns {string} */
function dur(ms) {
  var s = Math.round((ms || 0) / 1000);
  if (s < 60) return s + 's';
  if (s < 3600) return Math.floor(s / 60) + 'm ' + p2(s % 60) + 's';
  return Math.floor(s / 3600) + 'h ' + p2(Math.floor((s % 3600) / 60)) + 'm';
}

/* A 4xx that is not a failure: the 429 the API answers a quota probe with. Claude
   Code opens every session with a max_tokens:1 "quota" ping, reads the 429 as its
   answer and carries on. Drawn red it would put an error on the board at the start
   of every session — which is how a real error learns to look normal. Only the 429
   is forgiven: a probe that 401s means the key is dead, and that stays red. */
/** @param {reqRow} r @returns {boolean} */
function benign(r) { return !!r.probe && r.status === 429; }
/** @param {reqRow} r @returns {boolean} */
function isErr(r) { return !ok(r) && !benign(r); }
/** @param {reqRow} r @returns {number} */
function rowCost(r) { var c = r.cost || {}; return (c.in || 0) + (c.read || 0) + (c.write || 0) + (c.out || 0); }

/* Fade a number in when it moves by more than 10% — the eye catches the change
   without the page ever animating a still figure. */
/** @param {string} k @param {number} num @returns {string} */
function anim(k, num) {
  var now = Date.now(), prev = PV[k];
  if (prev === undefined || Math.abs(num - prev) / Math.max(Math.abs(prev), 1e-9) > 0.1) PVT[k] = now;
  PV[k] = num;
  return (now - (PVT[k] || 0) < 750) ? 'ttNum 0.5s ease-out' : 'none';
}

/** @param {string} c @returns {string} */
function swatch(c) { return '<span class="sw" style="background:' + c + '"></span>'; }
/** @param {number} rd @param {number} inp @param {number} wr @param {number} out @returns {string} */
function mix(rd, inp, wr, out) {
  var t = rd + inp + wr + out;
  return '<span class="mixbar">' +
    '<span style="width:' + pctOf(rd, t) + ';background:' + MRD + '"></span>' +
    '<span style="width:' + pctOf(inp, t) + ';background:' + MIN + '"></span>' +
    '<span style="width:' + pctOf(wr, t) + ';background:' + MWR + '"></span>' +
    '<span style="width:' + pctOf(out, t) + ';background:' + MOU + '"></span></span>';
}
/** @param {[string, string][]} pairs colour, label @returns {string} */
function legend(pairs) {
  var h = '<div class="leg">', i;
  for (i = 0; i < pairs.length; i++) h += '<div>' + swatch(pairs[i][0]) + '<span class="t">' + esc(pairs[i][1]) + '</span></div>';
  return h + '</div>';
}
/** @type {[string, string][]} */
var SPEND_LEGEND = [[MRD, 'cache read'], [MIN, 'fresh input'], [MWR, 'cache write'], [MOU, 'output']];
/** @type {[string, string][]} */
var SHAPE_LEGEND = [[MTH, 'thinking'], [MOU, 'text'], [MIN, 'tool_use']];
/** @type {[string, string][]} */
var CACHE_LEGEND = [[MRD, 'read'], [MWR, 'write / break'], [MIN, 'billed fresh'], [MER, 'error']];
/** @type {[string, string][]} */
var COMP_LEGEND = [[CTOOL, 'tool schemas'], [CSYS, 'system'], [CHIST, 'history']];

/* ---------- tooltip ---------- */
var TIP = /** @type {HTMLElement} */ (/** @type {any} */ (null));
/** Everything a hover handler needs, indexed the way the DOM data- attributes index it.
 * @type {{buckets: bucket[], rows: reqRow[], cache: cacheEvent[], buck: traceBucket[], days: dayBucket[]}} */
var TIPDATA = { buckets: [], rows: [], cache: [], buck: [], days: [] };

/** @param {string} title @param {string[][]} rows colour, name, value @param {string} foot @returns {string} */
function tipHtml(title, rows, foot) {
  var h = '<div class="t">' + esc(title) + '</div>', i;
  for (i = 0; i < rows.length; i++) {
    h += '<div class="r">' + swatch(rows[i][0]) + '<span class="n">' + esc(rows[i][1]) + '</span><span class="v">' + esc(rows[i][2]) + '</span></div>';
  }
  if (foot) h += '<div class="f">' + esc(foot) + '</div>';
  return h;
}
/** @param {MouseEvent} ev @param {string} html */
function showTip(ev, html) {
  var el = TIP;
  el.innerHTML = html;
  el.style.display = 'block';
  var r = /** @type {HTMLElement} */ (ev.currentTarget).getBoundingClientRect(), w = el.offsetWidth, h = el.offsetHeight;
  var x = r.left + r.width / 2 - w / 2;
  if (x < 8) x = 8;
  if (x + w > window.innerWidth - 8) x = window.innerWidth - 8 - w;
  var y = r.top - h - 10;
  if (y < 8) y = r.bottom + 10;
  el.style.left = Math.round(x) + 'px';
  el.style.top = Math.round(y) + 'px';
}
function hideTip() { TIP.style.display = 'none'; }

/* What the reply did, parsed into tag/name/args for the trace row and the
   inspector title. op is "tool_use · DesignSync — args", or the bare stop_reason
   when the reply was text. Errors speak through their status instead. */
/** @param {reqRow} q @returns {opInfo} */
function opParts(q) {
  if (q.probe) {
    if (benign(q)) return { tag: 'probe', tagC: '#7f7f7f', name: '', args: 'quota check · 429 is the answer, not a failure' };
    if (ok(q)) return { tag: 'probe', tagC: '#7f7f7f', name: '', args: 'quota check' };
    // A probe that failed any other way is a real problem, and a loud one: a 401
    // here means the key is dead. Being a probe forgives the 429 and nothing else.
    return { tag: 'probe', tagC: MER, name: String(q.status), args: 'quota check failed · ' + (q.errType || q.errMsg || '') };
  }
  if (!ok(q)) return { tag: 'error', tagC: MER, name: String(q.status), args: q.errType || q.errMsg || '' };

  var op = q.op || '';
  var em = op.indexOf(' — '), head = em >= 0 ? op.slice(0, em) : op, args = em >= 0 ? op.slice(em + 3) : '';
  var dm = head.indexOf(' · ');
  if (dm < 0) {
    // No tool: the reply was text, and op is the stop_reason it ended on.
    return { tag: 'text', tagC: '#9f9f9f', name: '', args: head === 'end_turn' ? '' : head };
  }
  return { tag: head.slice(0, dm), tagC: MIN, name: head.slice(dm + 3), args: args };
}

/* ---------- overview ---------- */
/** @returns {string} */
function renderHome() {
  var ov = D.overview, i;
  var win = ov.windowMin || 10; // the server owns the window; 10 min is the default
  var winCap = 'last ' + win + ' min';

  var burnAvg = ov.burnAvg || 0;

  /* Could have saved: schemas that ship on every request and are never called,
     priced at the cadence the live sessions are actually running at. The only
     number on the page that is a claim rather than a measurement, so it names the
     session it is accusing. */
  var worst = sessionOf(ov.worstSid || '');
  var wasteSub = worst
    ? (ov.unusedCount || 0) + ' schemas never called · ' + fK(worst.unusedBytes) + '/req in “' + esc(trunc(worst.label, 30)) + '”'
    : 'no unused schemas in live sessions';

  var hitNow = ov.hitNow || 0, hitAvg = ov.hitAvg || 0, hitSub;
  if (!D.traced) hitSub = 'cache reads bill at 0.1× fresh input';
  else if (hitNow < hitAvg - 0.06) hitSub = '▾ vs ' + (hitAvg * 100).toFixed(0) + '% lifetime — uncached input bills at 1×';
  else hitSub = 'lifetime ' + (hitAvg * 100).toFixed(0) + '% · cache reads bill at 0.1×';

  var reqHr = ov.reqHr || 0, winReqs = ov.winReqs || 0;
  var reqSub = winReqs + (winReqs === 1 ? ' request' : ' requests') + ' in the ' + winCap + ' · ' + fM(ov.avgReq || 0, 4) + ' avg';

  var h = '<div class="wrap" style="padding-top:36px;padding-bottom:72px"><div class="tiles">';
  h += '<div><div class="cap">Today\'s total spend</div>' +
    '<div class="val" style="gap:8px"><span class="big" style="animation:' + anim('today-spend', ov.todayCost || 0) + '">' + fM(ov.todayCost || 0, 2) + '</span></div>' +
    '<div class="tile-sub" style="margin-top:13px;font-size:11.5px">priced requests since local midnight</div></div>';

  h += '<div class="tile' + (worst ? ' click" id="wasteopen' : '') + '" style="padding-top:5px" title="open the cut list">' +
    '<div class="cap">Could have saved</div>' +
    '<div class="val" style="gap:5px"><span class="mid" style="animation:' + anim('waste', ov.wasteHr || 0) + '">' + fM(ov.wasteHr || 0, 2) + '</span>' +
    '<span class="unit">/hr</span></div><div class="tile-sub">' + wasteSub + '</div></div>';

  h += '<div style="padding-top:5px"><div class="cap">Cache hit · ' + esc(winCap) + '</div>' +
    '<div class="val" style="gap:5px"><span class="mid" style="animation:' + anim('hit', hitNow) + '">' + (hitNow * 100).toFixed(1) + '</span>' +
    '<span class="unit">%</span></div><div class="tile-sub">' + esc(hitSub) + '</div></div>';

  h += '<div style="padding-top:5px"><div class="cap">Requests · ' + esc(winCap) + '</div>' +
    '<div class="val" style="gap:8px"><span class="mid" style="animation:' + anim('req', reqHr) + '">' + fC(reqHr) + '</span>' +
    '<span class="unit">/hr</span></div><div class="tile-sub">' + esc(reqSub) + '</div></div>';
  h += '</div>';

  /* spend timeline: 60 one-minute buckets, cost stacked by class, folded server-side */
  var B = ov.timeline || [], maxB = ov.peakMin || 0.001;
  TIPDATA.buckets = B;
  h += '<div style="margin-top:38px"><div class="hdrline">' +
    '<div class="cap">Spend · last 60 min <span class="sub">· peak ' + fM(ov.peakMin || 0, 2) + '/min · dashed line = lifetime avg</span></div>' +
    legend(SPEND_LEGEND) + '</div>' +
    '<div class="chart" style="height:96px">' +
    '<div class="grid" style="top:33%"></div><div class="grid" style="top:66%"></div>' +
    '<div class="grid" title="lifetime average" style="border-top:1px dashed rgba(255,255,255,0.22);bottom:' +
    Math.min(94, ((burnAvg / 60) / maxB) * 96).toFixed(1) + '%"></div>' +
    '<div class="cols">';
  for (i = 0; i < B.length; i++) {
    var b = B[i];
    h += '<div class="col" data-b="' + i + '">' +
      '<i style="height:' + hpc(b.costRead, maxB, 96) + ';background:' + MRD + '"></i>' +
      '<i style="height:' + hpc(b.costIn, maxB, 96) + ';background:' + MIN + '"></i>' +
      '<i style="height:' + hpc(b.costWrite, maxB, 96) + ';background:' + MWR + '"></i>' +
      '<i style="height:' + hpc(b.costOut, maxB, 96) + ';background:' + MOU + '"></i>' +
      '<div class="err" style="height:' + (b.err > 0 ? '3px' : '0px') + '"></div></div>';
  }
  h += '</div></div><div class="axis"><span>-60m</span><span>-45m</span><span>-30m</span><span>-15m</span><span>now</span></div></div>';

  h += sessionTable();
  return h + '</div>';
}
/** @param {number} v @param {number} max @param {number} span @returns {string} */
function hpc(v, max, span) { return (((v || 0) / (max || 1)) * span).toFixed(2) + '%'; }

/** @param {string} sid @returns {sessionRow|null} */
function sessionOf(sid) {
  for (var i = 0; i < D.sessions.length; i++) if (D.sessions[i].id === sid) return D.sessions[i];
  return null;
}

/* ---------- the sessions table ----------
   The front page. A request is not a thing anyone did; a session is. */
/** @returns {string} */
function sessionTable() {
  var R = D.sessions || [], i, day, lastDay = '';

  var h = '<div style="margin-top:50px"><div class="hdrline">' +
    '<div class="cap">Sessions <span class="sub">· click one to trace it</span></div>' +
    '<div class="note">' + fC(D.traced) + ' requests traced · ' + fM(D.cost, 2) + ' total' +
    (D.unpricedReqs > 0 ? ' · ' + fC(D.unpricedReqs) + ' unpriced' : '') + '</div></div>' +
    '<div class="sgrid shead"><span></span><span>Session</span><span>Token mix</span>' +
    '<span class="r">Hit</span><span class="r">$/hr</span><span class="r">Spent</span><span class="r">Tokens</span>' +
    '<span class="r">Unused</span><span class="r">Req · Err</span><span class="r">Last</span></div>';

  if (!R.length) {
    var at = D.port ? '<span class="m">ANTHROPIC_BASE_URL=http://localhost:' + esc(D.port) + '</span>' : 'this proxy';
    return h + '<div class="empty">No sessions yet. Point your client at ' + at + ' — the first one lands here within two seconds.</div></div>';
  }

  for (i = 0; i < R.length; i++) {
    day = sessionDay(R[i].last);
    if (day.key !== lastDay) {
      h += '<div class="sday"><span>' + esc(day.label) + '</span><i></i></div>';
      lastDay = day.key;
    }
    h += sessionRowHtml(R[i]);
  }
  return h + '</div>';
}

/** @param {string} last @returns {{key:string,label:string}} */
function sessionDay(last) {
  var at = new Date(last), now = new Date();
  if (isNaN(at.getTime())) return { key: 'unknown', label: 'Unknown date' };

  var key = at.getFullYear() + '-' + at.getMonth() + '-' + at.getDate();
  var today = now.getFullYear() + '-' + now.getMonth() + '-' + now.getDate();
  now.setDate(now.getDate() - 1);
  var yesterday = now.getFullYear() + '-' + now.getMonth() + '-' + now.getDate();
  if (key === today) return { key: key, label: 'Today' };
  if (key === yesterday) return { key: key, label: 'Yesterday' };
  return { key: key, label: at.toLocaleDateString(undefined, { weekday: 'long', month: 'short', day: 'numeric', year: 'numeric' }) };
}

/** @param {sessionRow} s @returns {string} */
function sessionRowHtml(s) {
  var t = s.tok || { in: 0, read: 0, write: 0, out: 0 };
  var tot = (t.in || 0) + (t.read || 0) + (t.write || 0) + (t.out || 0);

  var dotC = s.live ? MRD : '#4f4f4f';
  var dotA = s.live ? 'ttPulse 2.6s ease-in-out infinite' : 'none';
  var stC = s.live ? '#8fd8b8' : '#5f5f5f';
  var stTx = s.live ? 'live' : 'idle';

  /* A session with no rate for its model shows a badge, never a dollar figure —
     a $0.00 here would be a lie about a model we have no price for. */
  var spent = s.priced ? fM(s.cost, 2) : '<span class="badge unp">unpriced</span>';
  var rate = (s.live && s.rateHr > 0) ? fM(s.rateHr, 2) : '—';

  /* What this session ships on every request and has never once called. The bytes
     are the actionable number: it is what you would delete. */
  var unused = s.unused > 0
    ? '<span title="' + esc(s.unused + ' schemas never called') + '">' + fK(s.unusedBytes) + '</span>'
    : '—';

  /* Subagent sessions are folded into this row; the chip is the only trace of
     them here — the drill-down lives in the session's trace. */
  var agents = s.agents > 0
    ? '<span class="agn">+' + s.agents + (s.agents === 1 ? ' agent' : ' agents') + '</span>'
    : '';

  return '<a class="sgrid srow" data-sid="' + esc(s.id) + '" href="' + esc(sessionURL(s.id)) + '">' +
    '<span class="state"><span class="dot" style="background:' + dotC + ';animation:' + dotA + '"></span>' +
    '<span class="tx" style="color:' + stC + '">' + stTx + '</span></span>' +
    '<span style="display:flex;align-items:center;gap:8px;min-width:0">' +
    '<span class="slabel" style="color:' + (s.live ? '#ececec' : '#a8a8a8') + '" title="' + esc(s.label) + '">' + esc(s.label) + '</span>' + agents + '</span>' +
    mix(t.read || 0, t.in || 0, t.write || 0, t.out || 0) +
    '<span class="snum" style="color:#cfcfcf">' + (s.hit * 100).toFixed(0) + '%</span>' +
    '<span class="snum" style="color:#ececec">' + rate + '</span>' +
    '<span class="snum" style="color:#cfcfcf">' + spent + '</span>' +
    '<span class="snum" style="color:#9f9f9f">' + fT(tot) + '</span>' +
    '<span class="snum" style="color:#9f9f9f">' + unused + '</span>' +
    '<span class="snum" style="color:#9f9f9f">' + s.req + (s.err > 0 ? '<span style="color:' + MER + '"> · ' + s.err + '</span>' : '') + '</span>' +
    '<span class="snum" style="color:#6f6f6f">' + esc(s.idle) + '</span></a>';
}

/* ---------- history ----------
   What this has cost over time. The overview answers "what is running right
   now"; this answers "what did last week cost", which is the question a bill
   asks.

   Every figure here is a sum of days the server already priced one request at a
   time (internal/api/history.go). Summing priced days is additive; pricing a
   summed day is not — so the rollup only ever happens in that order, and nothing
   on this screen re-derives a price. */

/* Model colours, cycled in rank order. Unlike the token-class palette these
   carry no fixed meaning: every row is named right next to its swatch. */
var MODELC = [MIN, MRD, MWR, MTH, MOU, '#8f8f8f'];

/** @param {dayBucket} b @returns {number} */
function dayCost(b) { var c = b.cost; return (c.in || 0) + (c.read || 0) + (c.write || 0) + (c.out || 0); }
/** Cache reads over everything that could have been a read — the ratio Go folds
 * per session, over a whole range of days. @param {tokens} t @returns {number} */
function hitOf(t) { var all = (t.in || 0) + (t.read || 0) + (t.write || 0); return all > 0 ? (t.read || 0) / all : 0; }
/** @param {number} usd @param {number} n @returns {number} */
function perDay(usd, n) { return n > 0 ? usd / n : 0; }
/** @param {number} t @returns {dayBucket} */
function zeroDay(t) {
  return {
    t: t, cost: { in: 0, read: 0, write: 0, out: 0 }, tok: { in: 0, read: 0, write: 0, out: 0 },
    n: 0, err: 0, sessions: 0, unpriced: 0, models: {}
  };
}
/** @param {number} t @param {Intl.DateTimeFormatOptions} opts @returns {string} */
function dstr(t, opts) { return new Date(t).toLocaleDateString(undefined, opts); }
/** One comparison window, worded. @returns {string} */
function histPeriod() { return S.range === 365 ? '12 months' : S.range + ' days'; }
/** How a bucket names itself, in the grain the switcher is on. @param {number} t @returns {string} */
function histLabel(t) {
  return S.range === 365
    ? dstr(t, { month: 'short', year: 'numeric' })
    : dstr(t, { weekday: 'short', month: 'short', day: 'numeric' });
}

/* Adding buckets up is the one aggregation this file is allowed to do, and it is
   safe for exactly one reason: the non-additive part — a rate that depends on a
   request's own size and its own timestamp — already happened, per request, in Go. */
/** @param {dayBucket[]} list @param {number} t @returns {dayBucket} */
function sumDays(list, t) {
  var b = zeroDay(t), i, m, d;
  for (i = 0; i < list.length; i++) {
    d = list[i];
    b.cost.in += d.cost.in; b.cost.read += d.cost.read; b.cost.write += d.cost.write; b.cost.out += d.cost.out;
    b.tok.in += d.tok.in; b.tok.read += d.tok.read; b.tok.write += d.tok.write; b.tok.out += d.tok.out;
    b.n += d.n; b.err += d.err; b.unpriced += d.unpriced;
    // Distinct sessions can only be added up: a session that ran past midnight
    // counts on each day it worked, which is all a day bucket knows about it.
    b.sessions += d.sessions;
    for (m in d.models) b.models[m] = (b.models[m] || 0) + d.models[m];
  }
  return b;
}

/** @param {dayBucket[]} days oldest first @returns {dayBucket[]} */
function byMonth(days) {
  var out = [], group = [], key = '', i, d, k;
  for (i = 0; i < days.length; i++) {
    d = new Date(days[i].t);
    k = d.getFullYear() + '-' + d.getMonth();
    if (k !== key && group.length) { out.push(sumDays(group, monthStart(group[0].t))); group = []; }
    key = k;
    group.push(days[i]);
  }
  if (group.length) out.push(sumDays(group, monthStart(group[0].t)));
  return out;
}
/** @param {number} t @returns {number} */
function monthStart(t) { var d = new Date(t); return new Date(d.getFullYear(), d.getMonth(), 1).getTime(); }

/* The buckets one switcher position shows. skip 0 is the visible period and 1
   the one before it — the same slicing both times, so a comparison can never be
   against a differently-shaped window.

   A day the server has no bucket for is a day nobody worked, and it gets a zero
   here so a day off draws as the $0 it was instead of vanishing. The walk steps
   by calendar day rather than by 86.4M ms: a DST change makes a day 23 or 25
   hours long, and a fixed step would drift off midnight within a season. */
/** @param {number} skip @returns {dayBucket[]} */
function histRange(skip) {
  var out = [], i;
  if (S.range === 365) {
    // The newest twelve months — which drops the partial month recording began
    // in as soon as there is a thirteenth. Half a month drawn beside twelve
    // whole ones reads as a collapse in spend that never happened.
    var M = byMonth(H), end = M.length - skip * 12;
    return M.slice(Math.max(0, end - 12), Math.max(0, end));
  }
  var byT = /** @type {Record<number, dayBucket|undefined>} */ ({});
  for (i = 0; i < H.length; i++) byT[H[i].t] = H[i];
  var today = new Date();
  var from = new Date(today.getFullYear(), today.getMonth(), today.getDate() - S.range * (skip + 1) + 1);
  for (i = 0; i < S.range; i++) {
    var t = new Date(from.getFullYear(), from.getMonth(), from.getDate() + i).getTime();
    out.push(byT[t] || zeroDay(t));
  }
  return out;
}

/** @param {dayBucket[]} B @returns {string} */
function rangeSwitcher(B) {
  /** @type {[number, string][]} */
  var opts = [[7, '7 days'], [30, '30 days'], [90, '90 days'], [365, '12 months']];
  var h = '<div class="ranges">', i;
  for (i = 0; i < opts.length; i++) {
    h += '<button type="button" data-r="' + opts[i][0] + '" class="' + (S.range === opts[i][0] ? 'on' : '') + '">' +
      esc(opts[i][1]) + '</button>';
  }
  var span = B.length ? histLabel(B[0].t) + ' — ' + histLabel(B[B.length - 1].t) : '';
  return h + '<span class="span">' + esc(span) + '</span></div>';
}

/** @returns {string} */
function renderHistory() {
  var i, b;
  if (!H.length) {
    return '<div class="wrap" style="padding-top:30px">' + rangeSwitcher([]) +
      '<div class="note" style="margin-top:34px">reading the day buckets…</div></div>';
  }

  var B = histRange(0), P = histRange(1), monthly = S.range === 365;
  var span = sumDays(B, 0), before = sumDays(P, 0);
  /* Nothing is compared against a period of a different length, or an empty one.
     "+∞%" against a week that predates the database is arithmetic, not a fact
     about spend — and one rule here means the tile and the card cannot disagree
     about whether there is anything to compare against. */
  var prev = /** @type {dayBucket|null} */ ((P.length === B.length && dayCost(before) > 0) ? before : null);
  var total = dayCost(span), avg = total / B.length;
  var peak = B[0];
  for (i = 1; i < B.length; i++) if (dayCost(B[i]) > dayCost(peak)) peak = B[i];
  TIPDATA.days = B;

  var h = '<div class="wrap" style="padding-top:30px;padding-bottom:72px">' + rangeSwitcher(B);

  /* ---- tiles ---- */
  var deltaTile;
  if (prev) {
    var prevTotal = dayCost(prev), pc = Math.round(((total - prevTotal) / prevTotal) * 100);
    deltaTile = '<div><div class="cap">vs previous ' + esc(histPeriod()) + '</div>' +
      '<div class="val" style="gap:5px"><span class="mid" style="color:' + (pc >= 0 ? MER : MRD) + '">' +
      (pc >= 0 ? '&#9650;' : '&#9662;') + ' ' + Math.abs(pc) + '</span><span class="unit">%</span></div>' +
      '<div class="tile-sub">' + fM(prevTotal, 2) + ' the ' + esc(histPeriod()) + ' before</div></div>';
  } else {
    deltaTile = '<div><div class="cap">Requests</div>' +
      '<div class="val"><span class="mid">' + fC(span.n) + '</span></div>' +
      '<div class="tile-sub">' + fC(span.err) + (span.err === 1 ? ' error' : ' errors') + ' across the range</div></div>';
  }

  h += '<div class="tiles" style="margin-top:34px">' +
    '<div><div class="cap">Total spend</div>' +
    '<div class="val"><span class="big" style="animation:' + anim('hist-total', total) + '">' + fM(total, 2) + '</span></div>' +
    '<div class="tile-sub">priced requests only · ' + fC(span.n) + ' requests' +
    (span.unpriced > 0 ? ' · ' + fC(span.unpriced) + ' unpriced' : '') + '</div></div>' +

    '<div style="padding-top:5px"><div class="cap">Average</div>' +
    '<div class="val" style="gap:5px"><span class="mid">' + fM(avg, 2) + '</span>' +
    '<span class="unit">' + (monthly ? '/mo' : '/day') + '</span></div>' +
    '<div class="tile-sub">cache hit ' + pct0(span.tok.read, span.tok.in + span.tok.read + span.tok.write) +
    ' over the range</div></div>' +

    '<div style="padding-top:5px"><div class="cap">Most expensive ' + (monthly ? 'month' : 'day') + '</div>' +
    '<div class="val"><span class="mid">' + fM(dayCost(peak), 2) + '</span></div>' +
    // With nothing spent there is no most expensive anything, and naming the
    // first bucket in the range would be picking one at random.
    '<div class="tile-sub">' + (total > 0 ? esc(histLabel(peak.t)) : '—') + '</div></div>' +

    deltaTile + '</div>';

  /* ---- the bars ---- */
  var max = 0;
  for (i = 0; i < B.length; i++) if (dayCost(B[i]) > max) max = dayCost(B[i]);
  var scale = (max || 0.001) * 1.12; // headroom, so the tallest bar's own figure has somewhere to sit

  h += '<div style="margin-top:44px"><div class="hdrline">' +
    '<div class="cap">Spend · ' + (monthly ? 'by month' : 'by day') + ' <span class="sub">· peak ' +
    fM(max, 2) + (monthly ? '/mo' : '/day') + ' · dashed line = average' +
    // Never a silent $0: if anything in range had no rate, the total below it is short.
    (span.unpriced > 0 ? ' · ' + fC(span.unpriced) + ' unpriced ' + (span.unpriced === 1 ? 'request' : 'requests') +
      ' not in these bars' : '') + '</span></div>' +
    legend(SPEND_LEGEND) + '</div>' +
    '<div class="chart" style="height:180px">' +
    '<div class="grid" style="top:25%"></div><div class="grid" style="top:50%"></div><div class="grid" style="top:75%"></div>' +
    '<div class="grid" title="average" style="border-top:1px dashed rgba(255,255,255,0.22);bottom:' +
    Math.min(97, (avg / scale) * 100).toFixed(1) + '%"></div><div class="cols">';

  for (i = 0; i < B.length; i++) {
    b = B[i];
    // Its own figure, but only while there are few enough bars for the labels to
    // sit apart. Past a dozen they collide into a grey smear. In full, never
    // rounded to the nearest dollar: "$0" over a day that cost $0.37 is the one
    // reading this page must not produce.
    var lbl = B.length <= 12
      ? '<span class="lbl" style="bottom:' + ((dayCost(b) / scale) * 100 + 2).toFixed(1) + '%">' + fUsd(dayCost(b)) + '</span>'
      : '';
    h += '<div class="col" data-h="' + i + '">' +
      '<i style="height:' + hpc(b.cost.read, scale, 100) + ';background:' + MRD + '"></i>' +
      '<i style="height:' + hpc(b.cost.in, scale, 100) + ';background:' + MIN + '"></i>' +
      '<i style="height:' + hpc(b.cost.write, scale, 100) + ';background:' + MWR + '"></i>' +
      '<i style="height:' + hpc(b.cost.out, scale, 100) + ';background:' + MOU + '"></i>' +
      // A month with one bad afternoon in it is not a month that errored, so the
      // tick is a daily fact only.
      '<div class="err" style="height:' + (!monthly && b.err > 0 ? '3px' : '0') + '"></div>' + lbl + '</div>';
  }
  h += '</div></div>' + histAxis(B, monthly) + '</div>';

  h += '<div class="two a"><div><div class="cap">Spend by model ' +
    '<span class="sub">· priced per request, summed over the range</span></div>' + modelRows(B, total) + '</div>' +
    '<div><div class="cap">Insights</div>' + histCards(span, prev, peak) + '</div></div>';

  return h + histTable(B, monthly) + '</div>';
}

/** @param {dayBucket[]} B @param {boolean} monthly @returns {string} */
function histAxis(B, monthly) {
  var h = '<div class="axis">', i, step;
  if (monthly) {
    for (i = 0; i < B.length; i++) h += '<span>' + esc(dstr(B[i].t, { month: 'short' })) + '</span>';
  } else if (S.range === 7) {
    for (i = 0; i < B.length; i++) h += '<span>' + esc(dstr(B[i].t, { weekday: 'short' })) + '</span>';
  } else {
    // Too many days to name them all: six or so posts, then where it ends.
    step = Math.ceil(B.length / 6);
    for (i = 0; i < B.length - step; i += step) h += '<span>' + esc(dstr(B[i].t, { month: 'short', day: 'numeric' })) + '</span>';
    h += '<span>today</span>';
  }
  return h + '</div>';
}

/* Which models the money went to. Go sums a day's spend per model as it prices
   each request; this only adds those days together and ranks them. */
/** @param {dayBucket[]} B @param {number} total @returns {string} */
function modelRows(B, total) {
  var by = sumDays(B, 0).models, names = [], m, i;
  for (m in by) names.push(m);
  names.sort(function (a, b) { return by[b] - by[a]; });

  var h = '<div style="margin-top:6px">';
  for (i = 0; i < names.length; i++) {
    var c = MODELC[i % MODELC.length], usd = by[names[i]];
    h += '<div class="mrow">' + swatch(c) +
      '<span class="m ell" style="font-size:12px;color:#c9c9c9" title="' + esc(names[i]) + '">' + esc(shortModel(names[i])) + '</span>' +
      '<span class="track"><i style="width:' + pctOf(usd, total) + ';background:' + c + '"></i></span>' +
      '<span class="num r">' + pct0(usd, total) + '</span>' +
      '<span class="num r" style="color:#ececec">' + fUsd(usd) + '</span></div>';
  }
  if (!names.length) h += '<div class="note" style="padding:12px 0">nothing priced in this range</div>';
  return h + '</div>';
}

/** @param {dayBucket} span @param {dayBucket|null} prev the equal-length period before it, when there is one @param {dayBucket} peak @returns {string} */
function histCards(span, prev, peak) {
  var monthly = S.range === 365, i, w;
  var hitNow = hitOf(span.tok), hitPrev = prev ? hitOf(prev.tok) : 0;

  /* Nothing ran, so there is nothing to be insightful about. Three confident
     cards over an empty range — a peak day, a heaviest weekday — would be the
     page claiming a pattern it has no evidence for. */
  if (span.n === 0) return '<div class="empty" style="padding:18px 0">Nothing recorded in this range yet.</div>';

  /* Which weekday costs the most, per day it happened. Read off the WHOLE
     history rather than the visible range: a rhythm is a claim about how someone
     works, and one week of data is not evidence of one. */
  var cost = [0, 0, 0, 0, 0, 0, 0], days = [0, 0, 0, 0, 0, 0, 0];
  for (i = 0; i < H.length; i++) {
    w = new Date(H[i].t).getDay();
    cost[w] += dayCost(H[i]);
    days[w]++;
  }
  var bi = new Date(H[0].t).getDay();
  for (i = 0; i < 7; i++) if (perDay(cost[i], days[i]) > perDay(cost[bi], days[bi])) bi = i;
  var bt = H[0].t; // a real date that fell on that weekday, so it can be named in the reader's locale
  for (i = 0; i < H.length; i++) if (new Date(H[i].t).getDay() === bi) { bt = H[i].t; break; }

  /** @type {[string, string, string, string, string][]} tag, colour, figure, title, body */
  var cards = [
    ['peak', MWR, fUsd(dayCost(peak)),
      histLabel(peak.t) + ' was the most expensive ' + (monthly ? 'month' : 'day'),
      fC(peak.n) + ' requests across ' + fC(peak.sessions) + (peak.sessions === 1 ? ' session' : ' sessions') + '.'],
    ['cache', MRD, Math.round(hitNow * 100) + '%',
      'Cache hit ' + (hitPrev > 0
        ? (hitNow >= hitPrev ? 'up from ' : 'down from ') + Math.round(hitPrev * 100) + '% the ' + histPeriod() + ' before'
        : 'across the range'),
      'Cache reads bill at 0.1× fresh input — the gap between these bars is mostly this number.'],
    ['rhythm', MIN, fUsd(perDay(cost[bi], days[bi])) + '/day',
      dstr(bt, { weekday: 'long' }) + 's are the heaviest day',
      'Averaged over the ' + fC(H.length) + (H.length === 1 ? ' day' : ' days') + ' that recorded anything.']
  ];

  var h = '<div class="cards one">';
  for (i = 0; i < cards.length; i++) {
    h += '<div class="card"><div class="top">' +
      '<span class="tag" style="color:' + cards[i][1] + '">' + esc(cards[i][0]) + '</span>' +
      '<span class="usd">' + esc(cards[i][2]) + '</span></div>' +
      '<div class="t">' + esc(cards[i][3]) + '</div><div class="b">' + esc(cards[i][4]) + '</div></div>';
  }
  return h + '</div>';
}

/** @param {dayBucket[]} B @param {boolean} monthly @returns {string} */
function histTable(B, monthly) {
  var h = '<div style="margin-top:52px"><div class="cap">' + (monthly ? 'Months' : 'Days') +
    ' <span class="sub">· newest first</span></div>' +
    '<div class="dgrid shead"><span>' + (monthly ? 'Month' : 'Day') + '</span><span class="r">Req</span>' +
    '<span class="r">Sessions</span><span>Cost mix</span><span class="r">Hit</span><span class="r">Cost</span></div>';

  for (var i = B.length - 1; i >= 0; i--) {
    var b = B[i], c = b.cost, t = b.tok;
    var when = monthly
      ? esc(dstr(b.t, { month: 'short', year: 'numeric' }))
      : '<span class="wd m">' + esc(dstr(b.t, { weekday: 'short' })) + '</span>' + esc(dstr(b.t, { month: 'short', day: 'numeric' }));
    h += '<div class="dgrid drow" data-h="' + i + '">' +
      '<span class="dday">' + when + '</span>' +
      '<span class="num r">' + fC(b.n) + '</span>' +
      '<span class="num r">' + fC(b.sessions) + '</span>' +
      mix(c.read || 0, c.in || 0, c.write || 0, c.out || 0) +
      '<span class="num r">' + pct0(t.read, t.in + t.read + t.write) + '</span>' +
      '<span class="num r" style="color:#ececec">' + fM(dayCost(b), 2) + '</span></div>';
  }
  return h + '</div>';
}

/* ---------- the session trace ----------
   One session: where its money went, and what to do about it. */
/** @returns {string} */
function renderTrace() {
  if (!T) return '<div class="wrap" style="padding-top:36px"><div class="note">loading the trace…</div></div>';
  var t = T;
  TIPDATA.rows = t.rows;
  TIPDATA.cache = t.cache;
  TIPDATA.buck = t.buckets;

  var h = '<div class="wrap" style="padding-top:26px">';
  h += '<div class="back" id="back">← all sessions</div>';
  h += '<div class="tracehd">' + esc(t.label) + '</div>';
  h += '<div class="tracemeta">' + esc(shortModel(t.model)) + ' · ' + esc(trunc(t.sid, 20)) + ' · ' +
    t.req + (t.req === 1 ? ' request' : ' requests') + ' · ' + dur(t.durMs) + ' ' +
    '<span style="color:' + (t.live ? MRD : '#6f6f6f') + '">' + (t.live ? 'live' : 'idle ' + esc(t.idle)) + '</span></div>';

  h += traceStats(t);
  h += sessionGraph(t);
  h += agentsPanel(t);
  h += insightCards(t);
  h += contextChart(t);

  h += '<div class="two a">' + shapeChart(t) + activityTable(t) + '</div>';
  h += cacheStrip(t);
  h += '<div class="two b">' + contextPanel(t) + cutPanel(t) + '</div>';
  h += traceList(t);

  return h + '</div>';
}

/* ---------- session graph ----------
   A context-style overview of the whole conversation. It is intentionally
   collapsed: a person scans the graph first, then opens only the turn whose
   operation flow they need to read. */
/** @param {traceView} t @returns {string} */
function sessionGraph(t) {
  var F = t.flow || [], i, j, n = (t.rows || []).length;
  if (!n) return '';

  var h = '<div class="graph-toggle"><button type="button" id="flowgraph" aria-expanded="' + (S.graph ? 'true' : 'false') + '">' +
    (S.graph ? '▾ hide session graph' : '▸ show session graph') + '</button><span class="note">' + n +
    (n === 1 ? ' operation' : ' operations') + ' · prompts, tool calls, results, subagents</span></div>';
  if (!S.graph) return h;
  if (!F.length) return h + '<div class="flow-graph"><div class="flow-graph-note">loading capture-backed session graph…</div></div>';

  h += '<section class="flow-graph" aria-label="Session execution graph"><div class="flow-graph-track">';
  for (i = 0; i < F.length; i++) {
    var turn = F[i], calls = turn.calls || [], results = turn.results || [];
    h += '<button type="button" class="flow-graph-node' + (turn.status >= 400 ? ' err' : '') + '" data-graph-id="' + esc(turn.id) + '">' +
      '<span class="flow-graph-head"><span>' + (i + 1) + '</span><time>' + esc(clock(tms(turn.time))) + '</time></span>' +
      '<strong title="' + esc(turn.ask || 'assistant turn') + '">' + esc(trunc(turn.ask || 'assistant turn', 48)) + '</strong>';
    if (results.length) {
      h += '<span class="flow-graph-results">';
      for (j = 0; j < results.length; j++) h += '<i>← ' + esc(results[j].name) + '</i>';
      h += '</span>';
    }
    if (calls.length) {
      h += '<span class="flow-graph-calls">';
      for (j = 0; j < calls.length; j++) h += '<i class="' + (calls[j].spawn ? 'spawn' : (calls[j].agent ? 'agent' : '')) + '">→ ' + esc(calls[j].name) + '</i>';
      h += '</span>';
    } else {
      h += '<span class="flow-graph-reply">reply</span>';
    }
    h += '</button>';
  }
  return h + '</div><div class="flow-graph-note">click an operation to open its causal detail in the trace below</div></section>';
}

/* ---------- causal operation flow ----------
   The table stays compact. Open a request to inspect only that operation's
   prompt, incoming results, outgoing tools, and any subagent it launched. */
/** @param {traceView} t @param {number} id @returns {string} */
function operationFlow(t, id) {
  var F = t.flow || [], turn = null, i, j;
  for (i = 0; i < F.length; i++) if (F[i].id === id) { turn = F[i]; break; }
  if (!turn) return '';

  var bad = turn.status >= 400;
  var h = '<section class="flow flow-inline"><div class="hdrline"><div class="cap">Operation flow ' +
    '<span class="sub">· prompt → agent action → tool result</span></div><div class="note">capture-backed causal detail</div></div>' +
    '<div class="flow-legend"><span><i class="flow-dot user"></i>prompt</span><span><i class="flow-dot result"></i>result returned</span>' +
    '<span><i class="flow-dot call"></i>agent call</span><span><i class="flow-dot spawn"></i>subagent spawned</span></div>' +
    '<div class="flow-list"><article class="flow-turn' + (bad ? ' err' : '') + '"><div class="flow-rail"><span></span></div><div class="flow-body">' +
    '<div class="flow-meta"><span class="m">' + esc(clock(tms(turn.time))) + '</span><span>this operation</span>' +
    '<span class="flow-status" style="color:' + (bad ? MER : MRD) + '">' + esc(String(turn.status)) + '</span></div>';

  if (turn.ask) h += '<div class="flow-ask"><span>USER</span><strong>' + esc(turn.ask) + '</strong></div>';
  if (!turn.captured) return h + '<div class="flow-missing">capture pruned · request facts remain below</div></div></article></div></section>';

  if (turn.results && turn.results.length) {
    h += '<div class="flow-results">';
    for (j = 0; j < turn.results.length; j++) {
      var result = turn.results[j];
      h += '<div class="flow-result"><span class="flow-arrow">↳</span><span class="flow-kind">RESULT</span>' +
        '<span class="m">' + esc(result.name) + '</span><span class="flow-detail">' + fK(result.bytes) + ' returned to the agent</span></div>';
    }
    h += '</div>';
  }

  if (turn.calls && turn.calls.length) {
    h += '<div class="flow-calls">';
    for (j = 0; j < turn.calls.length; j++) {
      var call = turn.calls[j], kind = call.spawn ? 'SPAWN' : (call.agent ? 'AGENT CALL' : 'TOOL');
      h += '<div class="flow-call' + (call.spawn ? ' spawn' : '') + '"><span class="flow-arrow">↳</span>' +
        '<span class="flow-kind">' + kind + '</span><span class="m flow-name">' + esc(call.name) + '</span>' +
        (call.summary ? '<span class="flow-detail" title="' + esc(call.summary) + '">' + esc(call.summary) + '</span>' : '') +
        (call.agentSid ? '<a class="flow-agent" data-sid="' + esc(call.agentSid) + '" href="' + esc(sessionURL(call.agentSid)) + '">trace subagent · ' + esc(call.agentLabel || call.agentSid) + ' →</a>' : '') +
        '</div>';
    }
    h += '</div>';
  } else if (!bad) {
    h += '<div class="flow-reply">agent replied without a tool call</div>';
  }
  return h + '</div></article></div></section>';
}

/** @param {traceView} t @returns {string} */
function traceStats(t) {
  var tk = t.tok, tokIn = (tk.in || 0) + (tk.read || 0) + (tk.write || 0);
  var ctxPct = t.contextWindow > 0 ? Math.round((t.ctx / t.contextWindow) * 100) : 0;
  var nAgents = (t.agents || []).length;

  /* Spent is the whole session's money — this conversation plus the subagents
     it spawned — because that is what the sessions table shows, and the two
     figures disagreeing would discredit both. The split is right under it. */
  var groupCost = (t.cost || 0) + (t.agentCost || 0);
  var spent = t.priced ? fM(groupCost, 2) : '<span class="badge unp">unpriced</span>';
  if (nAgents > 0 && t.priced) {
    spent += ' <span class="sub">· ' + fM(t.agentCost || 0, 2) + ' in ' + nAgents + (nAgents === 1 ? ' agent' : ' agents') + '</span>';
  }

  /** @type {[string, string, boolean][]} caption, value, lead */
  var cells = [
    ['Spent', spent, true],
    ['Tokens in', fT(tokIn), false],
    ['Tokens out', fT(tk.out || 0), false],
    ['Requests', t.req + (t.err > 0 ? '<span style="color:' + MER + '"> · ' + t.err + '</span>' : ''), false],
    ['Cache hit', (t.hit * 100).toFixed(0) + '%', false],
    ['Context window', fT(t.ctx) + ' <span class="sub">· ' + ctxPct + '%</span>', false],
    ['Shipped unused', t.unusedTok > 0 ? fT(t.unusedTok) + ' tok' : '—', false],
    ['Avg / request', t.priced ? fM(t.avgReq, 4) : '—', false],
    ['Duration', dur(t.durMs), false]
  ];

  var h = '<div class="stats">', i;
  for (i = 0; i < cells.length; i++) {
    h += '<div><div class="cap">' + esc(cells[i][0]) + '</div>' +
      '<div class="v' + (cells[i][2] ? ' lead' : '') + '">' + cells[i][1] + '</div></div>';
  }
  return h + '</div>';
}

/* The subagents this session spawned. Each is its own conversation with its own
   cache story, so its requests are NOT mixed into the trace below — it gets a
   summary row here, and a click opens its own trace. */
/** @param {traceView} t @returns {string} */
function agentsPanel(t) {
  var A = t.agents || [], i;
  if (!A.length) return '';

  var h = '<div style="margin-top:38px"><div class="hdrline">' +
    '<div class="cap">Subagents <span class="sub">· spawned by this session · click one to trace it</span></div>' +
    '<div class="note">' + A.length + (A.length === 1 ? ' agent' : ' agents') + ' · ' +
    (t.agentReq || 0) + ' requests · ' + fM(t.agentCost || 0, 2) + '</div></div>';

  for (i = 0; i < A.length; i++) {
    var a = A[i];
    var tk = a.tok || { in: 0, read: 0, write: 0, out: 0 };
    var tot = (tk.in || 0) + (tk.read || 0) + (tk.write || 0) + (tk.out || 0);
    var cost = a.priced ? fUsd(a.cost) : '<span class="badge unp">unpriced</span>';
    h += '<a class="agrow" data-sid="' + esc(a.sid) + '" href="' + esc(sessionURL(a.sid)) + '">' +
      '<span class="state"><span class="dot" style="background:' + (a.live ? MRD : '#4f4f4f') +
      ';animation:' + (a.live ? 'ttPulse 2.6s ease-in-out infinite' : 'none') + '"></span></span>' +
      '<span class="slabel" style="font-size:12.5px;color:#c9c9c9" title="' + esc(a.label) + '">' + esc(a.label) + '</span>' +
      '<span class="m ell" style="font-size:10.5px;color:#7a7a7a">' + esc(shortModel(a.model)) + '</span>' +
      '<span class="snum" style="color:#9f9f9f">' + a.req + (a.err > 0 ? '<span style="color:' + MER + '"> · ' + a.err + '</span>' : '') + '</span>' +
      '<span class="snum" style="color:#9f9f9f">' + fT(tot) + '</span>' +
      '<span class="snum" style="color:#ececec">' + cost + '</span></a>';
  }
  return h + '</div>';
}

/* Do next: the advice, priced and ranked by Go. This function only knows how to
   word each kind — the decision to show it, and the number on it, are not made
   here and must not be. */
/** @param {traceView} t @returns {string} */
function insightCards(t) {
  var ins = t.insights || [];
  if (!ins.length) return '';

  var h = '<div style="margin-top:40px"><div class="cap">Do next ' +
    '<span class="sub">· ranked by $ impact · derived from this session\'s trace</span></div><div class="cards">';

  for (var i = 0; i < ins.length; i++) {
    var n = ins[i], tag = '', tagC = '#8a8a8a', title = '', body = '';

    if (n.kind === 'toolset') {
      tag = 'toolset'; tagC = MWR;
      title = 'Trim ' + n.n + ' never-called ' + (n.n === 1 ? 'schema' : 'schemas');
      body = groupCut(t) + ' ride on every request and were never invoked. ' +
        'Toggle the integration off for this task — the cut list below prices each schema.';
    } else if (n.kind === 'explore') {
      tag = 'context'; tagC = MIN;
      title = 'Exploration output is filling the context';
      body = t.exploreCalls + ' Read / Grep / Glob calls returned ' + fK(t.exploreBytes) + ' — ' +
        pct0(t.exploreBytes, t.resultBytes) + ' of all tool output, re-read on every request after. ' +
        'A code-index MCP or tighter reads would cut most of it.';
    } else if (n.kind === 'thinking') {
      tag = 'behavior'; tagC = MTH;
      title = 'Check if the thinking earns its keep';
      body = n.n + '% of output tokens are thinking (' + fT(t.out.think) + ') — output bills 5× input and never caches. ' +
        'Spot-check the trace: fine for hard debugging, wasted on mechanical steps.';
    } else if (n.kind === 'truncate') {
      tag = 'behavior'; tagC = MER;
      title = n.n + (n.n === 1 ? ' reply' : ' replies') + ' cut off at max_tokens';
      body = 'Red ticks in the stop_reason strip — the turn re-ran to finish what was truncated. ' +
        'Raise max_tokens or split the ask into smaller steps.';
    } else if (n.kind === 'cache') {
      tag = 'cache'; tagC = MRD;
      title = t.stateless ? 'A dynamic value is poisoning the cache' : 'Cache breaks are re-billing history';
      body = t.stateless
        ? 'The cached head differs on every request, so the full context re-bills at 1× each call — see the cache strip below. ' +
          'Keep tools + system byte-stable; push dynamic values to the end of messages.'
        : n.n + (n.n === 1 ? ' break' : ' breaks') + ' this session — each one re-writes the whole prefix. ' +
          'Idle gaps past the 5-minute TTL do it on their own: pausing often? Use the 1h TTL.';
    } else if (n.kind === 'history') {
      tag = 'context'; tagC = MIN;
      title = 'History is ' + fT(t.ctx) + ' tokens — compact';
      body = 'Every request re-reads the whole staircase; the slope in the chart above is the cost of not compacting. ' +
        '/compact (or a fresh session) drops most of it.';
    }

    h += '<div class="card"><div class="top">' +
      '<span class="tag" style="color:' + tagC + '">' + esc(tag) + '</span>' +
      '<span class="usd">' + fUsd(n.usd) + (n.perHr ? '/hr' : '') + '</span></div>' +
      '<div class="t">' + esc(title) + '</div><div class="b">' + esc(body) + '</div></div>';
  }
  return h + '</div></div>';
}

/* "plugin_slack MCP ×8 · built-in ×12" — which integrations the dead schemas came
   from, because that is the thing you would actually switch off. */
/** @param {traceView} t @returns {string} */
function groupCut(t) {
  var by = /** @type {Record<string, number>} */ ({}), order = [], i, n, name;
  for (i = 0; i < t.cut.length; i++) {
    n = t.cut[i].name;
    name = n.indexOf('mcp__') === 0 ? n.split('__')[1] + ' MCP' : 'built-in';
    if (by[name] === undefined) { by[name] = 0; order.push(name); }
    by[name]++;
  }
  order.sort(function (a, b) { return (by[b] || 0) - (by[a] || 0); });
  var out = [];
  for (i = 0; i < order.length; i++) out.push(order[i] + ' ×' + by[order[i]]);
  return out.join(' · ');
}

/* The staircase. Every request re-ships the whole conversation, so the bars climb
   even when nobody is doing anything expensive — and the cumulative-$ line over
   them is the integral of that climb, which is the thing worth seeing. */
/** @param {traceView} t @returns {string} */
function contextChart(t) {
  var R = t.rows, i, maxTok = 1;
  for (i = 0; i < R.length; i++) {
    var q = R[i].tok, tot = (q.read || 0) + (q.in || 0) + (q.write || 0) + (q.out || 0);
    if (tot > maxTok) maxTok = tot;
  }

  var h = '<div style="margin-top:44px"><div class="hdrline">' +
    '<div class="cap">Context per request · tokens <span class="sub">· peak ' + fT(maxTok) + '</span></div>' +
    legend(SPEND_LEGEND.concat([['rgba(255,255,255,0.5)', 'cumulative $']])) + '</div>' +
    '<div class="chart perreq" style="height:190px">' +
    '<div class="grid" style="top:25%"></div><div class="grid" style="top:50%"></div><div class="grid" style="top:75%"></div>' +
    '<div class="cols">';

  for (i = 0; i < R.length; i++) {
    var r = R[i], k = r.tok;
    h += '<div class="col" data-q="' + i + '">' +
      '<i style="height:' + hpc(k.read, maxTok, 100) + ';background:' + MRD + '"></i>' +
      '<i style="height:' + hpc(k.in, maxTok, 100) + ';background:' + MIN + '"></i>' +
      '<i style="height:' + hpc(k.write, maxTok, 100) + ';background:' + MWR + '"></i>' +
      '<i style="height:' + hpc(k.out, maxTok, 100) + ';background:' + MOU + '"></i>' +
      '<div class="err" style="height:' + (isErr(r) ? '100%' : '0') + '"></div></div>';
  }
  h += '</div>';

  // Compaction marks: the requests where the context collapsed instead of growing.
  for (i = 0; i < t.compacted.length; i++) {
    var at = R.length > 1 ? (t.compacted[i] / (R.length - 1)) * 100 : 0;
    h += '<div class="mark" style="left:' + at.toFixed(2) + '%"><span>compacted</span></div>';
  }

  /* The cumulative line. A running sum of costs Go already priced — presentation,
     the same class of arithmetic as a bar width. */
  var cum = 0, total = 0, pts = [];
  for (i = 0; i < R.length; i++) total += rowCost(R[i]);
  for (i = 0; i < R.length; i++) {
    cum += rowCost(R[i]);
    var x = R.length > 1 ? (i / (R.length - 1)) * 1000 : 0;
    var y = 190 - (total > 0 ? (cum / total) * 185 : 0);
    pts.push(x.toFixed(1) + ',' + y.toFixed(1));
  }
  h += '<svg class="cum" viewBox="0 0 1000 190" preserveAspectRatio="none">' +
    '<polyline points="' + pts.join(' ') + '" fill="none" stroke="rgba(255,255,255,0.5)" stroke-width="1" vector-effect="non-scaling-stroke"></polyline></svg>';

  h += '</div><div class="axis"><span>' + esc(clockHM(tms(t.first))) + '</span>' +
    '<span>' + (t.priced ? fM(t.cost, 2) + ' cumulative' : '') + '</span>' +
    '<span>' + esc(clockHM(tms(t.last))) + '</span></div></div>';
  return h;
}

/* What the replies were made of, and how they ended. The red ticks in the strip
   below are max_tokens: a truncated reply is a turn that has to run again. */
/** @param {traceView} t @returns {string} */
function shapeChart(t) {
  var R = t.rows, i, maxOut = 1;
  for (i = 0; i < R.length; i++) if ((R[i].tok.out || 0) > maxOut) maxOut = R[i].tok.out;

  var o = t.out, note = o.total > 0
    ? pct0(o.think, o.total) + ' thinking · ' + fT(o.total) + ' out'
    : 'no output tokens yet';

  var h = '<div><div class="hdrline">' +
    '<div class="cap">Output shape · per request <span class="sub">· ' + esc(note) + '</span></div>' +
    legend(SHAPE_LEGEND) + '</div>' +
    '<div class="chart perreq" style="height:104px"><div class="grid" style="top:50%"></div><div class="cols">';

  for (i = 0; i < R.length; i++) {
    var sh = R[i].shape || { think: 0, text: 0, tool: 0 };
    h += '<div class="col" data-q="' + i + '">' +
      '<i style="height:' + hpc(sh.think, maxOut, 100) + ';background:' + MTH + '"></i>' +
      '<i style="height:' + hpc(sh.text, maxOut, 100) + ';background:' + MOU + '"></i>' +
      '<i style="height:' + hpc(sh.tool, maxOut, 100) + ';background:' + MIN + '"></i></div>';
  }
  h += '</div></div>';

  h += '<div class="stops">';
  for (i = 0; i < R.length; i++) h += '<span style="background:' + stopColor(R[i]) + '"></span>';
  h += '</div><div class="stopnote"><span>stop_reason strip — red = max_tokens (cut off)</span><span>' +
    (o.truncated > 0 ? o.truncated + ' truncated' : 'none truncated') + '</span></div></div>';
  return h;
}

/** @param {reqRow} r @returns {string} */
function stopColor(r) {
  if (isErr(r)) return MER;
  if (r.stop === 'max_tokens') return MER;
  if (r.stop === 'tool_use') return 'rgba(90,162,247,0.55)';
  if (r.stop === 'end_turn') return 'rgba(255,255,255,0.28)';
  return 'rgba(255,255,255,0.10)';
}

/** @param {traceView} t @returns {string} */
function activityTable(t) {
  var A = t.activity || [], i, big = 0, total = 0;
  for (i = 0; i < A.length; i++) { total += A[i].cost; if (A[i].cost > big) big = A[i].cost; }

  /** @type {Record<string, string>} */
  var SUB = {
    explore: 'Read · Grep · Glob', edit: 'Edit · Write', run: 'Bash', plan: 'TodoWrite · Task',
    mcp: 'MCP servers', reply: 'no tool called', tool: 'other tools'
  };

  var h = '<div><div class="hdrline"><div class="cap">Spend by activity</div>' +
    '<div class="note">what each request was doing</div></div><div style="margin-top:8px">';
  for (i = 0; i < A.length; i++) {
    var a = A[i];
    h += '<div class="actrow">' +
      '<span class="n">' + esc(a.kind) + ' <span class="s">' + esc(SUB[a.kind] || '') + '</span></span>' +
      '<span class="m r" style="font-size:10.5px;color:#7a7a7a">' + a.n + '</span>' +
      '<span class="track"><i style="width:' + pctOf(a.cost, big) + '"></i></span>' +
      '<span class="m r" style="font-size:11px;color:#ececec">' + fUsd(a.cost) + '</span>' +
      '<span class="m r" style="font-size:10.5px;color:#6f6f6f">' + pct0(a.cost, total) + '</span></div>';
  }
  if (!A.length) h += '<div class="note" style="padding:12px 0">nothing billed in this session yet</div>';
  return h + '</div><div class="foot">Each request is attributed to the action its turn performed; whatever a call ' +
    'returns lands back in context and is re-read by every request after it.</div></div>';
}

/* One cell per request, coloured by what its cache did — and, under it, a row for
   every break naming the segment that caused it. This is the panel that pays for
   the whole proxy. */
/** @param {traceView} t @returns {string} */
function cacheStrip(t) {
  var C = t.cache || [], i;
  var head = t.breaks > 0
    ? t.breaks + (t.breaks === 1 ? ' break' : ' breaks') + ' · ' + fUsd(t.breakCost) + ' re-billed'
    : (t.stateless ? 'nothing cached — the whole context bills fresh, every call' : 'no breaks');

  var h = '<div style="margin-top:48px"><div class="hdrline">' +
    '<div class="cap">Cache · per request <span class="sub">· ' + esc(head) + '</span></div>' +
    legend(CACHE_LEGEND) + '</div><div class="cells">';
  for (i = 0; i < C.length; i++) h += '<span data-c="' + i + '" style="background:' + cacheColor(C[i]) + '"></span>';
  h += '</div>';

  var breaks = [];
  for (i = 0; i < C.length; i++) if (C[i].class === 'break' || (C[i].rebill || 0) > 0) breaks.push(i);
  if (!breaks.length) return h + '</div>';

  h += '<div class="breaks">';
  for (i = 0; i < breaks.length; i++) {
    var e = C[breaks[i]], r = t.rows[breaks[i]];
    h += '<div class="brow">' +
      '<span class="seq">#' + esc(e.id) + ' · ' + esc(clock(tms(r.time))) + '</span>' +
      '<span class="segs">' + segCells(e) + '</span>' +
      '<span class="tx">' + esc(causeText(e)) + '</span>' +
      '<span class="usd">' + fUsd(e.rebill || 0) + '</span></div>';
  }
  return h + '<div class="note" style="margin-top:9px;color:#4f4f4f">prefix segments · tools → system → messages — ' +
    'the red cell is where the cached head diverged; everything after it re-bills</div></div></div>';
}

/** @param {cacheEvent} e @returns {string} */
function cacheColor(e) {
  if (e.class === 'err') return MER;
  if (e.class === 'hit') return MRD;
  if (e.class === 'break' || e.class === 'prime') return MWR;
  if (e.class === 'fresh') return MIN;
  return 'rgba(255,255,255,0.08)';
}

/* Three cells — tools, system, messages — with the diverged one red. badIdx is the
   index in the prefix chain: 0 is the toolset, 1 the system prompt, 2+ a message. */
/** @param {cacheEvent} e @returns {string} */
function segCells(e) {
  var names = ['tools', 'system', 'messages'], h = '', i, bad;
  if (e.badIdx < 0) bad = -1;
  else if (e.badIdx === 0) bad = 0;
  else if (e.badIdx === 1) bad = 1;
  else bad = 2;
  for (i = 0; i < 3; i++) {
    // Everything from the bad segment on re-bills: the prefix match stops there.
    var c = bad < 0 ? 'rgba(255,255,255,0.12)' : (i < bad ? MRD : (i === bad ? MER : 'rgba(255,90,90,0.3)'));
    h += '<span title="' + names[i] + '" style="background:' + c + '"></span>';
  }
  return h;
}

/** @param {cacheEvent} e @returns {string} */
function causeText(e) {
  var gap = Math.round((e.gapMs || 0) / 60000);
  if (e.cause === 'gap') return 'idle ' + gap + 'm — past the 5-minute TTL, so the prefix went cold and re-wrote itself';
  if (e.cause === 'tools') return 'the toolset changed — every byte after it re-bills';
  if (e.cause === 'system') return 'the system prompt changed — the history after it re-bills';
  if (e.cause === 'msg') return 'a message in the history changed at segment ' + e.badIdx;
  if (e.class === 'fresh') return 'billed fresh — no cached prefix was in play at all';
  return 'the prefix was re-written';
}

/* What is actually in the context right now: the tool schemas, the system prompt,
   and the conversation — and which schemas among them have never been called. */
/** @param {traceView} t @returns {string} */
function contextPanel(t) {
  var b = t.ctxBytes, tot = b.total || (b.tools + b.system + b.messages) || 1, i;

  var h = '<div><div class="hdrline"><div class="cap">Context window · latest request</div>' +
    '<div class="note">' + fK(b.total) + ' shipped</div></div>' +
    '<div class="bar" style="height:7px;margin-top:14px">' +
    '<span style="width:' + pctOf(b.tools, tot) + ';background:' + CTOOL + '"></span>' +
    '<span style="width:' + pctOf(b.system, tot) + ';background:' + CSYS + '"></span>' +
    '<span style="width:' + pctOf(b.messages, tot) + ';background:' + CHIST + '"></span></div>';

  /** @type {[string, string, number][]} */
  var rows = [[CTOOL, 'tool schemas', b.tools], [CSYS, 'system prompt', b.system], [CHIST, 'message history', b.messages]];
  for (i = 0; i < rows.length; i++) {
    h += '<div class="ctxrow">' + swatch(rows[i][0]) +
      '<span class="n">' + esc(rows[i][1]) + '</span>' +
      '<span class="m r" style="font-size:11.5px;color:#c9c9c9">' + fK(rows[i][2]) + '</span>' +
      '<span class="m r" style="font-size:11.5px;color:#6f6f6f">' + pct0(rows[i][2], tot) + '</span></div>';
  }

  h += '<div class="cap" style="margin-top:30px">Largest schemas shipped</div>';
  if (t.captureGone) {
    return h + '<div class="warnbox">The capture these are read from was deleted, so the schemas cannot be itemized. ' +
      'The byte splits above come from the request row itself, which is never deleted.</div></div>';
  }
  if (!t.tools.length) return h + '<div class="note" style="padding:12px 0">this session ships no tool schemas</div></div>';

  var big = t.tools[0].bytes || 1;
  var shown = S.toolsAll ? t.tools.length : Math.min(7, t.tools.length);
  h += '<div style="margin-top:6px">';
  for (i = 0; i < shown; i++) {
    var tl = t.tools[i];
    h += '<div class="toprow">' +
      '<span class="m ell" style="font-size:11.5px;color:' + (tl.unused ? '#8a8a8a' : '#c9c9c9') + '" title="' + esc(tl.name) + '">' + esc(tl.name) + '</span>' +
      '<span class="track"><i style="width:' + pctOf(tl.bytes, big) + '"></i></span>' +
      '<span class="m r" style="font-size:11px;color:#9f9f9f">' + fK(tl.bytes) + '</span>' +
      '<span class="m r" style="font-size:9.5px;color:#6f6f6f">' + (tl.unused ? 'never called' : '') + '</span></div>';
  }
  h += '</div>';
  if (t.tools.length > 7) h += '<div class="more" id="toolsall">' + (S.toolsAll ? 'show less' : 'show all ' + t.tools.length + ' schemas') + '</div>';
  return h + '</div>';
}

/* The cut list: what to delete, and what deleting it is worth. */
/** @param {traceView} t @returns {string} */
function cutPanel(t) {
  var h = '<div><div class="cap">Cut list</div>';

  if (t.captureGone) {
    h += '<div class="warnbox">The capture the schemas are read from was deleted — there is nothing left to price.</div>';
  } else if (!t.cut.length) {
    h += '<div style="margin-top:16px;font-size:12.5px;color:#6f6f6f;line-height:1.6">' +
      (t.tools.length
        ? 'Every schema this session ships has been called at least once. Nothing to cut.'
        : 'This session ships no tool schemas.') + '</div>';
  } else {
    var per = t.live ? '/hr at the current cadence' : 'over this session';
    h += '<div class="cuthead">' + t.cut.length + ' schemas ship on every request and were never called.</div>' +
      '<div class="cutsub">' + fT(t.unusedTok) + ' tokens per request · ' + fUsd(t.cutUsd) + ' ' + esc(per) + '</div>' +
      '<div class="breaks">';

    var shown = S.cutAll ? t.cut.length : Math.min(6, t.cut.length);
    for (var i = 0; i < shown; i++) {
      var c = t.cut[i];
      h += '<div class="cutrow">' +
        '<span class="m ell" style="font-size:11.5px;color:#d8d8d8" title="' + esc(c.name) + '">' + esc(c.name) + '</span>' +
        '<span class="m r" style="font-size:11px;color:#9a9a9a">' + fK(c.bytes) + '</span>' +
        '<span class="m r" style="font-size:11px;color:#ececec">' + fUsd(c.usd) + '</span></div>';
    }
    h += '</div>';
    if (t.cut.length > 6) h += '<div class="more" id="cutall">' + (S.cutAll ? 'show less' : 'show all ' + t.cut.length) + '</div>';
  }

  return h + resultsTable(t) + '</div>';
}

/* Everything the tools have returned into this context — the rows that make the
   staircase climb. */
/** @param {traceView} t @returns {string} */
function resultsTable(t) {
  var R = t.results || [];
  if (!R.length) return '';
  var big = R[0].bytes || 1, i;

  var h = '<div style="margin-top:36px"><div class="hdrline">' +
    '<div class="cap">Tool results in context</div>' +
    '<div class="note">' + fK(t.resultBytes) + ' from ' + R.length + (R.length === 1 ? ' tool' : ' tools') + '</div></div>' +
    '<div style="margin-top:6px">';
  for (i = 0; i < Math.min(6, R.length); i++) {
    var r = R[i];
    h += '<div class="resrow">' +
      '<span class="m ell" style="font-size:11.5px;color:#d8d8d8" title="' + esc(r.name) + '">' + esc(r.name) + '</span>' +
      '<span class="m r" style="font-size:10.5px;color:#7a7a7a">' + r.n + '</span>' +
      '<span class="track"><i style="width:' + pctOf(r.bytes, big) + '"></i></span>' +
      '<span class="m r" style="font-size:11px;color:#9f9f9f">' + fK(r.bytes) + '</span>' +
      '<span class="m r" style="font-size:10.5px;color:#6f6f6f">' + pct0(r.bytes, t.resultBytes) + '</span></div>';
  }
  return h + '</div><div class="foot">Everything a tool returns stays in history and is re-read on every later ' +
    'request — the biggest rows here are what inflates the staircase above.</div></div>';
}

/* ---------- the trace list ----------
   Every request the session made, with the idle gaps left in: a five-minute pause
   is not nothing, it is a cache that went cold. */
/** @param {traceView} t @returns {string} */
function traceList(t) {
  var R = t.rows, i, maxMs = 1;
  for (i = 0; i < R.length; i++) if (R[i].ms > maxMs) maxMs = R[i].ms;

  var h = '<div style="margin-top:54px;padding-bottom:80px">' +
    '<div class="cap">Trace <span class="sub">· ' + R.length + (R.length === 1 ? ' request' : ' requests') +
    ' · click one to unfold it</span></div>';

  h += miniCharts(t);

  h += '<div class="qgrid qhead" style="margin-top:18px"><span>Time</span><span>Operation</span><span>St</span>' +
    '<span>Model</span><span class="r">Tokens in → out</span><span>Latency</span><span class="r">$</span></div>';

  for (i = 0; i < R.length; i++) {
    var e = t.cache[i];
    // An idle gap gets its own row. Past the TTL it is the reason the next request
    // re-wrote the whole prefix, and the cache strip above has already said so.
    if (e && e.gapMs > 60000) {
      h += '<div class="gap">⋯ idle ' + esc(dur(e.gapMs)) + (e.cause === 'gap' ? ' — the cached prefix went cold' : '') + '</div>';
    }
    h += traceRow(R[i], i, maxMs);
    if (S.xrow[R[i].id]) h += operationFlow(t, R[i].id) + traceDetail(R[i], e);
  }
  return h + '</div>';
}

/** @param {reqRow} q @param {number} i @param {number} maxMs @returns {string} */
function traceRow(q, i, maxMs) {
  var o = opParts(q), err = isErr(q), t = q.tok;
  var stC = err ? MER : (q.probe ? '#7f7f7f' : 'rgba(47,191,135,0.85)');
  var newTot = (t.in || 0) + (t.write || 0); // never summed with cache reads — they are different money
  var barW = Math.min(100, Math.max(1.5, ((q.ms || 0) / maxMs) * 100)).toFixed(1) + '%';
  var open = !!S.xrow[q.id];

  var op = '<span class="op">' +
    '<span class="m" style="flex:none;font-size:9px;color:' + (open ? '#d6d6d6' : '#5f5f5f') + '">' + (open ? '▾' : '▸') + '</span>';
  if (o.tag !== 'tool_use') op += '<span class="m" style="flex:none;font-size:9.5px;color:' + o.tagC + '">' + esc(o.tag) + '</span>';
  if (o.name) op += '<span class="m" style="flex:none;font-size:11px;color:#e4e4e4">' + esc(o.name) + '</span>';
  if (o.args) op += '<span class="m ell" style="font-size:10.5px;color:#7a7a7a">' + esc(o.args) + '</span>';
  if (q.stop === 'max_tokens') op += '<span class="m" style="flex:none;font-size:9px;color:' + MER + '">cut off</span>';
  op += '</span>';

  /* An unpriced row shows a badge, never a dollar figure — a $0.00 here would be
     a lie about a model we have no rate for. */
  var cost;
  if (!q.priced) cost = '<span class="badge unp">unpriced</span>';
  else if (err || q.probe) cost = '<span class="num" style="color:#5f5f5f">—</span>';
  else cost = '<span class="num" style="color:#e8e8e8">' + fM(rowCost(q), 4) + '</span>';

  return '<div class="qgrid qrow" data-id="' + esc(q.id) + '" data-q="' + i + '">' +
    '<span class="m" style="font-size:10.5px;color:#8f8f8f">' + esc(clock(tms(q.time))) + '</span>' +
    op +
    '<span class="m" style="font-size:9.5px;color:' + stC + '">' + esc(q.status) + '</span>' +
    '<span class="m ell" style="font-size:10.5px;color:#b9b9b9">' + esc(shortModel(q.model)) + '</span>' +
    '<span class="m r" style="font-size:10.5px;color:#b9b9b9">' + (err || q.probe ? '—' : fT(newTot) + ' → ' + fT(t.out || 0)) + '</span>' +
    '<span class="lat"><span class="track" style="flex:1 1 0"><i style="width:' + barW + ';background:' + (err ? MER : '#8f8f8f') + '"></i></span>' +
    '<span class="m r" style="flex:none;font-size:10px;color:#7f7f7f">' + fC(q.ms) + '</span></span>' +
    '<span class="r">' + cost + '</span></div>';
}

/** One line of an unfolded row's breakdown: swatch, name, and two right-aligned figures. */
/** @param {string} color @param {string} label @param {string} a @param {string} b @returns {string} */
function qxLane(color, label, a, b) {
  return '<div class="qxlane">' + swatch(color) +
    '<span style="color:#b5b5b5">' + esc(label) + '</span>' +
    '<span class="m r" style="color:#9f9f9f">' + a + '</span>' +
    '<span class="m r" style="color:#e0e0e0">' + b + '</span></div>';
}

/* The row, unfolded in place: everything the wire already knows about this one
   request — the bill by token class, where the shipped bytes went, what the
   output was spent on, what the cache did and why — parsed and laid out, without
   leaving the list. The captured bodies stay one click further, in the inspector:
   they need a second fetch, and most questions are answered before them. */
/** @param {reqRow} q @param {cacheEvent|undefined} e @returns {string} */
function traceDetail(q, e) {
  var t = q.tok, c = q.cost, sh = q.shape || { think: 0, text: 0, tool: 0 }, b = q.bytes;
  var h = '<div class="qx">';

  // The ask, in full — the op column can only gesture at it.
  if (q.label) h += '<div class="qxask"><span class="m" style="font-size:10px;color:#6e6e6e">asked · </span>' + esc(q.label) + '</div>';
  if (q.errMsg) h += '<div class="errbox" style="margin-top:10px"><div class="m" style="font-size:11.5px;color:#ff7a7a">' +
    esc(q.status + ' · ' + (q.errType || '')) + '</div>' +
    '<div style="margin-top:6px;font-size:12px;color:#c9c9c9;line-height:1.6">' + esc(q.errMsg) + '</div></div>';

  h += '<div class="qxgrid">';

  // ---- billing: the row's own quartet, tokens next to dollars ----
  var total = rowCost(q), denom = total > 0 ? total : 1;
  h += '<div><div class="cap">Billing · ' + esc(shortModel(q.model)) + '</div>';
  if (q.priced) {
    h += '<div class="m qxbig">' + fM(total, 4) + '</div>' +
      '<div class="bar" style="height:5px;margin-top:10px">' +
      '<span style="width:' + pctOf(c.read || 0, denom) + ';background:' + MRD + '"></span>' +
      '<span style="width:' + pctOf(c.in || 0, denom) + ';background:' + MIN + '"></span>' +
      '<span style="width:' + pctOf(c.write || 0, denom) + ';background:' + MWR + '"></span>' +
      '<span style="width:' + pctOf(c.out || 0, denom) + ';background:' + MOU + '"></span></div>';
  } else {
    h += '<div style="margin:10px 0 4px"><span class="badge unp">unpriced</span></div>';
  }
  h += '<div style="margin-top:6px">' +
    qxLane(MRD, 'cache read · 0.1×', fT(t.read), q.priced ? fM(c.read || 0, 4) : '—') +
    qxLane(MIN, 'fresh input · 1×', fT(t.in), q.priced ? fM(c.in || 0, 4) : '—') +
    qxLane(MWR, 'cache write · 1.25×', fT(t.write), q.priced ? fM(c.write || 0, 4) : '—') +
    qxLane(MOU, 'output', fT(t.out), q.priced ? fM(c.out || 0, 4) : '—') +
    '</div></div>';

  // ---- payload in, and what the output was spent on ----
  var btot = b.total || (b.tools + b.system + b.messages) || 1;
  h += '<div><div class="cap">Shipped · ' + fK(b.total) + '</div>' +
    '<div class="bar" style="height:5px;margin-top:10px">' +
    '<span style="width:' + pctOf(b.tools, btot) + ';background:' + CTOOL + '"></span>' +
    '<span style="width:' + pctOf(b.system, btot) + ';background:' + CSYS + '"></span>' +
    '<span style="width:' + pctOf(b.messages, btot) + ';background:' + CHIST + '"></span></div>' +
    '<div style="margin-top:6px">' +
    qxLane(CTOOL, 'tool schemas', fK(b.tools), pct0(b.tools, btot)) +
    qxLane(CSYS, 'system prompt', fK(b.system), pct0(b.system, btot)) +
    qxLane(CHIST, 'message history', fK(b.messages), pct0(b.messages, btot)) +
    '</div>';
  var otot = (sh.think || 0) + (sh.text || 0) + (sh.tool || 0);
  h += '<div class="cap" style="margin-top:16px">Output · ' + fT(t.out) + ' tok</div>';
  if (otot > 0) {
    h += '<div class="bar" style="height:5px;margin-top:10px">' +
      '<span style="width:' + pctOf(sh.think || 0, otot) + ';background:' + MTH + '"></span>' +
      '<span style="width:' + pctOf(sh.text || 0, otot) + ';background:' + MOU + '"></span>' +
      '<span style="width:' + pctOf(sh.tool || 0, otot) + ';background:' + MIN + '"></span></div>' +
      '<div style="margin-top:6px">' +
      qxLane(MTH, 'thinking', fT(sh.think), pct0(sh.think, otot)) +
      qxLane(MOU, 'text', fT(sh.text), pct0(sh.text, otot)) +
      qxLane(MIN, 'tool calls', fT(sh.tool), pct0(sh.tool, otot)) +
      '</div>';
  } else {
    h += '<div class="note" style="padding:8px 0">no output tokens</div>';
  }
  h += '</div>';

  // ---- what the cache did, and the clock ----
  h += '<div><div class="cap">Cache & timing</div>';
  if (e) {
    h += '<div style="display:flex;align-items:baseline;gap:10px;margin-top:10px">' +
      '<span class="chip" style="color:' + cacheColor(e) + ';border-color:rgba(255,255,255,0.14)">' + esc(e.class) + '</span>' +
      (e.rebill ? '<span class="m" style="font-size:11px;color:' + MER + '">' + fUsd(e.rebill) + ' re-billed</span>' : '') + '</div>' +
      '<div style="margin-top:9px;font-size:11.5px;color:#9f9f9f;line-height:1.55">' +
      esc(e.class === 'hit' ? 'the cached prefix matched — history billed at 0.1×' : causeText(e)) + '</div>';
    if (e.gapMs > 1000) h += '<div class="note" style="margin-top:7px">idle ' + esc(dur(e.gapMs)) + ' before this request</div>';
  }
  var rest = Math.max(0, (q.ms || 0) - (q.ttft || 0));
  h += '<div class="bar" style="height:5px;margin-top:14px">' +
    '<span style="width:' + pctOf(q.ttft || 0, q.ms || 1) + ';background:' + MIN + '"></span>' +
    '<span style="width:' + pctOf(rest, q.ms || 1) + ';background:rgba(90,162,247,0.32)"></span></div>' +
    '<div style="margin-top:6px">' +
    qxLane(MIN, 'first token', q.ttft > 0 ? fC(q.ttft) + ' ms' : '—', '') +
    qxLane('rgba(90,162,247,0.32)', 'full response', fC(q.ms) + ' ms', '') +
    '</div>' +
    '<div style="display:flex;flex-wrap:wrap;gap:7px;margin-top:12px">' +
    '<span class="chip" style="color:' + (q.stop === 'max_tokens' ? '#ff7a7a' : '#b5b5b5') +
    ';border-color:' + (q.stop === 'max_tokens' ? 'rgba(255,90,90,0.4)' : 'rgba(255,255,255,0.12)') + '">stop: ' +
    esc(ok(q) ? (q.stop || '—') : '—') + '</span>' +
    (q.aborted ? '<span class="chip" style="color:#ff7a7a;border-color:rgba(255,90,90,0.4)">aborted by client</span>' : '') +
    '</div></div>';

  h += '</div>'; // qxgrid
  h += '<div class="more" data-insp="' + esc(q.id) + '" style="margin-top:14px">request & response bodies →</div>';
  return h + '</div>';
}

/* Requests, errors and latency over the session's span — equal TIME per column,
   so the gaps look like the gaps they were. */
/** @param {traceView} t @returns {string} */
function miniCharts(t) {
  var B = t.buckets || [], i, maxN = 1, maxErr = 1, maxMs = 1;
  for (i = 0; i < B.length; i++) {
    if (B[i].n > maxN) maxN = B[i].n;
    if (B[i].err > maxErr) maxErr = B[i].err;
    if (B[i].ms > maxMs) maxMs = B[i].ms;
  }
  var x0 = esc(clockHM(tms(t.first))), x1 = esc(clockHM(tms(t.last)));
  var errC = t.err > 0 ? MER : '#8f8f8f';

  var h = '<div class="minis">';

  h += '<div><div class="hd"><span class="t">Requests</span><span class="v">' + t.req + ' total</span></div>' +
    '<div class="chart" style="height:64px"><div class="grid" style="top:50%"></div><div class="cols">';
  for (i = 0; i < B.length; i++) {
    h += '<div class="col" data-k="' + i + '"><i style="height:' + hpc(B[i].n, maxN, 100) + ';background:rgba(255,255,255,0.55)"></i></div>';
  }
  h += '</div></div><div class="axis"><span>' + x0 + '</span><span>' + x1 + '</span></div></div>';

  h += '<div><div class="hd"><span class="t">Errors</span><span class="v" style="color:' + errC + '">' +
    (t.err > 0 ? t.err + ' errored' : 'none') + '</span></div>' +
    '<div class="chart" style="height:64px"><div class="grid" style="top:50%"></div><div class="cols">';
  for (i = 0; i < B.length; i++) {
    h += '<div class="col" data-k="' + i + '"><i style="height:' + hpc(B[i].err, maxErr, 100) + ';background:' + MER + '"></i></div>';
  }
  h += '</div></div><div class="axis"><span>' + x0 + '</span><span>' + x1 + '</span></div></div>';

  h += '<div><div class="hd"><span class="t">Latency</span><span class="v">' + fC(maxMs) + ' ms peak</span></div>' +
    '<div class="chart" style="height:64px"><div class="grid" style="top:50%"></div><div class="cols">';
  for (i = 0; i < B.length; i++) {
    // Time to first token, solid; the rest of the response, translucent above it.
    var ttft = B[i].ttft, rest = Math.max(0, B[i].ms - ttft);
    h += '<div class="col" data-k="' + i + '">' +
      '<i style="height:' + hpc(ttft, maxMs, 100) + ';background:' + MIN + '"></i>' +
      '<i style="height:' + hpc(rest, maxMs, 100) + ';background:rgba(90,162,247,0.32)"></i></div>';
  }
  h += '</div></div><div class="axis"><span>' + x0 + '</span><span>' + x1 + '</span></div></div>';

  return h + '</div>';
}

/* ---------- inspector ---------- */
/** @param {number} id */
function openInsp(id) {
  var q = rowOf(id);
  if (!q) return;
  S.id = id; CAP = null; S.open = {}; S.rawSide = 'request';
  $('#scrim').style.display = 'block';
  var el = $('#insp');
  el.style.display = 'block';
  el.innerHTML = inspShell(q, null);
  wireInsp();
  fetch('/api/capture?id=' + encodeURIComponent(id)).then(function (res) {
    return res.ok ? res.json() : MISSING; // 404 = the capture row was deleted
  }).then(function (j) {
    if (S.id !== id) return;
    CAP = j || MISSING;
    el.innerHTML = inspShell(/** @type {reqRow} */ (q), CAP);
    wireInsp();
  }).catch(function () {
    if (S.id !== id) return;
    CAP = MISSING;
    el.innerHTML = inspShell(/** @type {reqRow} */ (q), MISSING);
    wireInsp();
  });
}
function closeInsp() {
  S.id = null; CAP = null;
  $('#insp').style.display = 'none';
  $('#scrim').style.display = 'none';
}
/** @param {number|null} id @returns {reqRow|null} */
function rowOf(id) {
  if (!T) return null;
  for (var i = 0; i < T.rows.length; i++) if (T.rows[i].id === id) return T.rows[i];
  return null;
}

/* j is null while /api/capture is in flight, MISSING once it 404s. Every tab
   handles all three states — a deleted capture must never throw or blank the
   drawer. */
/** @param {reqRow} q @param {captureView|null} j @returns {string} */
function inspShell(q, j) {
  var i;
  var opTitle = ok(q) ? (q.op || 'text') : ('error — ' + q.status + (q.errType ? ' ' + q.errType : ''));
  var h = '<div class="pad"><div class="hdrline" style="align-items:flex-start;gap:14px">' +
    '<div style="display:flex;align-items:baseline;gap:12px;min-width:0">' +
    '<span class="m" style="font-size:17px;color:#fff;flex:none">#' + esc(q.id) + '</span>' +
    '<span class="m" style="font-size:12px;line-height:1.55;color:' + (ok(q) ? '#e8e8e8' : '#ff7a7a') + ';word-break:break-word">' + esc(opTitle) + '</span></div>' +
    '<button class="x" id="ix" aria-label="close">×</button></div>';

  /** @type {[string, string, string][]} */
  var chips = [
    [ok(q) ? q.status + ' OK' : String(q.status), ok(q) ? 'rgba(47,191,135,0.9)' : MER, ok(q) ? 'rgba(47,191,135,0.3)' : 'rgba(255,90,90,0.4)'],
    [clock(tms(q.time)), '#b5b5b5', 'rgba(255,255,255,0.12)'],
    [fC(q.ms) + ' ms', '#b5b5b5', 'rgba(255,255,255,0.12)'],
    [q.ttft > 0 ? 'ttft ' + fC(q.ttft) + ' ms' : 'ttft —', '#b5b5b5', 'rgba(255,255,255,0.12)'],
    [shortModel(q.model), '#b5b5b5', 'rgba(255,255,255,0.12)'],
    ['stop: ' + (ok(q) ? (q.stop || '—') : '—'), (q.stop === 'max_tokens') ? '#ff7a7a' : '#b5b5b5', (q.stop === 'max_tokens') ? 'rgba(255,90,90,0.4)' : 'rgba(255,255,255,0.12)']
  ];
  h += '<div style="display:flex;flex-wrap:wrap;gap:7px;margin-top:14px">';
  for (i = 0; i < chips.length; i++) h += '<span class="chip" style="color:' + chips[i][1] + ';border-color:' + chips[i][2] + '">' + esc(chips[i][0]) + '</span>';
  if (!q.priced) h += '<span class="badge unp">unpriced</span>';
  h += '</div>';

  if (q.errMsg) h += '<div class="errbox"><div class="m" style="font-size:11.5px;color:#ff7a7a">' + esc(q.status + ' · ' + (q.errType || '')) + '</div>' +
    '<div style="margin-top:6px;font-size:12px;color:#c9c9c9;line-height:1.6">' + esc(q.errMsg) + '</div></div>';

  var tabs = ['billing', 'context', 'request', 'response', 'raw'];
  h += '<div class="tabs">';
  for (i = 0; i < tabs.length; i++) h += '<span class="tab' + (S.tab === tabs[i] ? ' on' : '') + '" data-tab="' + tabs[i] + '">' + tabs[i] + '</span>';
  h += '</div>';

  if (S.tab === 'billing') h += billingTab(q);
  else if (S.tab === 'context') h += contextTab(q, j);
  else if (S.tab === 'response') h += responseTab(q, j);
  else if (S.tab === 'raw') h += rawTab(q, j);
  else h += requestTab(q, j);
  return h + '</div>';
}

/* Billing: the row's own quartets. No capture needed — and no dollar figure at
   all when the model had no rate. */
/** @param {reqRow} q @returns {string} */
function billingTab(q) {
  var c = q.cost, t = q.tok, i;
  /** @type {[string, string, number, number][]} colour, label, tokens, cost */
  var lanes = [
    [MRD, 'cache read · billed at 0.1×', t.read || 0, c.read || 0],
    [MIN, 'fresh input · 1×', t.in || 0, c.in || 0],
    [MWR, 'cache write · 1.25×', t.write || 0, c.write || 0],
    [MOU, 'output', t.out || 0, c.out || 0]
  ];
  var h;
  if (!q.priced) {
    h = '<div style="margin-top:24px;display:flex;align-items:center;gap:12px">' +
      '<span class="badge unp">unpriced</span>' +
      '<span style="font-size:12.5px;color:#c9a97a">no rate for ' + esc(q.model) + ' — tokens are recorded, cost is unknown</span></div>' +
      '<div class="warnbox">This request is excluded from every dollar figure on the page. Add a rate for the model and it prices retroactively — costs are computed at read time, never stored.</div>' +
      '<div style="margin-top:16px">';
    for (i = 0; i < lanes.length; i++) {
      h += '<div class="lanerow">' + swatch(lanes[i][0]) + '<span style="font-size:12px;color:#b5b5b5">' + esc(lanes[i][1]) + '</span>' +
        '<span class="m r" style="font-size:11.5px;color:#9f9f9f">' + fT(lanes[i][2]) + '</span>' +
        '<span class="m r" style="font-size:11.5px;color:#5f5f5f">—</span></div>';
    }
    return h + '</div>';
  }
  var total = rowCost(q), denom = total > 0 ? total : 1;
  h = '<div class="m" style="margin-top:24px;font-size:30px;font-weight:500;color:#fafafa">' + fM(total, 4) + '</div>' +
    '<div class="bar" style="height:7px;margin-top:16px">' +
    '<span style="width:' + pctOf(c.read || 0, denom) + ';background:' + MRD + '"></span>' +
    '<span style="width:' + pctOf(c.in || 0, denom) + ';background:' + MIN + '"></span>' +
    '<span style="width:' + pctOf(c.write || 0, denom) + ';background:' + MWR + '"></span>' +
    '<span style="width:' + pctOf(c.out || 0, denom) + ';background:' + MOU + '"></span></div><div style="margin-top:4px">';
  for (i = 0; i < lanes.length; i++) {
    h += '<div class="lanerow">' + swatch(lanes[i][0]) + '<span style="font-size:12px;color:#b5b5b5">' + esc(lanes[i][1]) + '</span>' +
      '<span class="m r" style="font-size:11.5px;color:#9f9f9f">' + fT(lanes[i][2]) + '</span>' +
      '<span class="m r" style="font-size:11.5px;color:#e8e8e8">' + fM(lanes[i][3], 4) + '</span></div>';
  }
  return h + '</div>';
}

/* Context: where this one request's bytes went. The stacked bar survives a
   deleted capture on the row's own byte splits; the itemized tables do not. */
/** @param {reqRow} q @param {captureView|null} j @returns {string} */
function contextTab(q, j) {
  var b = q.bytes, tot = b.total || (b.tools + b.system + b.messages) || 1, i;

  var h = '<div style="margin-top:24px;font-size:13px;color:#dcdcdc">' +
    fK(b.total) + ' shipped → ' + fT(q.tok.out || 0) + ' tokens out</div>' +
    '<div class="bar" style="height:7px;margin-top:14px">' +
    '<span style="width:' + pctOf(b.tools, tot) + ';background:' + CTOOL + '"></span>' +
    '<span style="width:' + pctOf(b.system, tot) + ';background:' + CSYS + '"></span>' +
    '<span style="width:' + pctOf(b.messages, tot) + ';background:' + CHIST + '"></span></div><div style="margin-top:4px">';

  /** @type {[string, string, number][]} */
  var rows = [[CTOOL, 'tool schemas', b.tools], [CSYS, 'system prompt', b.system], [CHIST, 'message history', b.messages]];
  for (i = 0; i < rows.length; i++) {
    h += '<div class="audrow">' + swatch(rows[i][0]) +
      '<span style="font-size:12px;color:#b5b5b5">' + esc(rows[i][1]) + '</span>' +
      '<span class="m r" style="font-size:11.5px;color:#c9c9c9">' + fK(rows[i][2]) + '</span>' +
      '<span class="m r" style="font-size:11.5px;color:#6f6f6f">' + pct0(rows[i][2], tot) + '</span></div>';
  }
  h += '</div>';

  if (j === null) return h + loading();
  if (j.missing || !j.breakdown) {
    return h + '<div class="warnbox">The capture for this request was deleted, so the itemized schema table is gone. ' +
      'The byte splits above come from the request row itself, which is never deleted.</div>';
  }

  var tools = j.breakdown.tools || [];
  h += '<div class="hdrline" style="margin-top:26px"><div class="cap">Tools by size</div>' +
    '<div class="note">' + tools.length + ' schemas · ' + fK(b.tools) + '</div></div>';
  if (!tools.length) return h + '<div class="note" style="padding:12px 0">this request shipped no tool schemas</div>';

  var big = tools[0].bytes || 1;
  var shown = S.toolsAll ? tools.length : Math.min(6, tools.length);
  h += '<div style="margin-top:6px">';
  for (i = 0; i < shown; i++) {
    h += '<div class="toolrow">' +
      '<span class="m ell" style="font-size:11.5px;color:#c9c9c9" title="' + esc(tools[i].name) + '">' + esc(tools[i].name) + '</span>' +
      '<span class="track"><i style="width:' + pctOf(tools[i].bytes, big) + '"></i></span>' +
      '<span class="m r" style="font-size:11px;color:#9f9f9f">' + fK(tools[i].bytes) + '</span>' +
      '<span class="m r" style="font-size:9.5px;color:#6f6f6f">' + pctOf(tools[i].bytes, tot) + '</span></div>';
  }
  h += '</div>';
  if (tools.length > 6) h += '<div class="more" id="toolsall">' + (S.toolsAll ? 'show less' : 'show all ' + tools.length + ' schemas') + '</div>';
  return h + flagChips(j.breakdown.flags);
}

/** @param {Flags|undefined} f @returns {string} */
function flagChips(f) {
  var g = f || { thinking: false, contextManagement: false, outputConfig: false };
  /** @type {[string, boolean][]} */
  var L = [['thinking', g.thinking], ['context management', g.contextManagement], ['output config', g.outputConfig]];
  var h = '<div class="cap" style="margin-top:26px">Request flags</div><div style="display:flex;gap:7px;margin-top:10px">', i;
  for (i = 0; i < L.length; i++) h += '<span class="chip ' + (L[i][1] ? 'on' : 'off') + '">' + esc(L[i][0]) + (L[i][1] ? ' on' : ' off') + '</span>';
  return h + '</div>';
}

/* ---------- request / response / raw ---------- */
/** @param {*} v @returns {boolean} */
function isArr(v) { return Object.prototype.toString.call(v) === '[object Array]'; }
/** @param {*} v @returns {string} */
function inspText(v) {
  if (typeof v === 'string') return v;
  if (isArr(v)) {
    var out = [], i;
    for (i = 0; i < v.length; i++) out.push(typeof v[i] === 'string' ? v[i] : (v[i] && v[i].text) || JSON.stringify(v[i]));
    return out.join('\n');
  }
  return v == null ? '' : JSON.stringify(v, null, 2);
}

/* foldPre renders long text collapsed to its head, with an expander that shows
   how much is hidden. The inspector's bodies are tens of KB — a wall of every
   one of them at once is what made the tabs unreviewable. */
/** @param {string} key @param {string} txt @param {number} cap @returns {string} */
function foldPre(key, txt, cap) {
  txt = String(txt == null ? '' : txt);
  if (!txt) return '';
  var open = !!S.open[key];
  if (txt.length <= cap) return '<pre>' + esc(txt) + '</pre>';
  return '<pre' + (open ? ' style="max-height:520px"' : '') + '>' + esc(open ? txt : txt.slice(0, cap) + ' …') + '</pre>' +
    '<div class="more" data-x="' + esc(key) + '">' + (open ? '▴ collapse' : '▸ expand · ' + fK(txt.length) + ' of text') + '</div>';
}

/* msgDetail is one captured message, unfolded: every block named, sized and
   shown whole — the breakdown the preview line can only gesture at. */
/** @param {*} m @param {string} key @returns {string} */
function msgDetail(m, key) {
  var c = m.content;
  if (typeof c === 'string') return foldPre(key + 'b', c, 900);
  if (!isArr(c)) return foldPre(key + 'b', JSON.stringify(c, null, 2), 900);
  var h = '', i;
  for (i = 0; i < c.length; i++) {
    var b = c[i] || {};
    var head = b.type || 'block', body = '', color = '#8a8a8a';
    if (b.type === 'text') { body = b.text || ''; }
    else if (b.type === 'thinking') { body = b.thinking || ''; color = MTH; }
    else if (b.type === 'tool_use') { head += ' · ' + (b.name || ''); body = JSON.stringify(b.input || {}, null, 2); color = MIN; }
    else if (b.type === 'tool_result') { body = inspText(b.content); }
    else if (b.type === 'image') { body = ''; }
    else { body = JSON.stringify(b, null, 2); }
    h += '<div class="blk"><div class="bh"><span style="color:' + color + '">' + esc(head) + '</span>' +
      '<span class="m">' + fK(JSON.stringify(b).length) + '</span></div>' +
      foldPre(key + 'b' + i, body, 900) + '</div>';
  }
  return h;
}
/** @param {*} m one message of the captured request @returns {string} */
function msgPreview(m) {
  var c = m.content, i, parts = [];
  if (typeof c === 'string') return c;
  if (Object.prototype.toString.call(c) !== '[object Array]') return JSON.stringify(c);
  for (i = 0; i < c.length; i++) {
    var b = c[i] || {};
    if (b.type === 'tool_use') parts.push('tool_use · ' + b.name);
    else if (b.type === 'tool_result') parts.push('tool_result · ' + fK(JSON.stringify(b).length));
    else if (b.type === 'thinking') parts.push('thinking');
    else if (b.type === 'image') parts.push('image');
    else parts.push(String(b.text || '').replace(/\s+/g, ' '));
  }
  return parts.join(' · ');
}
/** @param {reqRow} q @returns {string} */
function gone(q) {
  return '<div class="warnbox">The capture for request #' + esc(q.id) + ' was deleted — its body is gone. ' +
    'The request row survives: billing and the byte breakdown still work.</div>';
}
/** @returns {string} */
function loading() { return '<div class="note" style="padding:16px 0">loading…</div>'; }

/** @param {reqRow} q @param {captureView|null} j @returns {string} */
function requestTab(q, j) {
  if (j === null) return loading();
  if (j.missing) return gone(q);
  var req = j.request || {};
  var sys = req.system;
  var all = req.messages || [];
  var msgs = [], i;
  for (i = 0; i < all.length; i++) {
    if (all[i] && all[i].role === 'system') { if (!sys) sys = all[i].content; }
    else msgs.push(all[i]);
  }
  var h = '<div class="hdrline" style="margin-top:24px"><div class="cap">System prompt</div>' +
    '<div class="note">' + fK(q.bytes.system) + '</div></div>' +
    (sys ? foldPre('sys', inspText(sys), 500) : '<div class="note" style="padding:12px 0">no system prompt</div>');

  h += '<div class="hdrline" style="margin-top:26px"><div class="cap">Message history</div>' +
    '<div class="note">' + msgs.length + ' messages · ' + fK(q.bytes.messages) + ' · click a row to unfold it</div></div><div style="margin-top:8px">';

  /* Every turn is reachable, but the tail is what you usually came for — the
     earlier ones sit one click behind their count. */
  var skip = S.open['hist'] ? 0 : Math.max(0, msgs.length - 14);
  if (skip) h += '<div class="histrow" data-x="hist" style="cursor:pointer"><span class="m" style="font-size:10.5px;color:#5f5f5f">·</span>' +
    '<span class="m ell" style="font-size:11.5px;color:#8a8a8a">▸ show ' + skip + ' earlier turns held in context</span><span></span></div>';
  for (i = skip; i < msgs.length; i++) {
    var m = msgs[i] || {}, key = 'm' + i, open = !!S.open[key];
    h += '<div class="histrow" data-x="' + key + '" style="cursor:pointer">' +
      '<span class="m" style="font-size:10.5px;color:' + (m.role === 'user' ? '#9a9a9a' : '#d6d6d6') + '">' + (open ? '▾ ' : '▸ ') + esc(m.role || '·') + '</span>' +
      '<span class="m ell" style="font-size:11.5px;color:#a8a8a8">' + esc(trunc(msgPreview(m), 160)) + '</span>' +
      '<span class="m r" style="font-size:10.5px;color:#5f5f5f">' + fK(JSON.stringify(m).length) + '</span></div>';
    if (open) h += '<div class="msgx">' + msgDetail(m, key) + '</div>';
  }
  return h + '</div>';
}

/** @param {reqRow} q @param {captureView|null} j @returns {string} */
function responseTab(q, j) {
  if (j === null) return loading();
  if (j.missing) return gone(q);
  var resp = j.response || {}, bl = resp.content || [];
  var sh = q.shape || { think: 0, text: 0, tool: 0 };
  var h = '<div class="hdrline" style="margin-top:24px"><div class="cap">Decoded response</div>' +
    '<div class="note">' + bl.length + (bl.length === 1 ? ' block' : ' blocks') + ' · ' + fT(q.tok.out || 0) + ' out · ' +
    fT(sh.think) + ' thinking · stop ' + esc(q.stop || resp.stop_reason || '—') + '</div></div>';
  if (!bl.length) return h + '<div class="note" style="padding:12px 0">the response carried no content blocks</div>';

  /* One card per block, in reply order: what the model thought, said and called
     — each sized, each unfoldable on its own instead of one concatenated wall. */
  for (var i = 0; i < bl.length; i++) {
    var b = bl[i] || {};
    var head = b.type || 'block', body = '', color = '#8a8a8a';
    if (b.type === 'tool_use') { head = 'tool_use · ' + (b.name || ''); body = JSON.stringify(b.input || {}, null, 2); color = MIN; }
    else if (b.type === 'thinking') { body = b.thinking || ''; color = MTH; }
    else if (b.type === 'text') { body = b.text || ''; color = '#d6d6d6'; }
    else { body = JSON.stringify(b, null, 2); }
    h += '<div class="blk"><div class="bh"><span style="color:' + color + '">' + esc(head) + '</span>' +
      '<span class="m">' + fK((body || '').length) + '</span></div>' + foldPre('r' + i, body, 900) + '</div>';
  }
  return h;
}

/* The verbatim string behind the raw tab, kept for the copy button. Rewritten
   on every rawTab render, so it always matches what is on screen. */
var RAWTXT = '';

/** @param {reqRow} q @param {captureView|null} j @returns {string} */
function rawTab(q, j) {
  if (j === null) return loading();
  if (j.missing) return gone(q);
  var side = S.rawSide === 'response' ? 'response' : 'request';
  var src = side === 'response' ? j.response : j.request;
  var raw = src == null ? '' : (JSON.stringify(src, null, 2) || '');
  if (raw.length > 400000) raw = raw.slice(0, 400000) + '\n… truncated';
  RAWTXT = raw;

  var h = '<div class="hdrline" style="margin-top:24px">' +
    '<div style="display:flex;align-items:baseline;gap:16px"><div class="cap">Raw body</div>' +
    '<span class="rsw' + (side === 'request' ? ' on' : '') + '" data-raw="request">request</span>' +
    '<span class="rsw' + (side === 'response' ? ' on' : '') + '" data-raw="response">response</span></div>' +
    '<div style="display:flex;align-items:baseline;gap:14px">' +
    '<span class="note">' + (side === 'request' ? fK(q.bytes.total) : fK(raw.length)) + '</span>' +
    (raw ? '<span class="more" id="rawcopy" style="margin-top:0">copy</span>' : '') + '</div></div>';
  if (!raw) return h + '<div class="note" style="padding:12px 0">no ' + side + ' body was captured</div>';

  var lines = raw.split('\n'), i;
  h += '<div class="rawbox" style="max-height:540px">';
  for (i = 0; i < lines.length; i++) {
    h += '<div class="rawrow"><span class="m rawn">' + (i + 1) + '</span><span class="m rawtx">' + esc(lines[i]) + '</span></div>';
  }
  return h + '</div>';
}

function wireInsp() {
  $('#ix').onclick = closeInsp;
  /** @type {NodeListOf<HTMLElement>} */
  var t = document.querySelectorAll('#insp .tab'), i;
  for (i = 0; i < t.length; i++) t[i].onclick = (function (tab) {
    return function () {
      S.tab = tab; S.toolsAll = false;
      redrawInsp();
    };
  })(/** @type {string} */ (t[i].getAttribute('data-tab'))); // data-tab is written in inspShell
  var ta = document.querySelector('#insp #toolsall');
  if (ta) /** @type {HTMLElement} */ (ta).onclick = function () {
    S.toolsAll = !S.toolsAll;
    redrawInsp();
  };

  // expand / collapse toggles: message rows, block bodies, the system prompt
  var xs = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('#insp [data-x]'));
  for (i = 0; i < xs.length; i++) xs[i].onclick = (function (k) {
    return function (/** @type {MouseEvent} */ ev) {
      ev.stopPropagation(); // a block toggle must not also toggle its message row
      S.open[k] = !S.open[k];
      redrawInsp();
    };
  })(/** @type {string} */ (xs[i].getAttribute('data-x')));

  // the raw tab's request / response switch, and its copy button
  var rs = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('#insp .rsw'));
  for (i = 0; i < rs.length; i++) rs[i].onclick = (function (side) {
    return function () { S.rawSide = side; redrawInsp(); };
  })(/** @type {string} */ (rs[i].getAttribute('data-raw')));
  var rc = /** @type {HTMLElement|null} */ (document.querySelector('#insp #rawcopy'));
  if (rc) {
    var rcEl = rc;
    rcEl.onclick = function () {
      if (navigator.clipboard) navigator.clipboard.writeText(RAWTXT);
      rcEl.textContent = 'copied';
    };
  }
}
function redrawInsp() {
  var q = rowOf(S.id);
  if (!q) return;
  $('#insp').innerHTML = inspShell(q, CAP);
  wireInsp();
}

/* ---------- shell ----------
   A poll draws the same frame a click does, so a session's numbers keep moving
   while it is being read. What a poll must not do is announce itself: `quiet`
   suppresses the entry animation of anything already on screen, and the graph
   keeps the horizontal scroll the person put it at. */
/** @param {boolean} [quiet] this frame came from the 2s poll, not from a click */
function render(quiet) {
  var app = $('#app');
  var graph = document.querySelector('.flow-graph');
  var scrolled = graph ? graph.scrollLeft : 0;
  var view = viewFromURL(), i;

  // The header tabs live outside #app, so they are marked here rather than drawn.
  var tabs = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('[data-view]'));
  for (i = 0; i < tabs.length; i++) tabs[i].className = tabs[i].getAttribute('data-view') === view ? 'on' : '';

  app.className = quiet ? 'quiet' : '';
  app.innerHTML = S.sid ? renderTrace() : (view === 'history' ? renderHistory() : renderHome());

  if (quiet && scrolled) {
    graph = document.querySelector('.flow-graph');
    if (graph) graph.scrollLeft = scrolled;
  }
  wire();
}

function wire() {
  var i, els;

  // the header's two screens
  els = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('[data-view]'));
  for (i = 0; i < els.length; i++) els[i].onclick = (function (view) {
    /** @param {MouseEvent} e */
    return function (e) {
      if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      e.preventDefault();
      hideTip();
      navigateView(view);
    };
  })(String(els[i].getAttribute('data-view')));

  // the history range switcher
  els = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('[data-r]'));
  for (i = 0; i < els.length; i++) els[i].onclick = (function (r) {
    return function () { S.range = r; hideTip(); render(); };
  })(Number(els[i].getAttribute('data-r')));

  // one history bucket, hovered on its bar or on its row in the table
  els = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('[data-h]'));
  for (i = 0; i < els.length; i++) {
    els[i].onmouseenter = (function (b) {
      /** @param {MouseEvent} ev */
      return function (ev) {
        /** @type {string[][]} */
        var rows = [[MRD, 'cache read', fUsd(b.cost.read)], [MIN, 'fresh input', fUsd(b.cost.in)],
                    [MWR, 'cache write', fUsd(b.cost.write)], [MOU, 'output', fUsd(b.cost.out)]];
        if (b.err > 0) rows.push([MER, 'errors', String(b.err)]);
        if (b.unpriced > 0) rows.push(['rgba(217,160,78,0.45)', 'unpriced', String(b.unpriced)]);
        showTip(ev, tipHtml(histLabel(b.t) + ' · ' + fC(b.n) + (b.n === 1 ? ' request' : ' requests'), rows,
          fM(dayCost(b), 2) + ' total · hit ' + pct0(b.tok.read, b.tok.in + b.tok.read + b.tok.write) +
          ' · ' + fC(b.sessions) + (b.sessions === 1 ? ' session' : ' sessions')));
      };
    })(/** @type {dayBucket} */ (TIPDATA.days[Number(els[i].getAttribute('data-h'))] || zeroDay(0)));
    els[i].onmouseleave = hideTip;
  }

  // sessions → trace
  els = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('[data-sid]'));
  for (i = 0; i < els.length; i++) els[i].onclick = (function (sid) {
    /** @param {MouseEvent} e */
    return function (e) {
      if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      e.preventDefault();
      hideTip();
      navigateSession(sid, false);
    };
  })(/** @type {string} */ (els[i].getAttribute('data-sid')));

  // the waste tile → the cut list of the session shipping the most of it
  var wo = document.querySelector('#wasteopen');
  if (wo) /** @type {HTMLElement} */ (wo).onclick = function () {
    if (D.overview.worstSid) navigateSession(D.overview.worstSid, false);
  };

  var back = document.querySelector('#back');
  if (back) /** @type {HTMLElement} */ (back).onclick = function () { navigateSession(null, false); };

  var ca = document.querySelector('#cutall');
  if (ca) /** @type {HTMLElement} */ (ca).onclick = function () { S.cutAll = !S.cutAll; render(); };
  var ta = document.querySelector('#app #toolsall');
  if (ta) /** @type {HTMLElement} */ (ta).onclick = function () { S.toolsAll = !S.toolsAll; render(); };
  var fg = document.querySelector('#flowgraph');
  if (fg) /** @type {HTMLElement} */ (fg).onclick = function () {
    S.graph = !S.graph;
    render();
    if (S.graph && S.sid) pollTrace(S.sid, true);
  };

  // trace rows unfold in place; the bodies live one click further, in the inspector
  els = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('[data-id]'));
  for (i = 0; i < els.length; i++) els[i].onclick = (function (id) {
    return function () {
      hideTip();
      S.xrow[id] = !S.xrow[id];
      render();
      if (S.xrow[id] && S.sid) pollTrace(S.sid, true);
    };
  })(Number(els[i].getAttribute('data-id')));
  els = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('[data-graph-id]'));
  for (i = 0; i < els.length; i++) els[i].onclick = (function (id) {
    return function () {
      S.graph = false; S.xrow[id] = true; render();
      if (S.sid) pollTrace(S.sid, true);
      var row = document.querySelector('[data-id="' + id + '"]');
      if (row) row.scrollIntoView({ block: 'center', behavior: 'smooth' });
    };
  })(Number(els[i].getAttribute('data-graph-id')));
  els = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('[data-insp]'));
  for (i = 0; i < els.length; i++) els[i].onclick = (function (id) {
    return function () { hideTip(); openInsp(id); };
  })(Number(els[i].getAttribute('data-insp')));

  // the overview's spend timeline
  els = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('[data-b]'));
  for (i = 0; i < els.length; i++) els[i].onmouseenter = (function (b) {
    /** @param {MouseEvent} ev */
    return function (ev) {
      var tot = (b.costRead || 0) + (b.costIn || 0) + (b.costWrite || 0) + (b.costOut || 0);
      var rows = [[MRD, 'cache read', fT(b.cacheRead)], [MIN, 'fresh input', fT(b.input)],
                  [MWR, 'cache write', fT(b.cacheWrite)], [MOU, 'output', fT(b.output)]];
      if (b.err > 0) rows.push([MER, 'errors', String(b.err)]);
      showTip(ev, tipHtml(clockHM(b.t) + ' · ' + b.n + (b.n === 1 ? ' request' : ' requests'), rows,
        fT((b.cacheRead || 0) + (b.input || 0) + (b.cacheWrite || 0)) + ' in · ' + fT(b.output) + ' out · ' + fM(tot, 2)));
    };
  })(/** @type {bucket} */ (TIPDATA.buckets[Number(els[i].getAttribute('data-b'))] || {}));

  // the trace's per-request charts
  els = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('[data-q]'));
  for (i = 0; i < els.length; i++) els[i].onmouseenter = (function (idx, isRow) {
    /** @param {MouseEvent} ev */
    return function (ev) {
      var q = TIPDATA.rows[idx], e = TIPDATA.cache[idx];
      if (!q) return;
      // An unfolded row already shows everything the tooltip would — hovering it
      // must not float a summary over the breakdown being read.
      if (isRow && S.xrow[q.id]) return;
      var o = opParts(q);
      /** @type {string[][]} */
      var rows;
      if (isErr(q)) rows = [[MER, String(q.status), q.errType || '']];
      else rows = [[MRD, 'cache read', fT(q.tok.read)], [MIN, 'fresh input', fT(q.tok.in)],
                   [MWR, 'cache write', fT(q.tok.write)], [MOU, 'output', fT(q.tok.out)],
                   [MTH, 'thinking', fT((q.shape || {}).think)]];
      var foot = (q.priced ? fM(rowCost(q), 4) : 'unpriced') + ' · ' + fC(q.ms) + ' ms' +
        (e && e.class === 'break' ? ' · cache break' : '');
      showTip(ev, tipHtml(clock(tms(q.time)) + ' · ' + (o.name || o.tag), rows, foot));
    };
  })(Number(els[i].getAttribute('data-q')), els[i].className.indexOf('qrow') >= 0);

  // the trace's request / error / latency strips
  els = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('[data-k]'));
  for (i = 0; i < els.length; i++) els[i].onmouseenter = (function (b) {
    /** @param {MouseEvent} ev */
    return function (ev) {
      showTip(ev, tipHtml(b.n + (b.n === 1 ? ' request' : ' requests'), [
        [MER, 'errors', String(b.err)],
        [MIN, 'ttft', b.ttft > 0 ? fC(b.ttft) + ' ms' : '—'],
        ['rgba(255,255,255,0.55)', 'duration', b.ms > 0 ? fC(b.ms) + ' ms' : '—']
      ], ''));
    };
  })(/** @type {traceBucket} */ (TIPDATA.buck[Number(els[i].getAttribute('data-k'))] || { n: 0, err: 0, ms: 0, ttft: 0 }));

  els = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('.col, .cells span'));
  for (i = 0; i < els.length; i++) els[i].onmouseleave = hideTip;

  // the cache strip
  els = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('[data-c]'));
  for (i = 0; i < els.length; i++) els[i].onmouseenter = (function (idx) {
    /** @param {MouseEvent} ev */
    return function (ev) {
      var e = TIPDATA.cache[idx], q = TIPDATA.rows[idx];
      if (!e || !q) return;
      showTip(ev, tipHtml(clock(tms(q.time)) + ' · ' + e.class, [
        [cacheColor(e), 'what happened', causeText(e)]
      ], e.rebill ? fUsd(e.rebill) + ' re-billed' : ''));
    };
  })(Number(els[i].getAttribute('data-c')));
}

/* ---------- polling ----------
   Two endpoints, because they cost different things: /api/stats is a table scan
   and /api/trace reads a capture. Only the screen you are looking at is fetched. */
function poll() {
  fetch('/api/stats').then(function (r) { return r.json(); }).then(/** @param {statsView} j */ function (j) {
    if (!j) return;
    D = j;
    D.overview = j.overview || zeroOverview();
    D.sessions = j.sessions || [];
    var host = String(j.upstream || '').replace(/^https?:\/\//, '').replace(/\/.*$/, '');
    $('#route').textContent = 'localhost:' + j.port + ' → ' + host;
    $('#hdrstat').textContent = fC(j.traced) + ' traced · ' + fM(j.cost, 2) + ' total';
    var u = $('#unp'), n = j.unpricedReqs || 0, um = j.unpricedModels || [];
    // The models, not just the count: the badge is only actionable if it says
    // which rate row is missing. Short lists go inline; a long one would push the
    // header around, so it falls back to the tooltip.
    u.textContent = n + (n === 1 ? ' unpriced request' : ' unpriced requests') +
      (um.length && um.length <= 2 ? ' · ' + um.join(' ') : '');
    u.title = um.length ? 'no rate for: ' + um.join(', ') : '';
    u.style.display = n > 0 ? 'inline-block' : 'none';
    var store = j.storage || { captureBytes: 0, retention: 'off' };
    $('#capsz').textContent = fK(store.captureBytes) + ' captures';
    // Never while they are choosing: a 2s poll that reassigns the select mid-open
    // snaps it back under the cursor.
    var ret = /** @type {HTMLSelectElement} */ ($('#ret'));
    if (document.activeElement !== ret) ret.value = store.retention || 'off';
    if (S.sid) pollTrace(S.sid, false, true);
    else if (viewFromURL() === 'history') pollHistory(true);
    else render(true);
  }).catch(function () { /* the server is restarting; the next poll picks it up */ });
}

/* The day buckets, while that screen is open and never otherwise. It is the same
   lifetime scan /api/stats already pays for on every poll, which is what lets
   today's bar keep growing while it is being looked at. */
/** @param {boolean} [quiet] the 2s poll, not a click */
function pollHistory(quiet) {
  fetch('/api/history').then(function (r) { return r.json(); }).then(/** @param {historyView|null} j */ function (j) {
    if (!j || S.sid || viewFromURL() !== 'history') return; // they navigated away while it was in flight
    H = j.days || [];
    render(quiet);
  }).catch(function () { /* next poll */ });
}

/** @param {string} sid @param {boolean} flow @param {boolean} [quiet] the 2s poll, not a click */
function pollTrace(sid, flow, quiet) {
  var target = '/api/trace?sid=' + encodeURIComponent(sid) + (flow ? '&flow=1' : '');
  fetch(target).then(function (r) {
    if (r.status === 404) return null;
    if (!r.ok) throw new Error('trace request failed: ' + r.status);
    return r.json();
  }).then(/** @param {traceView|null} j */ function (j) {
    if (S.sid !== sid) return; // they navigated away while it was in flight
    if (!j) { navigateSession(null, true); return; }
    // A lightweight poll must not discard flow that an explicit, on-demand
    // request just loaded for the graph or an expanded operation.
    if (!flow && T && T.flow) j.flow = T.flow;
    T = j;
    render(quiet);
    // The graph and an unfolded operation are drawn from the capture-backed
    // flow, which the cheap poll does not carry: without this they would sit at
    // the operations the session had when they were opened. Re-read it only
    // when the session has actually grown — never twice a second for a
    // conversation that has not moved.
    if (!flow && staleFlow(j) && (S.graph || traceDetailOpen())) pollTrace(sid, true, quiet);
  }).catch(function () { /* next poll */ });
}

/** The flow is one turn per request, so a shorter one is a flow that predates
 * requests the session has since made.
 * @param {traceView} t @returns {boolean} */
function staleFlow(t) { return (t.flow || []).length < (t.rows || []).length; }

/** @returns {boolean} */
function traceDetailOpen() {
  var rows = S.xrow || {}, id;
  for (id in rows) if (rows[id]) return true;
  return false;
}

/** Change the visible screen without writing browser history. */
/** @param {string|null} sid */
function showSession(sid) {
  if (S.id !== null) closeInsp();
  S.sid = sid; T = null; S.toolsAll = false; S.cutAll = false; S.graph = false; S.xrow = {};
  render();      // the shell, with its loading note
  if (sid) pollTrace(sid, false);
}

/** Switch screens. The view is read back off the URL by render, so pushing the
 * location IS the state change — there is nothing else to keep in step.
 * @param {string} view overview | history */
function navigateView(view) {
  var target = viewURL(view);
  var current = window.location.pathname + window.location.search + window.location.hash;
  if (target !== current) window.history.pushState(null, '', target);
  showSession(null);
  if (view === 'history') pollHistory(false);
}

/** @param {string|null} sid @param {boolean} replace */
function navigateSession(sid, replace) {
  var target = sessionURL(sid);
  var current = window.location.pathname + window.location.search + window.location.hash;
  if (target !== current) {
    if (replace) window.history.replaceState(null, '', target);
    else window.history.pushState(null, '', target);
  }
  showSession(sid);
}

/* ---------- retention ----------
   The only two controls on this page that delete anything. Both write and then
   re-poll rather than patching D by hand: the server decides what actually went,
   and the next frame shows it. */
$('#ret').onchange = function () {
  var v = /** @type {HTMLSelectElement} */ ($('#ret')).value;
  fetch('/api/settings?retention=' + encodeURIComponent(v), { method: 'POST' }).then(poll).catch(function () { });
};
$('#purge').onclick = function () {
  if (!window.confirm('Delete every stored capture?\n\nThe request and response bodies go, so the inspector loses its detail. Every number on this page stays: they are folded from the fact rows, which are never deleted.')) return;
  fetch('/api/purge', { method: 'POST' }).then(poll).catch(function () { });
};

TIP = $('#tip');
$('#scrim').onclick = closeInsp;
window.addEventListener('keydown', function (e) {
  if (e.key !== 'Escape') return;
  // Leaving a screen by key takes any open tooltip with it: the element it was
  // anchored to is about to be replaced, and no mouseleave will ever fire for it.
  hideTip();
  if (S.id !== null) closeInsp();
  else if (S.sid) navigateSession(null, false);
  else if (viewFromURL() === 'history') navigateView('overview');
});
window.addEventListener('popstate', function () {
  showSession(sessionFromURL());
});
render();
poll();
setInterval(poll, 2000);
