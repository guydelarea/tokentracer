import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

// The dashboard has no build step or test runner. Exercise the pure guard
// directly from the shipped script so a harmless polling refactor cannot bring
// back the two-second DOM replacement of an open operation.
const source = readFileSync(new URL('./app.js', import.meta.url), 'utf8');
const match = source.match(/function traceDetailOpen\(\) \{[\s\S]*?\n\}/);
assert.ok(match, 'traceDetailOpen must exist');

let S = { xrow: {} };
const traceDetailOpen = eval('(' + match[0].replace('function traceDetailOpen()', 'function()') + ')');
assert.equal(traceDetailOpen(), false, 'a collapsed trace may refresh');

S = { xrow: { 42: true } };
assert.equal(traceDetailOpen(), true, 'an expanded operation must hold the rendered detail steady');

S = { xrow: { 42: false, 43: false } };
assert.equal(traceDetailOpen(), false, 'closed operations must not block refreshes');

// Session traces are real browser locations. Exercise the URL helpers from the
// shipped script so direct links remain decoded and unrelated query state is
// preserved when entering or leaving a session.
const fromURLMatch = source.match(/function sessionFromURL\(\) \{[\s\S]*?\n\}/);
const URLMatch = source.match(/function sessionURL\(sid\) \{[\s\S]*?\n\}/);
assert.ok(fromURLMatch, 'sessionFromURL must exist');
assert.ok(URLMatch, 'sessionURL must exist');

let window = {
  location: {
    href: 'http://localhost:8787/dashboard?view=cost&session=agent%2F42#spend'
  }
};
const sessionFromURL = eval('(' + fromURLMatch[0].replace('function sessionFromURL()', 'function()') + ')');
const sessionURL = eval('(' + URLMatch[0].replace('function sessionURL(sid)', 'function(sid)') + ')');

assert.equal(sessionFromURL(), 'agent/42', 'a direct session URL must hydrate the selected session');
assert.equal(
  sessionURL('agent/99'),
  '/dashboard?view=cost&session=agent%2F99#spend',
  'opening a session must preserve other URL state'
);
assert.equal(
  sessionURL(null),
  '/dashboard?view=cost#spend',
  'returning to the overview must remove only the session'
);

window.location.href = 'http://localhost:8787/dashboard?session=';
assert.equal(sessionFromURL(), null, 'an empty session parameter is the overview');

assert.match(
  source,
  /var S = \{ sid: sessionFromURL\(\)/,
  'initial page state must hydrate the session from the URL'
);
assert.match(
  source,
  /return '<a class="sgrid srow"[^;]+href="' \+ esc\(sessionURL\(s\.id\)\)/,
  'session rows must expose native links'
);
assert.match(
  source,
  /e\.metaKey \|\| e\.ctrlKey \|\| e\.shiftKey \|\| e\.altKey/,
  'modified clicks must retain native link behavior'
);

// Exercise the shipped navigation functions with a small browser/history
// double. This covers the state transitions behind direct loads and popstate,
// not just the URL string helpers.
const showMatch = source.match(/function showSession\(sid\) \{[\s\S]*?\n\}/);
const navigateMatch = source.match(/function navigateSession\(sid, replace\) \{[\s\S]*?\n\}/);
const popstateMatch = source.match(/window\.addEventListener\('popstate', function \(\) \{([\s\S]*?)\n\}\);/);
assert.ok(showMatch, 'showSession must exist');
assert.ok(navigateMatch, 'navigateSession must exist');
assert.ok(popstateMatch, 'popstate handler must exist');

let T = null;
let renders = 0;
let tracePolls = [];
let inspectorCloses = 0;
const historyCalls = [];

function render() { renders++; }
function pollTrace(sid, flow) { tracePolls.push({ sid, flow }); }
function closeInsp() { inspectorCloses++; }

function setBrowserURL(target) {
  const url = new URL(target, 'http://localhost:8787');
  window.location = {
    href: url.href,
    pathname: url.pathname,
    search: url.search,
    hash: url.hash
  };
}

window.history = {
  pushState(_state, _title, target) {
    historyCalls.push({ method: 'push', target });
    setBrowserURL(target);
  },
  replaceState(_state, _title, target) {
    historyCalls.push({ method: 'replace', target });
    setBrowserURL(target);
  }
};

const showSession = eval('(' + showMatch[0] + ')');
const navigateSession = eval('(' + navigateMatch[0] + ')');
const handlePopState = eval('(function () {' + popstateMatch[1] + '\n})');

setBrowserURL('/dashboard?view=cost#spend');
S = { sid: null, id: null, toolsAll: true, cutAll: true, graph: true, xrow: { 42: true } };
navigateSession('agent/99', false);
assert.deepEqual(
  historyCalls.at(-1),
  { method: 'push', target: '/dashboard?view=cost&session=agent%2F99#spend' },
  'opening a session must push its URL'
);
assert.equal(S.sid, 'agent/99');
assert.deepEqual(tracePolls.at(-1), { sid: 'agent/99', flow: false });

setBrowserURL('/dashboard?view=cost#spend');
handlePopState();
assert.equal(S.sid, null, 'popstate to the overview must clear the session');

setBrowserURL('/dashboard?view=cost&session=agent%2F42#spend');
handlePopState();
assert.equal(S.sid, 'agent/42', 'popstate must restore the session from the URL');
assert.deepEqual(tracePolls.at(-1), { sid: 'agent/42', flow: false });

navigateSession(null, true);
assert.deepEqual(
  historyCalls.at(-1),
  { method: 'replace', target: '/dashboard?view=cost#spend' },
  'normalizing a missing session must replace, not append, history'
);
assert.ok(renders >= 4, 'every navigation must render its destination');
assert.equal(inspectorCloses, 0, 'navigation without an inspector must not close one');

// A missing trace is the only response allowed to erase a deep link. Transient
// failures keep the route in place so the normal poll can retry.
const pollTraceMatch = source.match(/function pollTrace\(sid, flow, forceRender\) \{[\s\S]*?\n\}/);
assert.ok(pollTraceMatch, 'pollTrace must exist');
const pollTraceRequest = eval('(' + pollTraceMatch[0] + ')');
const originalFetch = globalThis.fetch;

try {
  setBrowserURL('/dashboard?session=kept');
  S.sid = 'kept';
  globalThis.fetch = async () => ({ status: 500, ok: false });
  pollTraceRequest('kept', false);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(S.sid, 'kept', 'a transient trace failure must preserve session state');
  assert.equal(window.location.search, '?session=kept', 'a transient trace failure must preserve the URL');

  globalThis.fetch = async () => ({ status: 404, ok: false });
  pollTraceRequest('kept', false);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(S.sid, null, 'a missing trace must return to the overview');
  assert.equal(window.location.search, '', 'a missing trace must remove the session URL');
  assert.equal(historyCalls.at(-1).method, 'replace', 'a missing trace must replace history');
} finally {
  globalThis.fetch = originalFetch;
}
