# HTTP Contract: TokenTracer v1

All endpoints served from one listener bound to `127.0.0.1:PORT` (default 8787). Dashboard/API routes additionally sit behind loopback middleware: non-loopback `RemoteAddr` → `404`.

## Proxy surface (transparent)

### ANY /* (catch-all)

- Forwarded verbatim to `UPSTREAM` (default `https://api.anthropic.com`) with hop-by-hop headers stripped and transport compression disabled.
- Response streamed back with `http.Flusher.Flush()` per chunk. Zero parsing on the client path — the client sees a byte-identical stream.
- **Recorded** only for `POST /v1/messages` (SSE and non-streaming).
- **Proxied but never recorded**: `POST /v1/messages/count_tokens`, all other paths.
- Client abort: captured bytes are kept; the fact row records whatever arrived.
- Non-2xx upstream: body captured; `error.type`/`error.message` extracted to `err_type`/`err_msg`.

## Dashboard surface (loopback only)

### GET /dashboard

The single-page dashboard (embedded `web/index.html`).

### GET /web/*

Static assets via `go:embed`: `app.css`, `app.js`, `logo.svg`.

### GET /api/stats

Server-side fold over `requests`, priced per row by `billing.Compute` at read time. Keeps tokentrace's contract names where the view survives:

```jsonc
{
  "port": 8787,
  "upstream": "https://api.anthropic.com",
  "traced": 123,                 // lifetime request count
  "cost": 1.23,                  // lifetime priced total, USD
  "unpricedReqs": 0,             // rows whose model had no rate (badge in UI)
  "overview": {
    "burnNow": 0.0,              // $/hr, current window
    "burnAvg": 0.0,
    "reqHr": 0,
    "winReqs": 0,
    "avgReq": 0.0,
    "hitNow": 0.0,               // cache hit rate: cache_read / (input + cache_read + w5m + w1h)
    "hitAvg": 0.0,
    "peakMin": 0.0,
    "latency": { "p50Ttft": 0, "p95Ttft": 0 },   // fourth tile (new in TokenTracer)
    "timeline": [ /* 60 per-minute buckets, cost stacked by class: input/cacheRead/cacheWrite/output */ ]
  },
  "recent": [
    {
      "id": 1, "time": "…", "model": "claude-sonnet-5", "sid": "210b4cd3…",
      "op": "tool_use · DesignSync", "status": 200, "ms": 1234, "ttft": 210,
      "stop": "tool_use",
      "tok": { "in": 0, "read": 0, "write": 0, "out": 0 },
      "cost": { "in": 0.0, "read": 0.0, "write": 0.0, "out": 0.0 },
      "priced": true
    }
  ]
}
```

Invariant: all numbers folded server-side in Go; the page only words and draws them.

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
- Capture row deleted → 404; the inspector's breakdown tab falls back to the fact-row stacked bar.

## Error responses

- Non-loopback client on dashboard/API routes: `404`.
- `/api/capture` with unknown/deleted id: `404`.
