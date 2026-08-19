import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

// The dashboard has no build step or test runner. Exercise the pure guards
// directly from the shipped script.
const source = readFileSync(new URL('./app.js', import.meta.url), 'utf8');
const css = readFileSync(new URL('./app.css', import.meta.url), 'utf8');
const match = source.match(/function traceDetailOpen\(\) \{[\s\S]*?\n\}/);
assert.ok(match, 'traceDetailOpen must exist');

let S = { xrow: {} };
const traceDetailOpen = eval('(' + match[0].replace('function traceDetailOpen()', 'function()') + ')');
assert.equal(traceDetailOpen(), false, 'a bare trace has no operation open');

S = { xrow: { 42: true } };
assert.equal(traceDetailOpen(), true, 'an expanded operation is an open detail');

S = { xrow: { 42: false, 43: false } };
assert.equal(traceDetailOpen(), false, 'closed operations leave nothing open');

// The flow is one turn per request. Anything shorter was read before requests
// the session has since made, and the graph drawn from it is behind.
const staleMatch = source.match(/function staleFlow\(t\) \{.*\}/);
assert.ok(staleMatch, 'staleFlow must exist');
const staleFlow = eval('(' + staleMatch[0].replace('function staleFlow(t)', 'function(t)') + ')');
assert.equal(staleFlow({ flow: [1, 2], rows: [1, 2] }), false, 'a flow covering every request is current');
assert.equal(staleFlow({ flow: [1], rows: [1, 2] }), true, 'a flow missing a request is behind');
assert.equal(staleFlow({ rows: [1] }), true, 'a flow never loaded is behind');
assert.equal(staleFlow({ rows: [] }), false, 'an empty session has nothing to re-read');

// A poll frame is marked so the CSS can keep already-visible detail from
// replaying its entry animation. The class and the rule ship together.
assert.match(source, /app\.className = quiet \? 'quiet' : ''/, 'a poll frame must mark itself quiet');
assert.match(css, /#app\.quiet .flow-turn\{animation:none\}/, 'a quiet frame must not replay the flow animation');

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

// The inspector gets each vendor's original wire body. Its adapter must expose
// OpenAI Responses instructions and Chat Completions developer messages as the
// system prompt instead of assuming Anthropic's system/messages shape.
const helpersStart = source.indexOf('function requestView(req)');
const helpersEnd = source.indexOf('/* foldPre renders', helpersStart);
assert.ok(helpersStart >= 0 && helpersEnd > helpersStart, 'inspector wire adapters must exist');
const { requestView, responseBlocks } = new Function(
  'isArr', 'inspText',
  source.slice(helpersStart, helpersEnd) + '; return { requestView, responseBlocks };'
)(
  (v) => Object.prototype.toString.call(v) === '[object Array]',
  (v) => typeof v === 'string' ? v : (v == null ? '' : JSON.stringify(v, null, 2))
);

const responsesRequest = requestView({
  instructions: 'You are OpenCode.',
  input: [
    { role: 'user', content: [{ type: 'input_text', text: 'Fix the parser' }] },
    { type: 'function_call', name: 'read', arguments: '{"path":"parser.go"}' }
  ]
});
assert.equal(responsesRequest.system, 'You are OpenCode.', 'Responses instructions must render as the system prompt');
assert.equal(responsesRequest.messages.length, 2, 'Responses input items must render as history');

const chatRequest = requestView({
  messages: [
    { role: 'developer', content: 'Use tools.' },
    { role: 'user', content: 'Run the tests.' }
  ]
});
assert.equal(chatRequest.system, 'Use tools.', 'Chat Completions developer messages must render as the system prompt');
assert.equal(chatRequest.messages.length, 1, 'system/developer messages must not duplicate into history');

const responseItems = responseBlocks({
  output: [
    { type: 'message', content: [{ type: 'output_text', text: 'Done.' }] },
    { type: 'function_call', name: 'bash', arguments: '{"command":"go test ./..."}' }
  ]
});
assert.equal(responseItems.length, 2, 'Responses output must render messages and tool calls');
assert.equal(responseItems[0].type, 'output_text');
assert.equal(responseItems[1].type, 'function_call');

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
let quietFrames = [];
let tracePolls = [];
let inspectorCloses = 0;
const historyCalls = [];

function render(quiet) { renders++; quietFrames.push(!!quiet); }
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
const pollTraceMatch = source.match(/function pollTrace\(sid, flow, quiet\) \{[\s\S]*?\n\}/);
assert.ok(pollTraceMatch, 'pollTrace must exist');
const pollTraceRequest = eval('(' + pollTraceMatch[0] + ')');
const originalFetch = globalThis.fetch;

/** Let the fetch promise chain drain. */
const settle = () => new Promise((resolve) => setTimeout(resolve, 5));

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

  // The bug this guards: an open graph or an unfolded operation used to stop the
  // session page repainting entirely, so a live session's numbers froze at
  // whatever they were when it was opened. A poll must always draw its frame.
  const served = [];
  globalThis.fetch = async (url) => {
    served.push(url);
    const flow = url.includes('flow=1') ? [{ id: 1 }, { id: 2 }] : undefined;
    return { status: 200, ok: true, json: async () => ({ rows: [{ id: 1 }, { id: 2 }], flow }) };
  };

  for (const reading of [{ what: 'an unfolded operation', S: { sid: 'live', graph: false, xrow: { 1: true } } },
                         { what: 'the session graph', S: { sid: 'live', graph: true, xrow: {} } },
                         { what: 'a plain trace', S: { sid: 'live', graph: false, xrow: {} } }]) {
    S = reading.S;
    T = { rows: [{ id: 1 }], flow: [{ id: 1 }] };
    renders = 0; quietFrames = []; served.length = 0;
    pollTraceRequest('live', false, true);
    await settle();
    assert.ok(renders > 0, `a poll must repaint with ${reading.what} on screen`);
    assert.deepEqual(quietFrames, quietFrames.map(() => true), 'a poll frame must stay quiet');
    const reread = served.filter((url) => url.includes('flow=1'));
    if (reading.S.graph || reading.S.xrow[1]) {
      assert.equal(reread.length, 1, `${reading.what} must re-read the flow the new request is missing from`);
      assert.equal(T.flow.length, 2, `${reading.what} must end up holding the current flow`);
    } else {
      assert.equal(reread.length, 0, 'a trace with nothing open must not pay for the capture read');
      assert.equal(T.flow.length, 1, 'a cheap poll must keep the flow it already had');
    }
  }

  // Once the flow covers every request, the next polls stay cheap.
  S = { sid: 'live', graph: true, xrow: {} };
  T = { rows: [{ id: 1 }, { id: 2 }], flow: [{ id: 1 }, { id: 2 }] };
  served.length = 0;
  pollTraceRequest('live', false, true);
  await settle();
  assert.deepEqual(served.filter((url) => url.includes('flow=1')), [], 'a current flow must not be re-read');
} finally {
  globalThis.fetch = originalFetch;
}

// The provider filter and badge exist only when there is something to choose
// between. With one upstream the whole apparatus stays off the page, which is
// the state most installs are in.
{
  const src = (re, what) => {
    const m = source.match(re);
    assert.ok(m, what + ' must exist');
    return m[0];
  };
  // The three helpers close over the module-scope D and S. Lift them into a
  // factory that takes those as parameters, so the shipped bodies run verbatim
  // against state this test controls.
  const make = eval('(function (D, S) {' +
    src(/function esc\(s\) \{[\s\S]*?\n\}/, 'esc') +
    src(/function filteredSessions\(\) \{[\s\S]*?\n\}/, 'filteredSessions') +
    src(/function upstreamFilter\(\) \{[\s\S]*?\n  return h \+ '<\/div>';\n\}/, 'upstreamFilter') +
    src(/function upstreamBadge\(s\) \{[\s\S]*?\n\}/, 'upstreamBadge') +
    'return { filteredSessions: filteredSessions, upstreamFilter: upstreamFilter, upstreamBadge: upstreamBadge };' +
    '})');

  const sessions = [
    { id: 'a', upstream: 'anthropic' },
    { id: 'b', upstream: 'openai' },
    { id: 'c' } // recorded before the upstream column existed
  ];

  let U = make(
    { port: 8787, upstreams: [{ name: 'anthropic', url: 'u', path: '' }], sessions: sessions },
    { upstream: '' }
  );
  assert.equal(U.upstreamFilter(), '', 'one upstream must draw no filter');
  assert.equal(U.upstreamBadge(sessions[0]), '', 'one upstream must draw no badge');

  const two = [{ name: 'anthropic', url: 'u1', path: '' }, { name: 'openai', url: 'u2', path: '/tt/openai' }];
  U = make({ port: 8787, upstreams: two, sessions: sessions }, { upstream: '' });
  assert.match(U.upstreamFilter(), /data-upstream="openai"/, 'two upstreams must draw a chip each');
  assert.match(U.upstreamFilter(), /chip on" data-upstream=""/, 'with no filter set, "all" is the lit chip');
  assert.match(U.upstreamFilter(), /http:\/\/localhost:8787\/tt\/openai/, 'a route needing a prefix must say so');
  assert.match(U.upstreamBadge(sessions[1]), /openai/, 'a routed session gets its badge');
  assert.equal(U.upstreamBadge(sessions[2]), '', 'a session recorded before routing gets none');
  assert.equal(U.filteredSessions().length, 3, 'no filter shows every session');

  U = make({ port: 8787, upstreams: two, sessions: sessions }, { upstream: 'openai' });
  assert.deepEqual(U.filteredSessions().map((s) => s.id), ['b'], 'a filter shows only that upstream');
  assert.match(U.upstreamFilter(), /chip on" data-upstream="openai"/, 'the active chip is lit');
  assert.match(U.upstreamFilter(), /chip off" data-upstream=""/, 'and "all" is not');
}
