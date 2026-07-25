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
