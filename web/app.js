// @ts-check
/* TokenTracer dashboard. Vanilla JS, no build step, no external requests.
 *
 * Two rules carried over from tokentrace, both load-bearing:
 *
 *  1. Everything untrusted goes through esc() before it becomes HTML. Log content
 *     is whatever the model and the tools emitted — treat it as attacker-influenced.
 *  2. Numbers are folded server-side in Go; this file only words and draws them.
 *     No cost, burn rate, percentile or aggregate is computed here. The one
 *     exception is presentation: bar widths and the percentages beside them.
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
 *   statsView & below  → internal/api/fold.go            (GET /api/stats)
 *   captureView        → internal/api/api.go             (GET /api/capture)
 *   Breakdown & below  → internal/anthropic/anthropic.go
 *
 * A field is required here when Go sends it unconditionally — which is all of
 * them but three: errType, errMsg and probe are `omitempty` in Go and optional
 * here.
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
 * @typedef {object} latency
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
 * @typedef {object} recentRow
 * @property {number} id
 * @property {string} time      RFC3339
 * @property {string} label     what was ASKED: the first user text, ≤64 chars. Always sent, often ''
 * @property {string} model     the model the money was billed against, not always the one asked for
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
 * @property {byteSplit} bytes
 * @property {string} [errType] omitempty
 * @property {string} [errMsg]  omitempty
 * @property {boolean} [probe]  omitempty
 */

/**
 * @typedef {object} overview
 * @property {number} burnNow   $/hr, extrapolated from the current window
 * @property {number} burnAvg   $/hr, lifetime
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
 * @property {boolean} coldStart under one window of history: no trend can be read off it
 */

/**
 * @typedef {object} statsView
 * @property {number} port
 * @property {string} upstream
 * @property {number} traced
 * @property {number} cost
 * @property {number} unpricedReqs
 * @property {tokens} tokens    lifetime
 * @property {overview} overview
 * @property {recentRow[]} recent
 */

/** D is a statsView once the first poll lands — and until it does, the placeholder
 * below, which has no tokens at all, an empty overview and a port that is still ''.
 * The first frame is drawn from that, which is why everything downstream reaches
 * for `|| 0` before it prints a number. These two typedefs say exactly that: the
 * contract's fields, its names and its types, all optional. Both are derived from
 * statsView rather than restating it, so neither can drift away from it.
 * @typedef {Partial<Omit<overview, 'latency'|'tokens'>> & {latency?: Partial<latency>, tokens?: Partial<tokens>}} overviewModel
 */
/** @typedef {Omit<statsView, 'port'|'tokens'|'overview'> & {port: number|string, tokens?: Partial<tokens>, overview: overviewModel}} statsModel */

/**
 * @typedef {object} ToolItem
 * @property {string} name
 * @property {number} bytes
 */

/**
 * @typedef {object} SystemItem
 * @property {number} bytes
 * @property {string} cacheControl "ephemeral/1h", "ephemeral", or ""
 */

/**
 * @typedef {object} MessageItem
 * @property {string} role
 * @property {number} bytes
 * @property {string[]} blockKinds
 */

/**
 * @typedef {object} Flags
 * @property {boolean} thinking
 * @property {boolean} contextManagement
 * @property {boolean} outputConfig
 */

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
 *
 * request and response are `any` deliberately. They are the captured blobs — the
 * request is whatever the client sent, and a blob that did not parse as JSON comes
 * back quoted, as a bare string. Neither has a shape this page may assume.
 * @typedef {object} captureView
 * @property {any} [request]
 * @property {any} [response]
 * @property {Breakdown} [breakdown] absent when the body would not parse
 * @property {boolean} [missing]     client-side only
 */

/** What a row's op string says the reply did.
 * @typedef {object} opInfo
 * @property {string} tag
 * @property {string} tagC
 * @property {string} name
 * @property {string} args
 */

var MRD = '#2fbf87', MIN = '#5aa2f7', MWR = '#d9a04e', MOU = '#ededed', MER = '#ff5a5a';
var CTOOL = '#c9c9c9', CSYS = '#7a7a7a', CHIST = '#454545';

/** @type {statsModel} */
var D = {
  port: '', upstream: '', traced: 0, cost: 0, unpricedReqs: 0,
  overview: { timeline: [], latency: {} }, recent: []
};
/** @type {{id: number|null, tab: string, toolsAll: boolean}} */
var S = { id: null, tab: 'request', toolsAll: false };
var PV = /** @type {Record<string, number|undefined>} */ ({}), PVT = /** @type {Record<string, number|undefined>} */ ({});
/** @type {captureView|null} */
var CAP = null;                // the open inspector's fetched capture, cached across tab switches
/** @type {captureView} */
var MISSING = { missing: true }; // /api/capture said 404 — the capture row was deleted

/* Every id passed to $ is one this file or index.html writes, so querySelector
   cannot miss — with one exception, #toolsall, which exists only once the tools
   table overflows, and whose single caller checks for it. Typing that miss away
   here is what keeps the other nine call sites from having to. */
/** @param {string} s @returns {HTMLElement} */
function $(s) { return /** @type {HTMLElement} */ (document.querySelector(s)); }
/** @param {*} s @returns {string} */
function esc(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}
/** @param {number|undefined} n @returns {string} */
function fT(n) { n = n || 0; if (n < 1000) return String(n); if (n < 1e6) return (n / 1e3).toFixed(1) + 'k'; return (n / 1e6).toFixed(2) + 'M'; }
/** @param {number|undefined} b @returns {string} */
function fK(b) { b = b || 0; if (b < 1024) return b + ' B'; return (b / 1024).toFixed(1) + ' KB'; }
/** @param {number|undefined} n @returns {string} */
function fC(n) { return String(Math.round(n || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ','); }
/** @param {number|undefined} v @param {number} d @returns {string} */
function fM(v, d) { return '$' + (v || 0).toFixed(d); }
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
/** @param {recentRow} r @returns {number} */
function tms(r) { return new Date(r.time).getTime(); }
/** @param {recentRow} r @returns {boolean} */
function ok(r) { return r.status < 400; }

/* A 4xx that is not a failure: the 429 the API answers a quota probe with. Claude
   Code opens every session with a max_tokens:1 "quota" ping, reads the 429 as its
   answer and carries on. Drawn red it would put an error on the board at the start
   of every session — which is how a real error learns to look normal. Only the 429
   is forgiven: a probe that 401s means the key is dead, and that stays red. */
/** @param {recentRow} r @returns {boolean} */
function benign(r) { return !!r.probe && r.status === 429; }

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
var COMP_LEGEND = [[CTOOL, 'tool schemas'], [CSYS, 'system'], [CHIST, 'history']];

/* ---------- row accessors ----------
   The /api/stats contract groups a row's tokens and costs into quartets. Nothing
   below invents a number; rowCost sums the row's own four cost components purely
   so the row can print one figure.

   The quartets come back Partial: the server always sends all four, but the page
   reads them through `|| 0` and the types keep that honest. */
/** @param {number|null} id @returns {recentRow|null} */
function rowOf(id) { for (var i = 0; i < D.recent.length; i++) if (D.recent[i].id === id) return D.recent[i]; return null; }
/** @param {recentRow} q @returns {Partial<tokens>} */
function tokOf(q) { return q.tok || {}; }
/** @param {recentRow} q @returns {Partial<costs>} */
function costOf(q) { return q.cost || {}; }
/** @param {recentRow} q @returns {number} */
function rowCost(q) { var c = costOf(q); return (c.in || 0) + (c.read || 0) + (c.write || 0) + (c.out || 0); }
/** @param {recentRow} q @returns {boolean} */
function unpriced(q) { return q.priced === false; }
/* Byte splits ride on the row when the server ships them; the breakdown tab
   degrades to a bare note when they are absent. */
/** @param {recentRow} q @returns {byteSplit|null} */
function bytesOf(q) { return q.bytes || null; }

/* ---------- tooltip ---------- */
var TIP = /** @type {HTMLElement} */ (/** @type {any} */ (null)), TIPDATA = /** @type {{buckets?: bucket[]}} */ ({});
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

/* What the reply did, parsed into tag/name/args for the log row and the
   inspector title. op is "tool_use · DesignSync" or "text — completion".
   Errors speak through their status instead. */
/** @param {recentRow} q @returns {opInfo} */
function opParts(q) {
  if (q.probe) {
    if (benign(q)) return { tag: 'probe', tagC: '#7f7f7f', name: '', args: 'quota check · 429 is the answer, not a failure' };
    if (ok(q)) return { tag: 'probe', tagC: '#7f7f7f', name: '', args: 'quota check' };
    // A probe that failed any other way is a real problem, and a loud one: a 401
    // here means the key is dead. Being a probe forgives the 429 and nothing else.
    return { tag: 'probe', tagC: MER, name: String(q.status), args: 'quota check failed · ' + (q.errType || q.errMsg || '') };
  }
  if (!ok(q)) return { tag: 'error', tagC: MER, name: String(q.status), args: q.errType || q.errMsg || '' };
  var op = q.op || 'text — completion';
  var em = op.indexOf(' — '), head = em >= 0 ? op.slice(0, em) : op, args = em >= 0 ? op.slice(em + 3) : '';
  var dm = head.indexOf(' · '), tag = dm >= 0 ? head.slice(0, dm) : head, name = dm >= 0 ? head.slice(dm + 3) : '';
  if (tag === 'text') return { tag: 'text', tagC: '#9f9f9f', name: '', args: args || name };
  return { tag: tag, tagC: MIN, name: name, args: args };
}

/* ---------- overview ---------- */
/** @returns {string} */
function renderHome() {
  var ov = D.overview || {}, i;
  var win = ov.windowMin || 10; // the server owns the window; 10 min is tokentrace's default
  var winCap = 'last ' + win + ' min';

  /* Burn is a RATE — dollars per hour, extrapolated from the window — and it is
     the biggest number on the page, so it is the one most likely to be misread as
     a bill. The subtitle carries the actual total for exactly that reason.

     Below one window of history the server floors burnAvg at the window, which
     pins it to burnNow and forces the ratio to 1. Every trend branch here would
     then land on "steady" — the calmest thing the page can say, said precisely
     when it knows least, and said over a first-turn cache write that will never
     recur. So a cold start does not get a trend. It gets told it is a cold start. */
  var burnNow = ov.burnNow || 0, burnAvg = ov.burnAvg || 0;
  var ratio = burnAvg > 0 ? burnNow / burnAvg : 1, burnSub;
  if (!D.traced) burnSub = 'no requests recorded yet';
  else if (ov.coldStart) burnSub = 'cold start · ' + fM(D.cost, 2) + ' total so far · too little history for a trend';
  else if (ratio > 1.35) burnSub = '▲ ' + ratio.toFixed(1) + '× the average of ' + fM(burnAvg, 2) + '/hr · ' + fM(D.cost, 2) + ' total';
  else if (ratio < 0.7) burnSub = '▾ below the ' + fM(burnAvg, 2) + '/hr average · ' + fM(D.cost, 2) + ' total';
  else burnSub = 'steady · avg ' + fM(burnAvg, 2) + '/hr · ' + fM(D.cost, 2) + ' total';

  var hitNow = ov.hitNow || 0, hitAvg = ov.hitAvg || 0, hitSub;
  if (!D.traced) hitSub = 'cache reads bill at 0.1× fresh input';
  else if (hitNow < hitAvg - 0.06) hitSub = '▾ vs ' + (hitAvg * 100).toFixed(0) + '% lifetime — uncached input bills at 1×';
  else hitSub = 'lifetime ' + (hitAvg * 100).toFixed(0) + '% · cache reads bill at 0.1×';

  var reqHr = ov.reqHr || 0, winReqs = ov.winReqs || 0;
  var reqSub = winReqs + (winReqs === 1 ? ' request' : ' requests') + ' in the ' + winCap + ' · ' + fM(ov.avgReq || 0, 4) + ' avg';

  var lat = ov.latency || {}, p50 = lat.p50Ttft || 0, p95 = lat.p95Ttft || 0;
  var latSub = p50 > 0 ? 'p95 ' + fC(p95) + ' ms · time to first token' : 'no timed responses yet';

  /* Tokens: the raw facts, before anybody prices them.

     Summing fresh input, cache reads and cache writes into one "in" is true and
     useless. They are the same token to the model and wildly different money to
     you — a cache read bills at 0.1× and a 1h write at 2×, a 20× spread — so the
     single number hides the only thing worth knowing about it. A Claude Code turn
     that reads 100k cached tokens and writes 4k fresh ones looks, collapsed, like
     a 104k monster; split, it is a cheap turn and an expensive one and you can
     tell which is which. So: NEW (what you paid full freight or more for) beside
     CACHED (what you got at a tenth), never added together. */
  var wt = ov.tokens || {}, lt = D.tokens || {};
  var winNew = (wt.in || 0) + (wt.write || 0), winCached = wt.read || 0, winOut = wt.out || 0;
  var lifeNew = (lt.in || 0) + (lt.write || 0), lifeCached = lt.read || 0, lifeOut = lt.out || 0;
  var tokSub = D.traced
    ? 'lifetime ' + fT(lifeNew) + ' new · ' + fT(lifeCached) + ' cached · ' + fT(lifeOut) + ' out'
    : 'new = fresh input + cache writes · cached reads bill at 0.1×';

  var h = '<div class="wrap" style="padding-top:36px"><div class="tiles">';
  h += '<div><div class="cap">Burn rate · ' + esc(winCap) + '</div>' +
    '<div class="val" style="gap:8px"><span class="big" style="animation:' + anim('burn', burnNow) + '">' + fM(burnNow, 2) + '</span>' +
    '<span class="unit" style="font-size:15px">/hr</span></div>' +
    '<div class="tile-sub" style="margin-top:13px;font-size:11.5px">' + esc(burnSub) + '</div></div>';
  h += '<div style="padding-top:5px"><div class="cap">Cache hit · ' + esc(winCap) + '</div>' +
    '<div class="val" style="gap:5px"><span class="mid" style="animation:' + anim('hit', hitNow) + '">' + (hitNow * 100).toFixed(1) + '</span>' +
    '<span class="unit">%</span></div><div class="tile-sub">' + esc(hitSub) + '</div></div>';
  h += '<div style="padding-top:5px"><div class="cap">Requests · ' + esc(winCap) + '</div>' +
    '<div class="val" style="gap:8px"><span class="mid" style="animation:' + anim('req', reqHr) + '">' + fC(reqHr) + '</span>' +
    '<span class="unit">/hr</span></div><div class="tile-sub">' + esc(reqSub) + '</div></div>';
  h += '<div style="padding-top:5px"><div class="cap">Tokens · ' + esc(winCap) + '</div>' +
    '<div class="val trio">' +
      '<span class="mid" style="animation:' + anim('tnew', winNew) + '">' + fT(winNew) + '</span>' +
      '<span class="unit">new</span>' +
      '<span class="unit" style="opacity:0.45;padding:0 1px">·</span>' +
      '<span class="mid" style="animation:' + anim('tcache', winCached) + '">' + fT(winCached) + '</span>' +
      '<span class="unit">cached</span>' +
      '<span class="unit" style="opacity:0.45;padding:0 1px">→</span>' +
      '<span class="mid" style="animation:' + anim('tout', winOut) + '">' + fT(winOut) + '</span>' +
      '<span class="unit">out</span>' +
    '</div><div class="tile-sub">' + esc(tokSub) + '</div></div>';
  h += '<div style="padding-top:5px"><div class="cap">Latency · TTFT p50</div>' +
    '<div class="val" style="gap:5px"><span class="mid" style="animation:' + anim('ttft', p50) + '">' + (p50 > 0 ? fC(p50) : '—') + '</span>' +
    '<span class="unit">ms</span></div><div class="tile-sub">' + esc(latSub) + '</div></div>';
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
      '<i style="height:' + hpc(b.costRead, maxB) + ';background:' + MRD + '"></i>' +
      '<i style="height:' + hpc(b.costIn, maxB) + ';background:' + MIN + '"></i>' +
      '<i style="height:' + hpc(b.costWrite, maxB) + ';background:' + MWR + '"></i>' +
      '<i style="height:' + hpc(b.costOut, maxB) + ';background:' + MOU + '"></i>' +
      '<div class="err" style="height:' + (b.err > 0 ? '3px' : '0px') + '"></div></div>';
  }
  h += '</div></div><div class="axis"><span>-60m</span><span>-45m</span><span>-30m</span><span>-15m</span><span>now</span></div></div>';

  h += requestLog();
  return h + '</div>';
}
/** @param {number} v @param {number} max @returns {string} */
function hpc(v, max) { return (((v || 0) / max) * 96).toFixed(2) + '%'; }

/* ---------- request log ----------
   One row per request — the flat log that replaced tokentrace's session table. */
/** @returns {string} */
function requestLog() {
  var R = D.recent || [], i, maxMs = 1;
  for (i = 0; i < R.length; i++) if (R[i].ms > maxMs) maxMs = R[i].ms;

  var h = '<div style="margin-top:50px"><div class="hdrline"><div class="cap">Requests <span class="sub">· newest first · click a row to inspect</span></div>' +
    '<div class="note">' + fC(D.traced) + ' traced' + (D.unpricedReqs > 0 ? ' · ' + fC(D.unpricedReqs) + ' unpriced' : '') + '</div></div>' +
    '<div class="thead tgrid"><span>Time</span><span>Model</span><span>Session</span><span>Prompt</span><span>Reply</span><span>St</span>' +
    '<span>Stop</span><span class="r">ms</span><span class="r">TTFT</span><span>Token mix</span>' +
    '<span class="r">new → out</span><span class="r">Cost</span></div>';

  if (!R.length) {
    var at = D.port ? '<span class="m">ANTHROPIC_BASE_URL=http://localhost:' + esc(D.port) + '</span>' : 'this proxy';
    return h + '<div class="empty">No requests yet. Point your client at ' + at + ' — rows land here within two seconds.</div></div>';
  }

  h += '<div class="tlist">';
  for (i = 0; i < R.length; i++) h += reqRow(R[i], maxMs);
  return h + '</div></div>';
}

/** @param {recentRow} q @param {number} maxMs @returns {string} */
function reqRow(q, maxMs) {
  var o = opParts(q), isErr = !ok(q) && !benign(q), t = tokOf(q);
  var stC = isErr ? MER : (q.probe ? '#7f7f7f' : 'rgba(47,191,135,0.85)');
  var stB = isErr ? 'rgba(255,90,90,0.4)' : (q.probe ? 'rgba(255,255,255,0.12)' : 'rgba(47,191,135,0.28)');
  var newTot = (t.in || 0) + (t.write || 0); // never summed with cache reads — see the Tokens tile
  var barW = Math.min(100, Math.max(1.5, ((q.ms || 0) / maxMs) * 100)).toFixed(1) + '%';

  var op = '<span style="display:flex;align-items:center;gap:8px;min-width:0">';
  if (o.tag !== 'tool_use') op += '<span class="m" style="flex:none;font-size:9.5px;color:' + o.tagC + '">' + esc(o.tag) + '</span>';
  if (o.name) op += '<span class="m" style="flex:none;font-size:11px;color:#e4e4e4">' + esc(o.name) + '</span>';
  if (o.args) op += '<span class="m ell" style="font-size:10.5px;color:#7a7a7a">' + esc(o.args) + '</span>';
  op += '</span>';

  var stop = isErr ? '—' : (q.stop || '—');
  var stopC = (stop === 'max_tokens') ? '#ff7a7a' : '#7f7f7f';

  /* An unpriced row shows a badge, never a dollar figure — a $0.00 here would be
     a lie about a model we have no rate for. */
  var cost;
  if (unpriced(q)) cost = '<span class="badge unp">unpriced</span>';
  else if (isErr || q.probe) cost = '<span class="num" style="color:#5f5f5f">—</span>';
  else cost = '<span class="num" style="color:#e8e8e8">' + fM(rowCost(q), 4) + '</span>';

  return '<div class="trow tgrid" data-id="' + esc(q.id) + '">' +
    '<span class="m" style="font-size:10.5px;color:#8f8f8f">' + esc(clock(tms(q))) + '</span>' +
    '<span class="m ell" style="font-size:10.5px;color:#b9b9b9">' + esc(shortModel(q.model)) + '</span>' +
    '<span class="m ell" style="font-size:10px;color:#6f6f6f" title="' + esc(q.sid) + '">' + esc(trunc(q.sid || '—', 9)) + '</span>' +
    /* What was asked, next to what came back. Three rows can all say end_turn and
       cost real money, and only the prompt tells you that one of them was you and
       another was Claude Code paying a model to name the session. Muted on purpose:
       it is the context for the cost, not a rival to it. */
    '<span class="m ell" style="font-size:10.5px;color:#8f8f8f" title="' + esc(q.label) + '">' + esc(trunc(q.label || '—', 40)) + '</span>' +
    op +
    '<span class="m" style="font-size:9.5px;color:' + stC + ';border:1px solid ' + stB + ';padding:1px 0;text-align:center">' + esc(q.status) + '</span>' +
    '<span class="m ell" style="font-size:10px;color:' + stopC + '">' + esc(stop) + '</span>' +
    '<span style="display:flex;align-items:center;gap:6px">' +
      '<span style="flex:1 1 0;height:3px;background:rgba(255,255,255,0.05)"><span style="display:block;height:3px;width:' + barW + ';background:' + (isErr ? MER : '#8f8f8f') + '"></span></span>' +
      '<span class="m r" style="flex:none;font-size:10px;color:#7f7f7f">' + fC(q.ms) + '</span></span>' +
    '<span class="m r" style="font-size:10px;color:#7f7f7f">' + (q.ttft > 0 ? fC(q.ttft) : '—') + '</span>' +
    mix(t.read || 0, t.in || 0, t.write || 0, t.out || 0) +
    '<span class="m r" style="font-size:10.5px;color:#b9b9b9">' + (isErr || q.probe ? '—' : fT(newTot) + ' → ' + fT(t.out || 0)) + '</span>' +
    '<span class="r">' + cost + '</span></div>';
}

/* ---------- inspector ---------- */
/** @param {number} id */
function openInsp(id) {
  var q = rowOf(id);
  if (!q) return;
  S.id = id; S.toolsAll = false; CAP = null;
  $('#scrim').style.display = 'block';
  var el = $('#insp');
  el.style.display = 'block';
  el.innerHTML = inspShell(q, null);
  wireInsp();
  // q is non-null past the guard above and is never reassigned, but tsc drops that
  // narrowing the moment it crosses into a callback, so both handlers re-assert it.
  fetch('/api/capture?id=' + encodeURIComponent(id)).then(function (res) {
    return res.ok ? res.json() : MISSING; // 404 = the capture row was deleted
  }).then(function (j) {
    if (S.id !== id) return;
    CAP = j || MISSING;
    el.innerHTML = inspShell(/** @type {recentRow} */ (q), CAP);
    wireInsp();
  }).catch(function () {
    if (S.id !== id) return;
    CAP = MISSING;
    el.innerHTML = inspShell(/** @type {recentRow} */ (q), MISSING);
    wireInsp();
  });
}
function closeInsp() {
  S.id = null; CAP = null;
  $('#insp').style.display = 'none';
  $('#scrim').style.display = 'none';
}

/* j is null while /api/capture is in flight, MISSING once it 404s. Every tab
   handles all three states — a deleted capture must never throw or blank the
   drawer. */
/** @param {recentRow} q @param {captureView|null} j @returns {string} */
function inspShell(q, j) {
  var i;
  var opTitle = ok(q) ? (q.op || 'text — completion') : ('error — ' + q.status + (q.errType ? ' ' + q.errType : ''));
  var h = '<div class="pad"><div class="hdrline" style="align-items:flex-start;gap:14px">' +
    '<div style="display:flex;align-items:baseline;gap:12px;min-width:0">' +
    '<span class="m" style="font-size:17px;color:#fff;flex:none">#' + esc(q.id) + '</span>' +
    '<span class="m" style="font-size:12px;line-height:1.55;color:' + (ok(q) ? '#e8e8e8' : '#ff7a7a') + ';word-break:break-word">' + esc(opTitle) + '</span></div>' +
    '<button class="x" id="ix" aria-label="close">×</button></div>';

  var chips = [
    [ok(q) ? q.status + ' OK' : String(q.status), ok(q) ? 'rgba(47,191,135,0.9)' : MER, ok(q) ? 'rgba(47,191,135,0.3)' : 'rgba(255,90,90,0.4)'],
    [clock(tms(q)), '#b5b5b5', 'rgba(255,255,255,0.12)'],
    [fC(q.ms) + ' ms', '#b5b5b5', 'rgba(255,255,255,0.12)'],
    [q.ttft > 0 ? 'ttft ' + fC(q.ttft) + ' ms' : 'ttft —', '#b5b5b5', 'rgba(255,255,255,0.12)'],
    [shortModel(q.model), '#b5b5b5', 'rgba(255,255,255,0.12)'],
    ['stop: ' + (ok(q) ? (q.stop || '—') : '—'), (q.stop === 'max_tokens') ? '#ff7a7a' : '#b5b5b5', (q.stop === 'max_tokens') ? 'rgba(255,90,90,0.4)' : 'rgba(255,255,255,0.12)'],
    ['sid ' + (q.sid || '—'), '#b5b5b5', 'rgba(255,255,255,0.12)']
  ];
  h += '<div style="display:flex;flex-wrap:wrap;gap:7px;margin-top:14px">';
  for (i = 0; i < chips.length; i++) h += '<span class="chip" style="color:' + chips[i][1] + ';border-color:' + chips[i][2] + '">' + esc(chips[i][0]) + '</span>';
  if (unpriced(q)) h += '<span class="badge unp">unpriced</span>';
  h += '</div>';

  if (q.errMsg) h += '<div class="errbox"><div class="m" style="font-size:11.5px;color:#ff7a7a">' + esc(q.status + ' · ' + (q.errType || '')) + '</div>' +
    '<div style="margin-top:6px;font-size:12px;color:#c9c9c9;line-height:1.6">' + esc(q.errMsg) + '</div></div>';

  var tabs = ['request', 'response', 'billing', 'raw', 'breakdown'];
  h += '<div class="tabs">';
  for (i = 0; i < tabs.length; i++) h += '<span class="tab' + (S.tab === tabs[i] ? ' on' : '') + '" data-tab="' + tabs[i] + '">' + tabs[i] + '</span>';
  h += '</div>';

  if (S.tab === 'billing') h += billingTab(q);
  else if (S.tab === 'response') h += responseTab(q, j);
  else if (S.tab === 'raw') h += rawTab(q, j);
  else if (S.tab === 'breakdown') h += breakdownTab(q, j);
  else h += requestTab(q, j);
  return h + '</div>';
}

/* Billing: the row's own quartets. No capture needed — and no dollar figure at
   all when the model had no rate. */
/** @param {recentRow} q @returns {string} */
function billingTab(q) {
  var c = costOf(q), t = tokOf(q), i;
  /** @type {[string, string, number, number][]} colour, label, tokens, cost */
  var lanes = [
    [MRD, 'cache read · billed at 0.1×', t.read || 0, c.read || 0],
    [MIN, 'fresh input · 1×', t.in || 0, c.in || 0],
    [MWR, 'cache write · 1.25×', t.write || 0, c.write || 0],
    [MOU, 'output', t.out || 0, c.out || 0]
  ];
  var h;
  if (unpriced(q)) {
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

/* ---------- breakdown ----------
   The headline tab: where the money leaks. A stacked tools/system/history byte
   bar, then the capture's itemized sections. When the capture is gone the bar
   survives on the row's own byte splits. */
/** @param {recentRow} q @param {captureView|null} j @returns {string} */
function breakdownTab(q, j) {
  var bd = (j && !j.missing && j.breakdown) ? j.breakdown : null;
  var by = bytesOf(q);
  /* Section totals come from the fact row when it carries them (server-folded,
     and equal to the capture's section sums by a tested invariant). Falling back
     to the items we are already drawing keeps the bar honest if it doesn't. */
  var sec = by ? { tools: by.tools || 0, system: by.system || 0, messages: by.messages || 0, total: by.total || 0 }
    : (bd ? sumSections(bd) : null);

  var h = '';
  if (sec) h += stackBar(q, sec);
  else h += '<div class="note" style="padding:20px 0">no byte breakdown recorded for this request</div>';

  if (j === null) return h + '<div class="note" style="padding:16px 0">loading the itemized breakdown…</div>';
  if (!bd) {
    return h + '<div class="warnbox">The capture for this request was deleted, so the itemized tool / system / message tables are gone. ' +
      'The byte splits above come from the request row itself, which is never deleted.</div>';
  }

  h += toolsTable(bd.tools || [], sec);
  h += systemTable(bd.system || []);
  h += messagesTable(bd.messages || []);
  h += flagChips(bd.flags || {});
  return h;
}

/* Summing what we draw is presentation, not folding: these are the denominators
   of the bar above the tables. */
/** @param {Breakdown} bd @returns {byteSplit} */
function sumSections(bd) {
  var s = { tools: 0, system: 0, messages: 0, total: 0 }, i;
  for (i = 0; i < (bd.tools || []).length; i++) s.tools += bd.tools[i].bytes || 0;
  for (i = 0; i < (bd.system || []).length; i++) s.system += bd.system[i].bytes || 0;
  for (i = 0; i < (bd.messages || []).length; i++) s.messages += bd.messages[i].bytes || 0;
  s.total = s.tools + s.system + s.messages;
  return s;
}

/** @param {recentRow} q @param {byteSplit} sec @returns {string} */
function stackBar(q, sec) {
  var tot = sec.total || (sec.tools + sec.system + sec.messages) || 1, i;
  var h = '<div class="hdrline" style="margin-top:24px"><div class="cap">Request composition</div>' +
    '<div class="note">' + fK(sec.total) + ' shipped → ' + fT(tokOf(q).out || 0) + ' tokens out</div></div>' +
    '<div style="margin-top:12px">' + legend(COMP_LEGEND) + '</div>' +
    '<div class="bar" style="height:8px;margin-top:10px">' +
    '<span style="width:' + pctOf(sec.tools, tot) + ';background:' + CTOOL + '"></span>' +
    '<span style="width:' + pctOf(sec.system, tot) + ';background:' + CSYS + '"></span>' +
    '<span style="width:' + pctOf(sec.messages, tot) + ';background:' + CHIST + '"></span></div><div style="margin-top:4px">';
  /** @type {[string, string, number][]} colour, label, bytes */
  var rows = [[CTOOL, 'tool schemas', sec.tools], [CSYS, 'system prompt', sec.system], [CHIST, 'message history', sec.messages]];
  for (i = 0; i < rows.length; i++) {
    h += '<div class="audrow">' + swatch(rows[i][0]) +
      '<span style="font-size:12.5px;color:#c9c9c9">' + esc(rows[i][1]) + '</span>' +
      '<span class="m r" style="font-size:11.5px;color:#c9c9c9">' + fK(rows[i][2]) + '</span>' +
      '<span class="m r" style="font-size:11.5px;color:#6f6f6f">' + ((rows[i][2] / tot) * 100).toFixed(0) + '%</span></div>';
  }
  return h + '</div>';
}

/* The server sorts tools descending by size, so tools[0] is the bar's scale. */
/** @param {ToolItem[]} tools @param {byteSplit|null} sec @returns {string} */
function toolsTable(tools, sec) {
  if (!tools.length) return '<div class="hdrline" style="margin-top:30px"><div class="cap">Tool schemas</div></div>' +
    '<div class="note" style="padding:12px 0">this request shipped no tool schemas</div>';
  var big = tools[0].bytes || 1, i;
  var shown = S.toolsAll ? tools.length : Math.min(12, tools.length);
  var share = (sec && sec.total > 0) ? Math.round((sec.tools / sec.total) * 100) + '% of the request' : '';
  var h = '<div class="hdrline" style="margin-top:30px"><div class="cap">Tool schemas <span class="sub">· largest first</span></div>' +
    '<div class="note">' + tools.length + ' schemas · ' + fK(sec ? sec.tools : 0) + (share ? ' · ' + share : '') + '</div></div><div style="margin-top:6px">';
  for (i = 0; i < shown; i++) {
    var t = tools[i];
    h += '<div class="toolrow">' +
      '<span class="m ell" style="font-size:11.5px;color:#d8d8d8" title="' + esc(t.name) + '">' + esc(t.name) + '</span>' +
      '<span class="track"><i style="width:' + (((t.bytes || 0) / big) * 100).toFixed(1) + '%"></i></span>' +
      '<span class="m r" style="font-size:11px;color:#9f9f9f">' + fK(t.bytes) + '</span>' +
      '<span class="m r" style="font-size:10px;color:#6f6f6f">' + (sec && sec.total > 0 ? (((t.bytes || 0) / sec.total) * 100).toFixed(1) + '%' : '') + '</span></div>';
  }
  h += '</div>';
  if (tools.length > 12) h += '<div class="more" id="toolsall">' + (S.toolsAll ? 'show less' : 'show all ' + tools.length + ' schemas') + '</div>';
  return h;
}

/** @param {SystemItem[]} sys @returns {string} */
function systemTable(sys) {
  if (!sys.length) return '';
  var h = '<div class="hdrline" style="margin-top:30px"><div class="cap">System blocks</div>' +
    '<div class="note">' + sys.length + (sys.length === 1 ? ' block' : ' blocks') + '</div></div><div style="margin-top:6px">', i;
  for (i = 0; i < sys.length; i++) {
    var cc = sys[i].cacheControl;
    h += '<div class="sysrow">' +
      '<span class="m" style="font-size:10.5px;color:#8f8f8f">block ' + i + '</span>' +
      '<span>' + (cc ? '<span class="chip on">cache_control ' + esc(cc) + '</span>' : '<span class="chip off">no cache_control</span>') + '</span>' +
      '<span class="m r" style="font-size:11px;color:#9f9f9f">' + fK(sys[i].bytes) + '</span></div>';
  }
  return h + '</div>';
}

/** @param {MessageItem[]} msgs @returns {string} */
function messagesTable(msgs) {
  if (!msgs.length) return '';
  var h = '<div class="hdrline" style="margin-top:30px"><div class="cap">Messages</div>' +
    '<div class="note">' + msgs.length + (msgs.length === 1 ? ' message' : ' messages') + '</div></div><div style="margin-top:6px">', i, k;
  for (i = 0; i < msgs.length; i++) {
    var m = msgs[i], kinds = m.blockKinds || [], ks = '';
    for (k = 0; k < kinds.length; k++) ks += '<span class="chip off" style="margin-right:5px">' + esc(kinds[k]) + '</span>';
    h += '<div class="msgrow">' +
      '<span class="m" style="font-size:10.5px;color:' + (m.role === 'user' ? '#9a9a9a' : '#d6d6d6') + '">' + esc(m.role || '·') + '</span>' +
      '<span class="ell">' + (ks || '<span class="note">—</span>') + '</span>' +
      '<span class="m r" style="font-size:11px;color:#9f9f9f">' + fK(m.bytes) + '</span></div>';
  }
  return h + '</div>';
}

/** @param {Partial<Flags>} f @returns {string} */
function flagChips(f) {
  var L = [['thinking', f.thinking], ['context management', f.contextManagement], ['output config', f.outputConfig]], i;
  var h = '<div class="cap" style="margin-top:30px">Request flags</div><div style="display:flex;gap:7px;margin-top:10px">';
  for (i = 0; i < L.length; i++) h += '<span class="chip ' + (L[i][1] ? 'on' : 'off') + '">' + esc(L[i][0]) + (L[i][1] ? ' on' : ' off') + '</span>';
  return h + '</div>';
}

/* ---------- request / response / raw ----------
   The capture's request is the client's body verbatim; its response is the
   assembled message (blocks, model, stop_reason, usage). */
/** @param {*} v @returns {string} */
function inspText(v) {
  if (typeof v === 'string') return v;
  if (Object.prototype.toString.call(v) === '[object Array]') {
    var out = [], i;
    for (i = 0; i < v.length; i++) out.push(typeof v[i] === 'string' ? v[i] : (v[i] && v[i].text) || JSON.stringify(v[i]));
    return out.join('\n');
  }
  return v == null ? '' : JSON.stringify(v, null, 2);
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
/* The captured response is an anthropic.Response — `content` and `stop_reason`.
   The `raw` and `blocks` probes below predate it and no current server sends
   either; they cost nothing and they are why this reads as `any` rather than as
   the struct. */
/** @param {*} resp @returns {string} */
function respText(resp) {
  if (!resp) return '';
  if (resp.raw) return resp.raw;
  var out = [], i, bl = resp.blocks || resp.content || [];
  for (i = 0; i < bl.length; i++) {
    var b = bl[i] || {};
    if (b.type === 'tool_use') out.push('tool_use · ' + b.name + ' — ' + (b.text || JSON.stringify(b.input || {})));
    else out.push(b.type + ' — ' + (b.text || ''));
  }
  if (resp.stop_reason) out.push('stop_reason: ' + resp.stop_reason);
  return out.join('\n\n');
}
/** @param {recentRow} q @returns {string} */
function gone(q) {
  return '<div class="warnbox">The capture for request #' + esc(q.id) + ' was deleted — its body is gone. ' +
    'The request row survives: billing and the byte breakdown still work.</div>';
}
/** @returns {string} */
function loading() { return '<div class="note" style="padding:16px 0">loading…</div>'; }

/** @param {recentRow} q @param {captureView|null} j @returns {string} */
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
  var by = bytesOf(q);
  var h = '<div class="hdrline" style="margin-top:24px"><div class="cap">System prompt</div>' +
    '<div class="note">' + (by ? fK(by.system) : '') + '</div></div>' +
    '<pre>' + esc(inspText(sys)) + '</pre>';
  h += '<div class="hdrline" style="margin-top:26px"><div class="cap">Message history</div>' +
    '<div class="note">' + msgs.length + ' messages' + (by ? ' · ' + fK(by.messages) : '') + '</div></div><div style="margin-top:8px">';
  var skip = Math.max(0, msgs.length - 14);
  if (skip) h += '<div class="histrow"><span class="m" style="font-size:10.5px;color:#5f5f5f">·</span>' +
    '<span class="m ell" style="font-size:11.5px;color:#a8a8a8">… ' + skip + ' earlier turns held in context</span><span></span></div>';
  for (i = skip; i < msgs.length; i++) {
    var m = msgs[i] || {};
    h += '<div class="histrow">' +
      '<span class="m" style="font-size:10.5px;color:' + (m.role === 'user' ? '#9a9a9a' : '#d6d6d6') + '">' + esc(m.role || '·') + '</span>' +
      '<span class="m ell" style="font-size:11.5px;color:#a8a8a8">' + esc(trunc(msgPreview(m), 160)) + '</span>' +
      '<span class="m r" style="font-size:10.5px;color:#5f5f5f">' + fK(JSON.stringify(m).length) + '</span></div>';
  }
  return h + '</div>';
}

/** @param {recentRow} q @param {captureView|null} j @returns {string} */
function responseTab(q, j) {
  if (j === null) return loading();
  if (j.missing) return gone(q);
  return '<div class="hdrline" style="margin-top:24px"><div class="cap">Assembled response</div>' +
    '<div class="note">' + fT(tokOf(q).out || 0) + ' tokens · stop ' + esc(q.stop || '—') + '</div></div>' +
    '<pre style="max-height:360px">' + esc(respText(j.response)) + '</pre>';
}

/** @param {recentRow} q @param {captureView|null} j @returns {string} */
function rawTab(q, j) {
  if (j === null) return loading();
  if (j.missing) return gone(q);
  var raw = JSON.stringify(j.request, null, 2) || '';
  if (raw.length > 400000) raw = raw.slice(0, 400000) + '\n… truncated';
  var lines = raw.split('\n'), i, by = bytesOf(q);
  var h = '<div class="hdrline" style="margin-top:24px"><div class="cap">Raw request body</div>' +
    '<div class="note">' + (by ? fK(by.total) : fK(raw.length)) + '</div></div><div class="rawbox">';
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
      S.tab = tab;
      var q = rowOf(S.id);
      if (q) { $('#insp').innerHTML = inspShell(q, CAP); wireInsp(); }
    };
  })(/** @type {string} */ (t[i].getAttribute('data-tab'))); // data-tab is written three lines up in inspShell
  var ta = $('#toolsall');
  if (ta) ta.onclick = function () {
    S.toolsAll = !S.toolsAll;
    var q = rowOf(S.id);
    if (q) { $('#insp').innerHTML = inspShell(q, CAP); wireInsp(); }
  };
}

/* ---------- shell ---------- */
function render() {
  $('#app').innerHTML = renderHome();
  wire();
}

function wire() {
  var i, els = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('[data-id]'));
  for (i = 0; i < els.length; i++) els[i].onclick = (function (id) {
    return function () { hideTip(); openInsp(id); };
  })(Number(els[i].getAttribute('data-id')));

  els = document.querySelectorAll('[data-b]');
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
    // data-b indexes the very array TIPDATA.buckets was set from, so the `|| {}`
    // is a belt on a brace: the lookup cannot miss. Asserted, not widened, so the
    // bucket's fields stay checked inside the handler.
  })(/** @type {bucket} */ ((TIPDATA.buckets || [])[Number(els[i].getAttribute('data-b'))] || {}));

  els = document.querySelectorAll('.col');
  for (i = 0; i < els.length; i++) els[i].onmouseleave = hideTip;
}

function poll() {
  fetch('/api/stats').then(function (r) { return r.json(); }).then(/** @param {statsView} j */ function (j) {
    if (!j) return;
    D = j;
    D.overview = j.overview || { timeline: [], latency: {} };
    D.recent = j.recent || [];
    var host = String(j.upstream || '').replace(/^https?:\/\//, '').replace(/\/.*$/, '');
    $('#route').textContent = 'localhost:' + j.port + ' → ' + host;
    $('#hdrstat').textContent = fC(j.traced) + ' traced · ' + fM(j.cost, 2) + ' total';
    var u = $('#unp'), n = j.unpricedReqs || 0;
    u.textContent = n + (n === 1 ? ' unpriced request' : ' unpriced requests');
    u.style.display = n > 0 ? 'inline-block' : 'none';
    render();
  }).catch(function () { /* the server is restarting; the next poll picks it up */ });
}

TIP = $('#tip');
$('#scrim').onclick = closeInsp;
window.addEventListener('keydown', function (e) { if (e.key === 'Escape' && S.id !== null) closeInsp(); });
render();
poll();
setInterval(poll, 2000);
