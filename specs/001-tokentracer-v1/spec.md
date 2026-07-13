# TokenTracer v1 — Plan

## Context

TokenTracer is a local proxy that sits between an AI coding tool and its API, tracks what each request actually cost, breaks each request into its core values, and shows where money leaks. Guy already built this once: `/home/guy/tokentrace` (~5.8k lines Go, zero-dep, single package). That project began as a Go port of Matt Pocock's `proxy.mjs` and got married to two concepts Guy now doubts: **zero-deps/one-package purism** (dashboard as a raw-string HTML literal with no backticks allowed in JS, panic-based selftest instead of `go test`) and the **storage model** (append-only JSONL + gz sidecar files + in-memory ring replayed on boot).

TokenTracer is a **clean-room rewrite** in `/home/guy/tokentracer`. The only things reused from tokentrace are the **logo, visual design, and frontend** (`design/logo.svg`, the dashboard HTML/CSS/JS embedded in `dashboard.go`, contract documented in `docs/frontend-data.md`). All Go code is written fresh. Proven *concepts* are kept: facts-not-interpretations (usage stored verbatim, cost computed at read time), proxy path never blocks the client, dashboard loopback-only.

**Product pillars** (user-stated): 1) Understand LLM usage (costs, tokens), 2) Track LLM usage, 3) Improve LLM usage. v1 delivers Track + Understand — Understand includes a **full per-request breakdown** of the API request (every tool schema, system block, and message itemized and sized in the inspector); Improve is deferred (mechanism undecided) but v1 records the facts that make future recommendations possible (latency, tool bytes, called tools, cache behavior). tokentrace's ranked "Do next" insights (`trace.go: insightsOf`) are prior art for v2.

**User decisions made during brainstorm:**
- Go, proper multi-package layout, real `_test.go` tests (stdlib `testing`), dependencies where they pay.
- **SQLite only** via `modernc.org/sqlite` (pure Go, `CGO_ENABLED=0` static binary). One `tokentracer.db` is the single source of truth. No in-memory ring, no boot replay — API endpoints are SQL queries.
- **v1 narrow**: Anthropic Messages API only (`/v1/messages`, SSE + non-streaming), i.e. Claude Code via `ANTHROPIC_BASE_URL=http://localhost:8787`. OpenAI formats, Codex/Cursor, Trace/Sessions/insights views deferred.
- Fix known footguns: unknown model must NOT silently price at $0 (surface as unpriced); record per-request latency (duration, TTFT) from day one.

**Decisions made closing the Plan agent's open questions:** store the *assembled* response message in the capture blob (matches the reused inspector frontend and the existing fixture format, ~3x smaller than raw SSE); keep default port 8787 (TokenTracer replaces tokentrace); DB defaults to cwd; capture retention is manual (`DELETE FROM captures`); fourth overview tile shows latency (p50/p95 TTFT).

## Repo layout

```
tokentracer/
├── go.mod                          # module github.com/guydelarea/tokentracer; require modernc.org/sqlite
├── cmd/tokentracer/main.go         # env config, wiring, ListenAndServe on 127.0.0.1:PORT
├── internal/
│   ├── proxy/proxy.go              # catch-all reverse handler, streaming tee, latency stamps
│   ├── anthropic/anthropic.go      # vendor module (one file per vendor): ParseRequest + BreakdownRequest + SSE decoder → assembled message + merged usage
│   ├── billing/billing.go          # time-keyed rate table, Compute(), unpriced handling
│   ├── store/store.go              # Open (pragmas, migrations), InsertRequest, InsertCapture
│   ├── store/queries.go            # read side: recent rows, timeline buckets, lifetime totals
│   └── api/                        # /api/stats, /api/capture, /dashboard, loopback middleware
├── web/                            # embed.go (go:embed) + index.html, app.css, app.js, logo.svg
└── testdata/
    ├── anthropic_capture.json.gz   # the existing fixture, moved from repo root
    └── replay.sse                  # SSE stream synthesized from the fixture
```

## SQLite schema

Open with `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`, `foreign_keys=ON`. Versioned migrations = `schema_migrations` table + ordered SQL slices in `store.go` (no migration dep).

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

CREATE TABLE captures (
  request_id  INTEGER PRIMARY KEY REFERENCES requests(id),
  request_gz  BLOB NOT NULL,                  -- verbatim request body, gzipped
  response_gz BLOB                            -- assembled response message JSON, gzipped
);
```

Deleting capture rows never touches `requests` — tokentrace's fact/drill-down split, in relational form. No cost column anywhere.

**Session extraction** (Claude Code packs JSON into the `metadata.user_id` string): regex `"session_id"\s*:\s*"([^"]+)"` applied to that string; no match → NULL.

## Data extraction & analysis — validated against the real capture

The fixture (`2026-07-10T10-00-39-308_anthropic_35.json.gz`, a real Claude Code request) was dissected field-by-field to validate the model. Every fact column maps to real data:

| Column | JSON path | Real value in fixture |
|---|---|---|
| `model_req` | `request.model` | `claude-sonnet-5` |
| `session_id` | `request.metadata.user_id` → regex | `210b4cd3-be09-484a-a703-d00f5c6e855f` |
| `streamed` | `request.stream` | `true` |
| `turns` | `len(request.messages)` | 7 |
| `tool_count` | `len(request.tools)` | 119 |
| `total_bytes` | marshaled request | 311,871 |
| `tools_bytes` | marshaled `request.tools` | 236,217 (**75%**) |
| `system_bytes` | marshaled `request.system` | 29,469 (9%) |
| `messages_bytes` | marshaled `request.messages` | 45,459 (14%) |
| `label` | first **user-role** text block | first user message text, ≤64 chars |
| `stop_reason` | response | `tool_use` |
| `op` | response blocks | `tool_use · DesignSync` |
| usage columns | SSE `message_start` + `message_delta` | absent from this sidecar — see lossiness lesson |

**Parsing rules the real data dictates** (each becomes a fixture test):
- Message `content` arrives as **both** a raw string and a block array — `[1]`, `[3]`, `[4]` are strings; `[0]`, `[2]`, `[5]`, `[6]` are arrays. Handle both; same for `system` (string or block array).
- Messages include **role `"system"`** mid-conversation (Claude Code system-reminders), not just user/assistant. Never assume the role set; `label` = first *user-role* text, skipping system entries.
- `cache_control` appears in multiple places: system blocks (`{type: ephemeral, ttl: "1h"}`) and on a `tool_result` block inside messages. Tools carry none here (they're cached as part of the prefix). Don't hard-code breakpoint locations.
- Unknown request fields must round-trip unharmed: this one carries `thinking: {type: adaptive}`, `context_management.edits` (clear_thinking), `output_config.effort`, and a non-cached `x-anthropic-billing-header` system block. ParseRequest reads what it knows and never re-serializes the body (capture stores verbatim bytes).
- Tool schema sizes are wildly skewed (Workflow 21.5 KB → list_projects 149 B); per-tool sizes are derivable from the capture blob at read time — no fact column needed.

**Lossiness lesson (design-changing).** tokentrace's decoded sidecar dropped the thinking text (`{type: thinking}` only), stored tool_use input as a `text` string, and kept **no usage and no served model**. TokenTracer's `response_gz` therefore stores the **full assembled message**: every block's complete content (thinking text + signature, tool_use `id`/`name`/`input` as JSON, text), plus `model`, `stop_reason`, `usage` — so the blob alone can answer any future question, and the fact row stays the queryable subset. (Consequence for tests: the old fixture can't serve as byte-exact response ground truth — compare structurally on the fields it retained.)

**Analysis queries that power Understand** (the `/api/stats` fold is these, priced per row via `billing.Compute`):
- Spend timeline: `SELECT ts_ms/60000 AS minute, <usage cols> FROM requests WHERE ts_ms > ?` → price per row, stack by class (input/cacheRead/cacheWrite/output).
- Cache hit rate: `sum(cache_read_tokens) / sum(input_tokens + cache_read_tokens + cache_w5m + cache_w1h)`.
- Composition (the 75%-tools story): stacked `tools_bytes / system_bytes / messages_bytes` per request — the fixture alone shows tool schemas dominate the wire; this is the v1 seed of the Improve pillar.
- **Full request breakdown** (per request, inspector): derived at read time from `request_gz` — every tool schema named and sized (sorted desc; fixture: Workflow 21.5 KB → list_projects 149 B), every system block sized with its `cache_control`, every message itemized (index, role, bytes, block kinds — text/tool_use/tool_result/thinking), plus flags for `thinking`/`context_management`/`output_config`. No new fact columns (facts stay facts; breakdown is interpretation of the verbatim blob). Deleting the capture row degrades the inspector to the fact-row byte splits — the breakdown lives and dies with the blob.
- Latency: `ttft_ms` / `duration_ms` percentiles per model over a time window.

## Components

**`internal/proxy`** — catch-all `http.Handler`. Buffers the request body, forwards to `UPSTREAM` with hop-by-hop headers stripped and transport compression disabled, streams the response back with `http.Flusher.Flush()` per chunk while teeing into a buffer. Stamps t0 / TTFT (first body byte) / duration (EOF). Zero parsing on the client path. After stream completion hands `Exchange{Start, TTFT, Duration, Method, Path, Status, Streamed, ReqBody, RespBody}` to a `Sink` interface; a single worker goroutine does parse+insert (also serializes SQLite writes). `POST /v1/messages/count_tokens` and non-`/v1/messages` paths: proxied, never recorded. Client abort: keep captured bytes, record facts that arrived.

**`internal/anthropic`** — pure functions, no I/O. `ParseRequest([]byte) (RequestFacts, error)`: model, session id, stream flag, turns, tool count, tools/system/messages/total byte splits, label — following the parsing rules validated above (string-or-array content, system-role messages, unknown fields untouched). `DecodeSSE([]byte) (Response, error)`: `message_start` seeds model + usage (input, cache_read, cache_creation 5m/1h split); `content_block_start/_delta/_stop` assemble text/thinking/tool_use blocks (accumulate `input_json_delta`); `message_delta` merges stop_reason + output_tokens (later values win per key). The assembled `Response` is complete (all block content + model + usage + stop_reason) per the lossiness lesson. `DecodeJSON` for non-streaming. Non-2xx: extract `error.type`/`error.message`. `BreakdownRequest([]byte) (Breakdown, error)`: the full-breakdown fold — `Tools []{Name, Bytes}` sorted desc, `System []{Bytes, CacheControl}`, `Messages []{Role, Bytes, BlockKinds}`, feature flags — sharing ParseRequest's marshaling rules so section sums equal the fact-row byte columns.

**`internal/billing`** — `Rate{Key, InPerM, OutPerM, From, Until}` ordered slice; substring match on normalized model (strip `anthropic.` prefix, `@date` suffix), first hit wins, `[From, Until)` windows. `ReadMult=0.1`, `Write5mMult=1.25`, `Write1hMult=2.0`. `Compute(model, usage, at) Bill` where `Bill{Priced bool; In, Read, Write, Out, Total float64}`. **Unknown model → `Priced:false`**, propagated to the UI as a badge + `unpricedReqs` counter — never a silent $0. Seed with current Anthropic list prices verified against docs at implementation time (do NOT copy tokentrace's table — it holds fictional/aliased names).

**`internal/store`** — `Open(path)` (pragmas, migrations); write side `InsertRequest`/`InsertCapture` called only from the sink worker; read side returns Go structs, priced per row by `billing.Compute` in the API layer.

**`internal/api` + `web/`** — routes `GET /dashboard`, `GET /web/*` (go:embed), `GET /api/stats`, `GET /api/capture?id=N` (gunzips to `{request, response, breakdown}` — request/response are the shape the old inspector consumed; `breakdown` is `anthropic.BreakdownRequest` folded server-side, keeping the numbers-in-Go invariant). All behind loopback middleware (non-loopback RemoteAddr → 404) *and* the listener binds `127.0.0.1` — defense in depth. `/api/stats` keeps old contract names where the view survives: `{port, upstream, traced, cost, recent[], overview{burnNow, burnAvg, reqHr, winReqs, avgReq, hitNow, hitAvg, peakMin, timeline[60]}, unpricedReqs}`; `recent[]` rows carry `id`, time, model, sid, op, status, ms, ttft, stop, token quartet, cost quartet, priced flag. Numbers are folded server-side in Go; the page only words and draws them (tokentrace's proven invariant).

**Frontend port**: extract from `/home/guy/tokentrace/dashboard.go`'s raw string into `web/index.html`, `web/app.css`, `web/app.js`; copy `/home/guy/tokentrace/design/logo.svg`. Keep: palette (`#2fbf87` cache-read, `#5aa2f7` input, `#d9a04e` write, `#ededed` output, `#ff5a5a` error), stat tiles, 60-min stacked spend timeline, `esc()` discipline, 2s poll of `/api/stats`. Replace sessions table with a flat request log (reuse the `tgrid` row style); keep inspector drawer with tabs request / response / billing / raw + a **breakdown** tab: the tools/system/history stacked bar (byte columns) topped with itemized tables from `/api/capture`'s `breakdown` — tools by schema size, system blocks with cache_control, per-message role/bytes/block kinds. If the capture row was deleted, the tab falls back to the stacked bar alone. Add latency tile (p50/p95 TTFT) and unpriced badge. Port mechanically first (the JS deliberately has no backticks — it compiled inside a Go raw string); modernize later.

## Milestones (each independently verifiable)

- **M0 — skeleton**: go.mod, env config, stub server on `127.0.0.1:PORT`. Verify: `CGO_ENABLED=0 go build ./...`.
- **M1 — transparent proxy**: forwarding + per-chunk flush + latency stamps, no capture. Verify: `ANTHROPIC_BASE_URL=http://localhost:8787 claude` streams normally; unit test proves non-buffering (fake upstream sends chunk 1, waits for client to observe it, then chunk 2).
- **M2 — parse + capture + DB**: `anthropic` package (fixture tests), `store` schema, sink worker, gz captures. Verify: run a Claude Code turn, `sqlite3 tokentracer.db 'select model_req, session_id, tool_count, input_tokens, ttft_ms from requests'`; delete captures row, fact survives.
- **M3 — billing**: rate table + `Compute` + unpriced propagation. Verify: table tests incl. time-windowed rates and unknown model → `Priced:false`.
- **M4 — dashboard**: `/api/stats` fold, frontend port, inspector via `/api/capture`. Verify: live browser during a Claude Code session — tiles move, rows appear ≤2s, inspector shows all tabs incl. the full breakdown (tools sorted by size, system blocks, messages).
- **M5 — hardening + README**: error-body capture, client-abort behavior, quickstart README with logo and two-command setup.

## Testing strategy

- **Fixture tests** (`testdata/anthropic_capture.json.gz` — real capture: claude-sonnet-5, 119 tools, 3 system blocks, 7 messages, session id present, stop_reason tool_use): table-driven asserts on `ParseRequest` pinning every value in the extraction table above, incl. the string-vs-array content forms and system-role messages. Same fixture drives `BreakdownRequest`: pins per-tool sizes (Workflow ≈21.5 KB largest, list_projects 149 B smallest, 119 total), 3 system blocks with their `cache_control`, 7 message entries with roles/block kinds, and that section sums equal `tools_bytes`/`system_bytes`/`messages_bytes`.
- **SSE replay** (`testdata/replay.sse`, built once from the fixture's decoded blocks with synthesized usage events and thinking text — the old sidecar stored neither): unit-test `DecodeSSE` — structural comparison on fields the fixture retained (tool_use id/name/input, stop_reason), plus usage merge and full-content assembly.
- **Fake upstream**: `httptest.Server` replaying `replay.sse` as `text/event-stream` with per-event flush. E2E test: temp DB → proxy → POST fixture request → assert client got byte-identical stream, fact row has merged usage + `ttft_ms > 0` + session id, `/api/stats` prices it, `count_tokens` leaves no row.
- **Billing/store/api**: pure table tests; API tests seed the store and pin the JSON numbers the page renders.
- **Loopback**: spoofed non-loopback RemoteAddr → 404.

## End-to-end verification

1. `go run ./cmd/tokentracer` (defaults: 8787, `https://api.anthropic.com`, `./tokentracer.db`).
2. `ANTHROPIC_BASE_URL=http://localhost:8787 claude` — short task that uses a tool.
3. Confirm: streaming feels normal; dashboard rows live; token counts match inspector raw response usage; cost nonzero and plausible; `ttft_ms < duration_ms`; `count_tokens` produces no row; `/api/stats` unreachable from another machine.

## Config

| Env | Default | Meaning |
|---|---|---|
| `PORT` | `8787` | loopback listen port |
| `UPSTREAM` | `https://api.anthropic.com` | upstream base URL |
| `TOKENTRACER_DB` | `./tokentracer.db` | SQLite path |

Env vars only; no `.env`, no setup wizard in v1.

## Deferred (explicitly, for v2+)

OpenAI Chat Completions + Responses formats (Codex/Cursor/OpenCode); Sessions and Trace views; the **Improve** pillar — ranked money-leak recommendations (tokentrace's `insightsOf` kinds: unused tool schemas, exploration re-reads, thinking share, truncations, cache breaks, compaction candidates — are the prior art); secret redaction of capture blobs; capture auto-pruning.

## Reference files

- `/home/guy/tokentrace/dashboard.go` — frontend source to extract; `/api/stats` field names
- `/home/guy/tokentrace/docs/frontend-data.md` — data contract + capture-gap lessons (promote `stop_reason` + block shape to fact row)
- `/home/guy/tokentracer/2026-07-10T10-00-39-308_anthropic_35.json.gz` — test fixture → `testdata/`
- `/home/guy/tokentrace/design/logo.svg` — reused asset → `web/logo.svg`
- `/home/guy/tokentrace/README.md` — setup UX to preserve
