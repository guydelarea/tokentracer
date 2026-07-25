# HTTP Contract: TokenTracer v1

All endpoints served from one listener bound to `127.0.0.1:PORT` (default 8787). Dashboard/API routes additionally sit behind loopback middleware: non-loopback `RemoteAddr` → `404`.

## Proxy surface (transparent)

### ANY /* (catch-all)

- Forwarded verbatim to `UPSTREAM` (default `https://api.anthropic.com`) with hop-by-hop headers stripped and transport compression disabled.
- Response streamed back with `http.Flusher.Flush()` per chunk. Zero parsing on the client path — the client sees a byte-identical stream.
- **Recorded** only for `POST /v1/messages` (SSE and non-streaming).
- **Proxied but never recorded**: `POST /v1/messages/count_tokens`, all other paths.
- Client abort: the proxy detaches from the client context and drains upstream to EOF (30s cap) — billed tokens and the final `message_delta` usage are recorded even though the client is gone; the row records `aborted=1`. If the cap hits, the fact row records whatever arrived.
- Non-2xx upstream: body captured; `error.type`/`error.message` extracted to `err_type`/`err_msg`. Precedence: upstream `error.type` wins the column; Recorder rungs (`parse`/`panic`/`oversize`) appear only on otherwise-successful exchanges; a non-2xx whose error body won't parse gets `err_type='http_<status>'`.
- Concurrent requests are supported and normal (Claude Code subagents, background haiku calls) — each streams independently; writes serialize in the Recorder's single worker.
- Unparseable exchange: still recorded — degradation is per side (good-side facts survive; `err_msg` names the broken side) and every rung keeps the capture; recording one bad shape never stops the worker. Insert failure: retry once, then a loud stderr drop — never a crash.
- Shutdown (SIGINT/SIGTERM), in order: `Server.Shutdown` (bounded grace for in-flight streams and abort drains) → `recorder.Close()` (drains unconditionally) → `store.Close()`.

## Dashboard surface (loopback only)

### GET /dashboard

The single-page dashboard (embedded `web/index.html`).

### GET /web/*

Static assets via `go:embed`: `app.css`, `app.js`, `logo.svg`.

### GET /api/stats

The overview: the four tiles, the spend timeline, and the sessions table. Server-side fold over `requests`, priced per row by `billing.Compute` at read time.

There is no flat request log here. A request is not a thing anyone did — a session is — and the twenty requests that made up one turn are noise until you have picked the session they belong to. The requests live one level down, in `/api/trace`.

```jsonc
{
  "port": 8787,
  "upstream": "https://api.anthropic.com",
  "traced": 123,                 // lifetime request count
  "cost": 1.23,                  // lifetime priced total, USD
  "unpricedReqs": 0,             // rows whose model had no rate (badge in UI)
  "unpricedModels": [],          // the distinct model names behind that count, sorted
  "tokens": { "in": 0, "read": 0, "write": 0, "out": 0 },   // lifetime
  "overview": {
    "burnNow": 0.0,              // $/hr, current window
    "burnAvg": 0.0,
    "reqHr": 0,
    "winReqs": 0,
    "avgReq": 0.0,
    "hitNow": 0.0,               // cache hit rate: cache_read / (input + cache_read + w5m + w1h)
    "hitAvg": 0.0,
    "peakMin": 0.0,
    "windowMin": 10,
    "coldStart": false,          // under one window of history: the page must not claim a trend
    "wasteHr": 0.0,              // never-called schemas, LIVE sessions only, at current cadence
    "unusedCount": 0,            // …across how many schemas
    "worstSid": "…",             // …and the session shipping the most of them
    "latency": { "p50Ttft": 0, "p95Ttft": 0 },
    "timeline": [ /* 60 per-minute buckets, cost stacked by class: input/cacheRead/cacheWrite/output */ ]
  },
  "sessions": [
    {
      "id": "210b4cd3…", "label": "port the design", "model": "claude-opus-4-8",
      "live": true, "idle": "12s", "last": "…",
      "req": 42, "err": 0,
      "tok": { "in": 0, "read": 0, "write": 0, "out": 0 },
      "cost": 1.18,              // Σ of the session's own rows, priced individually
      "rateHr": 0.0,             // window spend per hour — 0 once the session stops
      "hit": 0.46,
      "unused": 119,             // schemas shipped and never called
      "unusedTok": 53709, "unusedBytes": 214780,
      "wasteHr": 0.0,            // what re-shipping them costs per hour at this cadence
      "priced": true, "stateless": false, "contextWindow": 200000
    }
  ]
}
```

The one field not folded from the rows — the state of the capture table and the window bounding it. It rides here because the page already polls this endpoint:

```jsonc
{
  "storage": {
    "captureBytes": 44040192,    // Σ gzipped capture size
    "retention": "7d"            // off | 24h | 7d | 30d
  }
}
```

A request whose client never named a session buckets under the display id `(no session id)`. It is a label, not an id: the rows carry `""`, and the trace endpoint maps it back.

### GET /api/trace?sid=SID

One session: every request it made, what its cache did, where its money went, and what to do about it. Unknown session → `404`.

Its own endpoint rather than a field on `/api/stats` because it reads a capture: that cost is paid when someone opens a session, not twice a second for every session they aren't looking at.

```jsonc
{
  "sid": "…", "label": "…", "model": "…", "live": true, "idle": "12s",
  "first": "…", "last": "…", "durMs": 411755,
  "req": 4, "err": 0, "cost": 1.18, "avgReq": 0.39, "priced": true,
  "tok": { "in": 0, "read": 0, "write": 0, "out": 0 },
  "hit": 0.46,
  "ctx": 100272,                 // context on the latest request that SPOKE (never an errored one)
  "contextWindow": 200000,
  "ctxBytes": { "total": 0, "tools": 0, "system": 0, "messages": 0 },

  "rows":  [ /* one per request, chronological — the trace list and every per-request chart */
    {
      "id": 1, "time": "…", "model": "claude-opus-4-8", "sid": "…",
      "op": "tool_use · DesignSync", "status": 200, "ms": 1234, "ttft": 210, "stop": "tool_use",
      "tok":   { "in": 0, "read": 0, "write": 0, "out": 0 },
      "cost":  { "in": 0.0, "read": 0.0, "write": 0.0, "out": 0.0 },
      "shape": { "think": 0, "text": 0, "tool": 0 },   // output tokens by block type (an ESTIMATE)
      "bytes": { "total": 0, "tools": 0, "system": 0, "messages": 0 },
      "priced": true
    }
  ],
  "cache": [ /* parallel to rows, one per request */
    { "id": 4, "class": "break", "cause": "gap", "badIdx": -1, "gapMs": 420000, "rebill": 0.92 }
  ],
  "breaks": 2, "breakCost": 0.925,
  "compacted": [ /* indexes into rows where the context collapsed instead of growing */ ],

  "out":      { "think": 0, "text": 0, "tool": 0, "total": 297, "truncated": 0, "stops": { "end_turn": 3 } },
  "activity": [ { "kind": "explore", "n": 12, "cost": 0.4 } ],   // costliest first
  "buckets":  [ /* 44 equal-TIME columns: { n, err, ms, ttft } — the gaps are the point */ ],

  "tools":     [ { "name": "Workflow", "bytes": 21229, "tokens": 5307, "unused": true, "usd": 0.01 } ],
  "cut":       [ /* …the unused ones: the cut list */ ],
  "unusedTok": 53709, "cutUsd": 0.107,
  "results":   [ { "name": "Read", "bytes": 200000, "n": 12 } ],  // tool output sitting in the context
  "resultBytes": 0, "exploreBytes": 0, "exploreCalls": 0,
  "stateless": false,          // spoke enough times to cache, never once got a read

  "insights": [ { "kind": "cache", "usd": 0.925, "perHr": false, "n": 2 } ],  // top 3, costliest first
  "captureGone": false         // the capture the schemas come from was deleted
}
```

- `class`: `hit` | `prime` | `break` | `fresh` | `none` | `err`. `cause`: `gap` | `tools` | `system` | `msg`.
- `badIdx` is the index in the request's cumulative prefix chain (tools → system → each message) at which it diverged from the request before it — `0` is the toolset, `1` the system prompt, `2+n` message _n_, `-1` when there is no segment to name. That index is not a hint about what broke the cache; it **is** what broke it.
- `rebill` is what a request cost ABOVE what a cache hit would have cost — never the raw bill.
- `insights` are facts with prices, not sentences: Go decides which cards to show and what each is worth; the page only words them.

Invariant: all numbers folded server-side in Go; the page only words and draws them. Both endpoints are pure functions — `fold(lifetime, window, tools, rates, now, cfg)` and `foldTrace(sid, rows, tools, results, captureGone, rates, now)`. The view structs' json tags ARE this contract, so the two cannot drift (contract tests marshal both structs and pin their keys against this document). The handlers are query → fold → encode.

### GET /api/capture?id=N

Gunzips the capture row for request N:

```jsonc
{
  "request":   { /* verbatim client request body, parsed JSON */ },
  "response":  { /* assembled response message: blocks, model, stop_reason, usage */ },
  "breakdown": {                      // anthropic.BreakdownRequest, folded server-side
    "tools":    [ { "name": "Workflow", "bytes": 21500 } /* sorted desc */ ],
    "system":   [ { "bytes": 1234, "cacheControl": "ephemeral/1h" } ],
    "messages": [ { "role": "user", "bytes": 456, "blockKinds": ["text"] } ],
    "flags":    { "thinking": true, "contextManagement": true, "outputConfig": true }
  }
}
```

- `request`/`response` are the shape the reused inspector already consumes.
- Section byte sums equal the fact row's `tools_bytes`/`system_bytes`/`messages_bytes` (tested invariant).
- Capture row deleted → 404; the inspector's context tab falls back to the fact-row stacked bar, and the trace's schema/cut-list panels say so via `captureGone`. Everything folded from the fact rows survives.

Captures are redacted before they are stored (`internal/redact`): vendor key shapes, private-key blocks, JWTs, and named credential values in JSON, headers and env assignments come back as `[redacted:<kind>]`. The facts are folded from the verbatim bytes first, so no byte count, prefix hash or token figure is affected — the capture is the only thing that changes.

### POST /api/settings?retention=off|24h|7d|30d

Sets the capture retention window and sweeps immediately. `204` on success, `400` on any other value — a window we cannot interpret is never read as permission to delete. The default is `off`; a background sweep re-applies the stored window hourly and once at startup.

### POST /api/purge

Deletes every capture now, regardless of the window. `204`. Fact rows are untouched: this reclaims disk, it does not erase history.

## Error responses

- Non-loopback client on dashboard/API routes: `404`. This includes the two write routes, which delete data.
- `/api/capture` with unknown/deleted id: `404`.
- `/api/settings` with an unknown retention window: `400`.
