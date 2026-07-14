# Data Model: TokenTracer v1

**Phase 1 output** | Date: 2026-07-13

Two persisted entities (SQLite), four in-memory value types. Governing invariant: **facts, not interpretations** — usage is stored verbatim from the API; cost is computed at read time; the capture blob is the interpretation source for drill-down and can be deleted without touching facts.

## Persisted entities

### requests — one row per recorded exchange (the facts)

```sql
CREATE TABLE requests (
  id            INTEGER PRIMARY KEY,          -- rowid
  ts_ms         INTEGER NOT NULL,             -- unix ms, arrival
  endpoint      TEXT NOT NULL,                -- "POST /v1/messages"
  session_id    TEXT,                         -- NULL → 'unknown' at read time
  model_req     TEXT NOT NULL,
  model_served  TEXT,                         -- from message_start
  status        INTEGER NOT NULL,
  streamed      INTEGER NOT NULL,
  aborted       INTEGER NOT NULL DEFAULT 0,   -- client hung up; upstream drained, tokens still billed
  duration_ms   INTEGER,
  ttft_ms       INTEGER,                      -- arrival → first upstream body byte
  stop_reason   TEXT,
  op            TEXT,                         -- display: "tool_use · Bash — git st…"
  label         TEXT,                         -- first user text, ≤64 chars
  -- usage verbatim from the API (facts; never priced here)
  input_tokens INTEGER, output_tokens INTEGER,
  cache_read_tokens INTEGER, cache_w5m_tokens INTEGER, cache_w1h_tokens INTEGER,
  -- request shape (the composition facts behind "where money leaks")
  turns INTEGER, tool_count INTEGER,
  total_bytes INTEGER, tools_bytes INTEGER, system_bytes INTEGER, messages_bytes INTEGER,
  err_type TEXT, err_msg TEXT,
  max_tokens INTEGER,                          -- migration 2: the request's own output cap; 1 = a probe, not a failure
  -- migration 3: the two derived facts the session trace cannot fold without.
  -- Both come from bodies we only hold at record time, and both must outlive the
  -- capture they came from — a capture can be deleted; a diagnosis should not die with it.
  think_tokens INTEGER,                        -- output tokens by block type: an ESTIMATE, split by
  text_tokens  INTEGER,                        -- block bytes, because the API bills one output figure
  tool_tokens  INTEGER,                        -- and never says which block spent it
  prefix       TEXT                            -- JSON array: cumulative cache-prefix hashes, tools → system → each message
);
CREATE INDEX idx_requests_ts      ON requests(ts_ms);
CREATE INDEX idx_requests_session ON requests(session_id, ts_ms);
```

Field sources (validated against the real fixture — see spec extraction table):

| Column | Source |
|---|---|
| `model_req`, `streamed`, `turns`, `tool_count` | request JSON (`model`, `stream`, `len(messages)`, `len(tools)`) |
| `session_id` | regex `"session_id"\s*:\s*"([^"]+)"` over `request.metadata.user_id`; no match → NULL |
| `total/tools/system/messages_bytes` | marshaled sizes of request / `.tools` / `.system` / `.messages` |
| `label` | first **user-role** text block, ≤64 chars (messages may include role `"system"` mid-conversation — skip those) |
| `model_served`, usage columns | SSE `message_start` seeded, `message_delta` merged (later values win per key) |
| `stop_reason`, `op` | assembled response |
| `ttft_ms`, `duration_ms` | proxy timing stamps (t0 → first upstream body byte; t0 → EOF) |
| `err_type`, `err_msg` | non-2xx body `error.type` / `error.message` (precedence: upstream wins; Recorder rungs `parse`/`panic`/`oversize` only on otherwise-successful exchanges; unparseable non-2xx body → `http_<status>`) |
| `aborted` | proxy's `ClientAborted` flag — the hangup is a wire fact that exists for one instant in the proxy |
| `think/text/tool_tokens` | `anthropic.SplitOutput` — billed `output_tokens` apportioned across the response's blocks by their bytes. Text takes the remainder, so the three always sum to exactly what the API billed |
| `prefix` | `anthropic.PrefixHashes` — a hash chained over the request's cache prefix (tools, then system, then each message), hashing the **raw wire bytes**. Prompt caching is a prefix match over that sequence, so the first index at which two requests' chains differ *is* what invalidated the cache: `0` the toolset, `1` the system prompt, `2+n` message *n* |

Validation rules:
- `cache_w5m_tokens` / `cache_w1h_tokens` come from `message_start`'s cache_creation 5m/1h split.
- Usage columns are nullable — a client abort or upstream error records whatever facts arrived (abort additionally drains upstream to EOF, 30s cap, so usage is normally complete).
- A body the parser doesn't understand still gets a row: `err_type='parse'` (or `'panic'` if recovered) plus the facts that need no parser — timing, status, byte sizes; the capture is still stored. Degradation is **per side**: a bad request body doesn't discard good response facts, or vice versa; `err_msg` names the broken side. The Recorder never loses an exchange it saw; the ladder bottom (insert failure) is retry-once, then a loud stderr drop.
- No cost column, ever.

### captures — drill-down blobs (the interpretations source)

```sql
CREATE TABLE captures (
  request_id  INTEGER PRIMARY KEY REFERENCES requests(id),
  request_gz  BLOB NOT NULL,                  -- verbatim request body, gzipped
  response_gz BLOB                            -- assembled response message JSON, gzipped
);
```

- `request_gz`: exact bytes the client sent — never re-serialized (unknown fields like `thinking`, `context_management`, `output_config` round-trip unharmed).
- `response_gz`: full assembled message — every block's complete content (thinking text + signature, tool_use `id`/`name`/`input` JSON, text) plus `model`, `stop_reason`, `usage`. NULL if the response never completed.
- **Relationship**: optional 1:1 with `requests`, inserted in the **same transaction** as the fact row — never a capture without facts. `DELETE FROM captures` never touches `requests`; the inspector degrades to fact-row byte splits.
- **Bodies only, never headers** — the API key lives in `x-api-key`/`authorization`; keeping headers out of the DB is a stated invariant.
- Response tee is capped (8 MB); overflow keeps the head and records `err_type='oversize'`.

### schema_migrations

Applied-migration bookkeeping: version integer per applied entry from the ordered SQL slice in `store.go`.

## In-memory value types (Go)

### record.Exchange — proxy → Recorder handoff

`{Start, TTFT, Duration, Method, Path, Status, Streamed, ReqBody, RespBody, RespTruncated, ClientAborted}` — raw material; no parsing happened yet. Defined in `internal/record` along with the `Sink` interface (`Record(Exchange)`); the proxy imports record and filters — every Exchange handed over becomes a row. `record.New(st *store.Store)` borrows the store; `Record` blocks when the bounded queue is full (backpressure over loss, post-stream so the client never feels it); `Close()` drains unconditionally — main owns the shutdown deadline.

### anthropic.RequestFacts — ParseRequest output

Model, session id, stream flag, turns, tool count, tools/system/messages/total byte splits, label. Parsing rules the real data dictates: message/system `content` is string **or** block array; roles include `"system"` mid-conversation; `cache_control` may appear on system blocks and inside message blocks — never hard-code locations; never re-serialize the body.

### anthropic.Response — DecodeSSE / DecodeJSON output

Assembled message: blocks (text / thinking / tool_use with accumulated `input_json_delta`), model, stop_reason, merged usage. Non-2xx → `err_type` / `err_msg` extracted instead.

### anthropic.Breakdown — BreakdownRequest output (read-time, from request_gz)

`Tools []{Name, Bytes}` sorted desc; `System []{Bytes, CacheControl}`; `Messages []{Role, Bytes, BlockKinds}`; feature flags (`thinking` / `context_management` / `output_config`). Shares ParseRequest's marshaling rules so **section sums equal the fact-row byte columns** (tested). Not persisted — pure interpretation of the blob.

### api.statsView — fold output

The return type of `fold(lifetime, window, recent, rates, now)` in `internal/api/fold.go` — a pure function, `now` as a parameter. Its json tags **are** the `/api/stats` wire contract; there is no translation layer. All aggregation happens here, per row, because the long-context tier and time-windowed rates make pricing non-additive — SQL sums cannot price correctly. Inputs come from three targeted store queries: `Lifetime()`, `Window(since)`, `Recent(n)`.

### billing.Rate / billing.Bill

`Rate{Key, InPerM, OutPerM, From, Until, LongCtxThreshold, LongCtxInPerM, LongCtxOutPerM}` — ordered slice, substring match on normalized model (strip `anthropic.` prefix, `@date` suffix), first hit wins, `[From, Until)` windows. Multipliers: read 0.1, write-5m 1.25, write-1h 2.0. Long-context tier applies when total input (input + cache reads + writes) exceeds the threshold (Anthropic's >200K premium pricing). Table generated from LiteLLM's `model_prices_and_context_window.json`, not hand-typed.
`Bill{Priced bool; In, Read, Write, Out, Total float64}` — `Priced:false` for unknown models, propagated to UI (badge + `unpricedReqs`); never a silent $0.

## State transitions

A recorded exchange has one lifecycle: **arrived → forwarded → streamed/completed (or aborted → upstream drained, or errored) → parsed by the Recorder (parse failure/panic degrades to an `err_type` row, never a dropped row) → inserted in one transaction** (`requests` always; `captures` when there's a body to keep). Rows are immutable after insert; the only mutation in the system is manual capture deletion. On shutdown the Recorder's queue is drained before the DB closes (`Server.Shutdown` → `recorder.Close()` → `store.Close()`).
