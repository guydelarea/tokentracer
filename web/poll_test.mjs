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
