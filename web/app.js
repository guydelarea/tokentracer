/* TokenTracer dashboard. Vanilla JS, no build step, no external requests.
 *
 * Two rules carried over from tokentrace, both load-bearing:
 *
 *  1. Everything untrusted goes through esc() before it becomes HTML. Log content
 *     is whatever the model and the tools emitted — treat it as attacker-influenced.
 *  2. Numbers are folded server-side in Go; this file only words and draws them.
 *     No cost, burn rate, percentile or aggregate is computed here. The one
 *     exception is presentation: bar widths and the percentages beside them.
 */

var MRD = '#2fbf87', MIN = '#5aa2f7', MWR = '#d9a04e', MOU = '#ededed', MER = '#ff5a5a';
var CTOOL = '#c9c9c9', CSYS = '#7a7a7a', CHIST = '#454545';

var D = {
  port: '', upstream: '', traced: 0, cost: 0, unpricedReqs: 0,
  overview: { timeline: [], latency: {} }, recent: []
};
var S = { id: null, tab: 'request', toolsAll: false };
var PV = {}, PVT = {};
var CAP = null;                // the open inspector's fetched capture, cached across tab switches
var MISSING = { missing: true }; // /api/capture said 404 — the capture row was deleted

function $(s) { return document.querySelector(s); }
function esc(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}
function fT(n) { n = n || 0; if (n < 1000) return String(n); if (n < 1e6) return (n / 1e3).toFixed(1) + 'k'; return (n / 1e6).toFixed(2) + 'M'; }
function fK(b) { b = b || 0; if (b < 1024) return b + ' B'; return (b / 1024).toFixed(1) + ' KB'; }
function fC(n) { return String(Math.round(n || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ','); }
function fM(v, d) { return '$' + (v || 0).toFixed(d); }
function p2(n) { return (n < 10 ? '0' : '') + n; }
function clock(t) { var d = new Date(t); return p2(d.getHours()) + ':' + p2(d.getMinutes()) + ':' + p2(d.getSeconds()); }
function clockHM(t) { var d = new Date(t); return p2(d.getHours()) + ':' + p2(d.getMinutes()); }
function trunc(s, n) { s = String(s || ''); return s.length > n ? s.slice(0, n - 1) + '…' : s; }
function shortModel(m) { return String(m || '').replace('claude-', '').replace(/-\d{8}$/, ''); }
function pctOf(v, t) { return t > 0 ? ((v / t) * 100).toFixed(1) + '%' : '0%'; }
function tms(r) { return new Date(r.time).getTime(); }
function ok(r) { return r.status < 400; }

/* Fade a number in when it moves by more than 10% — the eye catches the change
   without the page ever animating a still figure. */
function anim(k, num) {
  var now = Date.now(), prev = PV[k];
  if (prev === undefined || Math.abs(num - prev) / Math.max(Math.abs(prev), 1e-9) > 0.1) PVT[k] = now;
  PV[k] = num;
  return (now - (PVT[k] || 0) < 750) ? 'ttNum 0.5s ease-out' : 'none';
}

function swatch(c) { return '<span class="sw" style="background:' + c + '"></span>'; }
function mix(rd, inp, wr, out) {
  var t = rd + inp + wr + out;
  return '<span class="mixbar">' +
    '<span style="width:' + pctOf(rd, t) + ';background:' + MRD + '"></span>' +
    '<span style="width:' + pctOf(inp, t) + ';background:' + MIN + '"></span>' +
    '<span style="width:' + pctOf(wr, t) + ';background:' + MWR + '"></span>' +
    '<span style="width:' + pctOf(out, t) + ';background:' + MOU + '"></span></span>';
}
function legend(pairs) {
  var h = '<div class="leg">', i;
  for (i = 0; i < pairs.length; i++) h += '<div>' + swatch(pairs[i][0]) + '<span class="t">' + esc(pairs[i][1]) + '</span></div>';
  return h + '</div>';
}
var SPEND_LEGEND = [[MRD, 'cache read'], [MIN, 'fresh input'], [MWR, 'cache write'], [MOU, 'output']];
var COMP_LEGEND = [[CTOOL, 'tool schemas'], [CSYS, 'system'], [CHIST, 'history']];

/* ---------- row accessors ----------
   The /api/stats contract groups a row's tokens and costs into quartets. Nothing
   below invents a number; rowCost sums the row's own four cost components purely
   so the row can print one figure. */
function rowOf(id) { for (var i = 0; i < D.recent.length; i++) if (D.recent[i].id === id) return D.recent[i]; return null; }
function tokOf(q) { return q.tok || {}; }
function costOf(q) { return q.cost || {}; }
function rowCost(q) { var c = costOf(q); return (c.in || 0) + (c.read || 0) + (c.write || 0) + (c.out || 0); }
function unpriced(q) { return q.priced === false; }
/* Byte splits ride on the row when the server ships them; the breakdown tab
   degrades to a bare note when they are absent. */
function bytesOf(q) { return q.bytes || null; }

/* ---------- tooltip ---------- */
var TIP = null, TIPDATA = {};
function tipHtml(title, rows, foot) {
  var h = '<div class="t">' + esc(title) + '</div>', i;
  for (i = 0; i < rows.length; i++) {
    h += '<div class="r">' + swatch(rows[i][0]) + '<span class="n">' + esc(rows[i][1]) + '</span><span class="v">' + esc(rows[i][2]) + '</span></div>';
  }
  if (foot) h += '<div class="f">' + esc(foot) + '</div>';
  return h;
}
function showTip(ev, html) {
  var el = TIP;
  el.innerHTML = html;
  el.style.display = 'block';
  var r = ev.currentTarget.getBoundingClientRect(), w = el.offsetWidth, h = el.offsetHeight;
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
function opParts(q) {
  if (!ok(q)) return { tag: 'error', tagC: MER, name: String(q.status), args: q.errType || q.errMsg || '' };
  var op = q.op || 'text — completion';
  var em = op.indexOf(' — '), head = em >= 0 ? op.slice(0, em) : op, args = em >= 0 ? op.slice(em + 3) : '';
  var dm = head.indexOf(' · '), tag = dm >= 0 ? head.slice(0, dm) : head, name = dm >= 0 ? head.slice(dm + 3) : '';
  if (tag === 'text') return { tag: 'text', tagC: '#9f9f9f', name: '', args: args || name };
  return { tag: tag, tagC: MIN, name: name, args: args };
}

/* ---------- overview ---------- */
function renderHome() {
  var ov = D.overview || {}, i;
  var win = ov.windowMin || 10; // the server owns the window; 10 min is tokentrace's default
  var winCap = 'last ' + win + ' min';

  var burnNow = ov.burnNow || 0, burnAvg = ov.burnAvg || 0;
  var ratio = burnAvg > 0 ? burnNow / burnAvg : 1, burnSub;
  if (!D.traced) burnSub = 'no requests recorded yet';
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
function hpc(v, max) { return (((v || 0) / max) * 96).toFixed(2) + '%'; }

/* ---------- request log ----------
   One row per request — the flat log that replaced tokentrace's session table. */
function requestLog() {
  var R = D.recent || [], i, maxMs = 1;
  for (i = 0; i < R.length; i++) if (R[i].ms > maxMs) maxMs = R[i].ms;

  var h = '<div style="margin-top:50px"><div class="hdrline"><div class="cap">Requests <span class="sub">· newest first · click a row to inspect</span></div>' +
    '<div class="note">' + fC(D.traced) + ' traced' + (D.unpricedReqs > 0 ? ' · ' + fC(D.unpricedReqs) + ' unpriced' : '') + '</div></div>' +
    '<div class="thead tgrid"><span>Time</span><span>Model</span><span>Session</span><span>Reply</span><span>St</span>' +
    '<span>Stop</span><span class="r">ms</span><span class="r">TTFT</span><span>Token mix</span>' +
    '<span class="r">in → out</span><span class="r">Cost</span></div>';

  if (!R.length) {
    var at = D.port ? '<span class="m">ANTHROPIC_BASE_URL=http://localhost:' + esc(D.port) + '</span>' : 'this proxy';
    return h + '<div class="empty">No requests yet. Point your client at ' + at + ' — rows land here within two seconds.</div></div>';
  }

  h += '<div class="tlist">';
  for (i = 0; i < R.length; i++) h += reqRow(R[i], maxMs);
  return h + '</div></div>';
}

function reqRow(q, maxMs) {
  var o = opParts(q), isErr = !ok(q), t = tokOf(q);
  var stC = isErr ? MER : 'rgba(47,191,135,0.85)', stB = isErr ? 'rgba(255,90,90,0.4)' : 'rgba(47,191,135,0.28)';
  var inTot = (t.in || 0) + (t.read || 0) + (t.write || 0);
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
  else if (isErr) cost = '<span class="num" style="color:#5f5f5f">—</span>';
  else cost = '<span class="num" style="color:#e8e8e8">' + fM(rowCost(q), 4) + '</span>';

  return '<div class="trow tgrid" data-id="' + esc(q.id) + '">' +
    '<span class="m" style="font-size:10.5px;color:#8f8f8f">' + esc(clock(tms(q))) + '</span>' +
    '<span class="m ell" style="font-size:10.5px;color:#b9b9b9">' + esc(shortModel(q.model)) + '</span>' +
    '<span class="m ell" style="font-size:10px;color:#6f6f6f" title="' + esc(q.sid) + '">' + esc(trunc(q.sid || '—', 9)) + '</span>' +
    op +
    '<span class="m" style="font-size:9.5px;color:' + stC + ';border:1px solid ' + stB + ';padding:1px 0;text-align:center">' + esc(q.status) + '</span>' +
    '<span class="m ell" style="font-size:10px;color:' + stopC + '">' + esc(stop) + '</span>' +
    '<span style="display:flex;align-items:center;gap:6px">' +
      '<span style="flex:1 1 0;height:3px;background:rgba(255,255,255,0.05)"><span style="display:block;height:3px;width:' + barW + ';background:' + (isErr ? MER : '#8f8f8f') + '"></span></span>' +
      '<span class="m r" style="flex:none;font-size:10px;color:#7f7f7f">' + fC(q.ms) + '</span></span>' +
    '<span class="m r" style="font-size:10px;color:#7f7f7f">' + (q.ttft > 0 ? fC(q.ttft) : '—') + '</span>' +
    mix(t.read || 0, t.in || 0, t.write || 0, t.out || 0) +
    '<span class="m r" style="font-size:10.5px;color:#b9b9b9">' + (isErr ? '—' : fT(inTot) + ' → ' + fT(t.out || 0)) + '</span>' +
    '<span class="r">' + cost + '</span></div>';
}

/* ---------- inspector ---------- */
function openInsp(id) {
  var q = rowOf(id);
  if (!q) return;
  S.id = id; S.toolsAll = false; CAP = null;
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
    el.innerHTML = inspShell(q, CAP);
    wireInsp();
  }).catch(function () {
    if (S.id !== id) return;
    CAP = MISSING;
    el.innerHTML = inspShell(q, MISSING);
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
function billingTab(q) {
  var c = costOf(q), t = tokOf(q), i;
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
function sumSections(bd) {
  var s = { tools: 0, system: 0, messages: 0, total: 0 }, i;
  for (i = 0; i < (bd.tools || []).length; i++) s.tools += bd.tools[i].bytes || 0;
  for (i = 0; i < (bd.system || []).length; i++) s.system += bd.system[i].bytes || 0;
  for (i = 0; i < (bd.messages || []).length; i++) s.messages += bd.messages[i].bytes || 0;
  s.total = s.tools + s.system + s.messages;
  return s;
}

function stackBar(q, sec) {
  var tot = sec.total || (sec.tools + sec.system + sec.messages) || 1, i;
  var h = '<div class="hdrline" style="margin-top:24px"><div class="cap">Request composition</div>' +
    '<div class="note">' + fK(sec.total) + ' shipped → ' + fT(tokOf(q).out || 0) + ' tokens out</div></div>' +
    '<div style="margin-top:12px">' + legend(COMP_LEGEND) + '</div>' +
    '<div class="bar" style="height:8px;margin-top:10px">' +
    '<span style="width:' + pctOf(sec.tools, tot) + ';background:' + CTOOL + '"></span>' +
    '<span style="width:' + pctOf(sec.system, tot) + ';background:' + CSYS + '"></span>' +
    '<span style="width:' + pctOf(sec.messages, tot) + ';background:' + CHIST + '"></span></div><div style="margin-top:4px">';
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

function flagChips(f) {
  var L = [['thinking', f.thinking], ['context management', f.contextManagement], ['output config', f.outputConfig]], i;
  var h = '<div class="cap" style="margin-top:30px">Request flags</div><div style="display:flex;gap:7px;margin-top:10px">';
  for (i = 0; i < L.length; i++) h += '<span class="chip ' + (L[i][1] ? 'on' : 'off') + '">' + esc(L[i][0]) + (L[i][1] ? ' on' : ' off') + '</span>';
  return h + '</div>';
}

/* ---------- request / response / raw ----------
   The capture's request is the client's body verbatim; its response is the
   assembled message (blocks, model, stop_reason, usage). */
function inspText(v) {
  if (typeof v === 'string') return v;
  if (Object.prototype.toString.call(v) === '[object Array]') {
    var out = [], i;
    for (i = 0; i < v.length; i++) out.push(typeof v[i] === 'string' ? v[i] : (v[i] && v[i].text) || JSON.stringify(v[i]));
    return out.join('\n');
  }
  return v == null ? '' : JSON.stringify(v, null, 2);
}
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
function gone(q) {
  return '<div class="warnbox">The capture for request #' + esc(q.id) + ' was deleted — its body is gone. ' +
    'The request row survives: billing and the byte breakdown still work.</div>';
}
function loading() { return '<div class="note" style="padding:16px 0">loading…</div>'; }

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

function responseTab(q, j) {
  if (j === null) return loading();
  if (j.missing) return gone(q);
  return '<div class="hdrline" style="margin-top:24px"><div class="cap">Assembled response</div>' +
    '<div class="note">' + fT(tokOf(q).out || 0) + ' tokens · stop ' + esc(q.stop || '—') + '</div></div>' +
    '<pre style="max-height:360px">' + esc(respText(j.response)) + '</pre>';
}

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
  var t = document.querySelectorAll('#insp .tab'), i;
  for (i = 0; i < t.length; i++) t[i].onclick = (function (tab) {
    return function () {
      S.tab = tab;
      var q = rowOf(S.id);
      if (q) { $('#insp').innerHTML = inspShell(q, CAP); wireInsp(); }
    };
  })(t[i].getAttribute('data-tab'));
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
  var i, els = document.querySelectorAll('[data-id]');
  for (i = 0; i < els.length; i++) els[i].onclick = (function (id) {
    return function () { hideTip(); openInsp(id); };
  })(Number(els[i].getAttribute('data-id')));

  els = document.querySelectorAll('[data-b]');
  for (i = 0; i < els.length; i++) els[i].onmouseenter = (function (b) {
    return function (ev) {
      var tot = (b.costRead || 0) + (b.costIn || 0) + (b.costWrite || 0) + (b.costOut || 0);
      var rows = [[MRD, 'cache read', fT(b.cacheRead)], [MIN, 'fresh input', fT(b.input)],
                  [MWR, 'cache write', fT(b.cacheWrite)], [MOU, 'output', fT(b.output)]];
      if (b.err > 0) rows.push([MER, 'errors', String(b.err)]);
      showTip(ev, tipHtml(clockHM(b.t) + ' · ' + b.n + (b.n === 1 ? ' request' : ' requests'), rows,
        fT((b.cacheRead || 0) + (b.input || 0) + (b.cacheWrite || 0)) + ' in · ' + fT(b.output) + ' out · ' + fM(tot, 2)));
    };
  })((TIPDATA.buckets || [])[Number(els[i].getAttribute('data-b'))] || {});

  els = document.querySelectorAll('.col');
  for (i = 0; i < els.length; i++) els[i].onmouseleave = hideTip;
}

function poll() {
  fetch('/api/stats').then(function (r) { return r.json(); }).then(function (j) {
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
