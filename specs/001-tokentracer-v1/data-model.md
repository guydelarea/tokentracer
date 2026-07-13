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
  err_type TEXT, err_msg TEXT
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
| `err_type`, `err_msg` | non-2xx body `error.type` / `error.message` |

Validation rules:
- `cache_w5m_tokens` / `cache_w1h_tokens` come from `message_start`'s cache_creation 5m/1h split.
- Usage columns are nullable — a client abort or upstream error records whatever facts arrived.
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
- **Relationship**: optional 1:1 with `requests`. `DELETE FROM captures` never touches `requests`; the inspector degrades to fact-row byte splits.

### schema_migrations

Applied-migration bookkeeping: version integer per applied entry from the ordered SQL slice in `store.go`.

## In-memory value types (Go)

### proxy.Exchange — proxy → sink handoff

`{Start, TTFT, Duration, Method, Path, Status, Streamed, ReqBody, RespBody}` — raw material; no parsing happened yet.

### anthropic.RequestFacts — ParseRequest output

Model, session id, stream flag, turns, tool count, tools/system/messages/total byte splits, label. Parsing rules the real data dictates: message/system `content` is string **or** block array; roles include `"system"` mid-conversation; `cache_control` may appear on system blocks and inside message blocks — never hard-code locations; never re-serialize the body.

### anthropic.Response — DecodeSSE / DecodeJSON output

Assembled message: blocks (text / thinking / tool_use with accumulated `input_json_delta`), model, stop_reason, merged usage. Non-2xx → `err_type` / `err_msg` extracted instead.

### anthropic.Breakdown — BreakdownRequest output (read-time, from request_gz)

`Tools []{Name, Bytes}` sorted desc; `System []{Bytes, CacheControl}`; `Messages []{Role, Bytes, BlockKinds}`; feature flags (`thinking` / `context_management` / `output_config`). Shares ParseRequest's marshaling rules so **section sums equal the fact-row byte columns** (tested). Not persisted — pure interpretation of the blob.

### billing.Rate / billing.Bill

`Rate{Key, InPerM, OutPerM, From, Until}` — ordered slice, substring match on normalized model (strip `anthropic.` prefix, `@date` suffix), first hit wins, `[From, Until)` windows. Multipliers: read 0.1, write-5m 1.25, write-1h 2.0.
`Bill{Priced bool; In, Read, Write, Out, Total float64}` — `Priced:false` for unknown models, propagated to UI (badge + `unpricedReqs`); never a silent $0.

## State transitions

A recorded exchange has one lifecycle: **arrived → forwarded → streamed/completed (or aborted/errored) → parsed by sink worker → inserted** (`requests` always; `captures` when there's a body to keep). Rows are immutable after insert; the only mutation in the system is manual capture deletion.
