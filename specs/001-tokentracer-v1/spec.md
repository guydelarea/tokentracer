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
│   ├── record/record.go            # Recorder: Exchange+Sink types, queue, worker, degradation ladder, one-tx insert
│   ├── anthropic/anthropic.go      # vendor module (one file per vendor): ParseRequest + BreakdownRequest + SSE decoder → assembled message + merged usage
│   ├── billing/billing.go          # time-keyed rate table, Compute(), unpriced handling
│   ├── store/store.go              # Open (pragmas, migrations), InsertExchange (one tx: facts + capture)
│   ├── store/queries.go            # read side: Lifetime/Window/Recent — targeted SELECTs, zero aggregation
│   └── api/                        # fold.go (pure stats fold → statsView), /api/stats, /api/capture, /dashboard, loopback middleware
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

Deleting capture rows never touches `requests` — tokentrace's fact/drill-down split, in relational form. No cost column anywhere. **Captures store bodies only, never headers** — the API key lives in `x-api-key`/`authorization`; keeping headers out of the DB is a stated invariant, not an accident of what we happen to store.

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

**`internal/proxy`** — catch-all `http.Handler`. Buffers the request body, forwards to `UPSTREAM` with hop-by-hop headers stripped and transport compression disabled, streams the response back with `http.Flusher.Flush()` per chunk while teeing into a buffer. Stamps t0 / TTFT (first body byte) / duration (EOF). Zero parsing on the client path. After stream completion hands `record.Exchange{Start, TTFT, Duration, Method, Path, Status, Streamed, ReqBody, RespBody, RespTruncated, ClientAborted}` to the `record.Sink` interface — satisfied by the Recorder in prod, a fake in proxy tests. **The proxy owns the record-or-not filter**: only `POST /v1/messages` is handed over (`count_tokens` and other paths: proxied, never recorded), so the Recorder may assume every Exchange it receives becomes a row.

Robustness rules on the proxy side:

- **Client abort drains upstream.** On client disconnect (esc in Claude Code — constant), detach from the client context and keep reading upstream to EOF with a 30s cap: Anthropic bills the tokens generated up to the cancel, and the final `message_delta` usage arrives after the client is gone. Record complete facts; if the cap hits, record what arrived. The hangup itself is a fact: `ClientAborted` → `aborted=1` — an esc'd generation is billed spend, invisible to every JSONL-reading tool.
- **Concurrency is the norm**, not the exception — Claude Code fires parallel requests routinely (subagents, background haiku calls). One goroutine per request; the Recorder's single worker serializes writes.
- **Tee buffer capped** (8 MB — far above any real response): on overflow keep the head and set `RespTruncated` (the Recorder stamps `err_type='oversize'`); facts that parsed survive.
- **Graceful shutdown**, in order: `Server.Shutdown` (stops accepting; in-flight streams and abort drains finish) → `recorder.Close()` (drains the queue unconditionally) → `store.Close()`. main owns the single shutdown deadline; a blown deadline is WAL-crash-equivalent, which SQLite survives. The tail of a session is where the interesting requests are.

**`internal/record`** — the Recorder: the deep module owning **"never lose an exchange"**. Interface `Record(Exchange)` + `Close()`; the `Exchange` and `Sink` types live here (proxy imports record). `record.New(st *store.Store)` — accepts the store, never creates or closes it (main owns the handle; the api read side shares it). `Record` **blocks** when the bounded queue (256, unexported constant) is full — backpressure over loss; it's called post-stream, so the client path never feels it. One worker goroutine parses via direct `anthropic.*` calls (no vendor interface until a second vendor exists) and inserts fact row + capture in **one transaction**. The **degradation ladder**, applied per side — a bad request body doesn't discard good response facts or vice versa; `err_msg` names the broken side ("request: …" / "response: …"):

1. parse failure → `err_type='parse'`, facts that need no parser survive (timing, status, byte sizes);
2. panic → `recover()`, `err_type='panic'`, worker continues;
3. truncated tee → `err_type='oversize'`;
4. **ladder bottom** — insert failure: retry once after a short delay, then log the row's facts to stderr and drop. Never crash (the proxy is live infrastructure mid-session), never retry forever (a wedged worker backs the queue up into `Record`).

Every rung keeps the capture — the `panic` blob *is* the repro case. **`err_type` precedence**: upstream `error.type` wins the column (facts outrank interpretations); recorder rungs appear only on otherwise-successful exchanges; a non-2xx whose error body won't parse gets `err_type='http_<status>'`. `Record` after `Close` is an ordering violation, prevented by main's shutdown order rather than defensive code.

**`internal/anthropic`** — pure functions, no I/O. `ParseRequest([]byte) (RequestFacts, error)`: model, session id, stream flag, turns, tool count, tools/system/messages/total byte splits, label — following the parsing rules validated above (string-or-array content, system-role messages, unknown fields untouched). `DecodeSSE([]byte) (Response, error)`: `message_start` seeds model + usage (input, cache_read, cache_creation 5m/1h split); `content_block_start/_delta/_stop` assemble text/thinking/tool_use blocks (accumulate `input_json_delta`); `message_delta` merges stop_reason + output_tokens (later values win per key). The assembled `Response` is complete (all block content + model + usage + stop_reason) per the lossiness lesson. `DecodeJSON` for non-streaming. Non-2xx: extract `error.type`/`error.message`. **Forward-compatibility rule** (mirror of the request side's unknown-fields rule): unknown SSE event types and unknown block types are skipped and decoding continues — the assembled `Response` keeps what it understood, never fails the whole decode. `BreakdownRequest([]byte) (Breakdown, error)`: the full-breakdown fold — `Tools []{Name, Bytes}` sorted desc, `System []{Bytes, CacheControl}`, `Messages []{Role, Bytes, BlockKinds}`, feature flags — sharing ParseRequest's marshaling rules so section sums equal the fact-row byte columns.

**`internal/billing`** — `Rate{Key, InPerM, OutPerM, From, Until, LongCtxThreshold, LongCtxInPerM, LongCtxOutPerM}` ordered slice; substring match on normalized model (strip `anthropic.` prefix, `@date` suffix), first hit wins, `[From, Until)` windows. `ReadMult=0.1`, `Write5mMult=1.25`, `Write1hMult=2.0`. **Long-context tier**: Anthropic charges premium rates above 200K input tokens on supporting models — Claude Code sessions live near the ceiling, so ignoring it skews exactly the expensive requests; when total input (input + cache reads + writes) exceeds the threshold, the long-context rates apply. `Compute(model, usage, at) Bill` where `Bill{Priced bool; In, Read, Write, Out, Total float64}`. **Unknown model → `Priced:false`**, propagated to the UI as a badge + `unpricedReqs` counter — never a silent $0. Seed the table **generated from LiteLLM's `model_prices_and_context_window.json`** (the community price registry ccusage uses — prices and model names churn; don't hand-type them), verified against Anthropic docs at implementation time (do NOT copy tokentrace's table — it holds fictional/aliased names).

**`internal/store`** — `Open(path)` (pragmas, migrations); write side `InsertExchange` (fact row + capture row in **one transaction** — never a capture without facts) called only from the Recorder's worker; read side is three targeted SELECTs with time filters only — `Lifetime()` (usage columns + model + ts of all rows), `Window(since)` (full rows, last hour), `Recent(n)` (request log) — **zero aggregation in SQL**: the long-context tier makes pricing non-additive (whether a request crossed 200K is a per-request fact that `SUM … GROUP BY model` destroys, and time-windowed rates price each row at its own `ts`), so every number is computed per row in the api fold.

**`internal/api` + `web/`** — routes `GET /dashboard`, `GET /web/*` (go:embed), `GET /api/stats`, `GET /api/capture?id=N` (gunzips to `{request, response, breakdown}` — request/response are the shape the old inspector consumed; `breakdown` is `anthropic.BreakdownRequest` folded server-side, keeping the numbers-in-Go invariant). All behind loopback middleware (non-loopback RemoteAddr → 404) *and* the listener binds `127.0.0.1` — defense in depth. `/api/stats` keeps old contract names where the view survives: `{port, upstream, traced, cost, recent[], overview{burnNow, burnAvg, reqHr, winReqs, avgReq, hitNow, hitAvg, peakMin, timeline[60]}, unpricedReqs}`; `recent[]` rows carry `id`, time, model, sid, op, status, ms, ttft, stop, token quartet, cost quartet, priced flag. Numbers are folded server-side in Go; the page only words and draws them (tokentrace's proven invariant) — implemented as one pure function in `internal/api/fold.go`: `fold(lifetime, window, recent, rates, now) statsView`. The `statsView` struct's json tags **are** the `/api/stats` contract (no translation layer — it would fail the deletion test); `now` is a parameter, so bucket-boundary tests are deterministic; per-row pricing, burn, hit rates, p50/p95 TTFT, peak minute, 60 buckets, and `unpricedReqs` all happen inside. The handler is query → fold → `json.Encode`. Known ceiling (`ponytail:` in code, not v1 work): lifetime is a full scan + reprice per 2s poll — milliseconds at v1 scale; the upgrade is caching running totals keyed by max rowid (rows are immutable/append-only; a rate-table change only happens at restart, which resets the cache).

**Frontend port**: extract from `/home/guy/tokentrace/dashboard.go`'s raw string into `web/index.html`, `web/app.css`, `web/app.js`; copy `/home/guy/tokentrace/design/logo.svg`. Keep: palette (`#2fbf87` cache-read, `#5aa2f7` input, `#d9a04e` write, `#ededed` output, `#ff5a5a` error), stat tiles, 60-min stacked spend timeline, `esc()` discipline, 2s poll of `/api/stats`. Replace sessions table with a flat request log (reuse the `tgrid` row style); keep inspector drawer with tabs request / response / billing / raw + a **breakdown** tab: the tools/system/history stacked bar (byte columns) topped with itemized tables from `/api/capture`'s `breakdown` — tools by schema size, system blocks with cache_control, per-message role/bytes/block kinds. If the capture row was deleted, the tab falls back to the stacked bar alone. Add latency tile (p50/p95 TTFT) and unpriced badge. Port mechanically first (the JS deliberately has no backticks — it compiled inside a Go raw string); modernize later.

## Milestones (each independently verifiable)

- **M0 — skeleton**: go.mod, env config, stub server on `127.0.0.1:PORT`. Verify: `CGO_ENABLED=0 go build ./...`.
- **M1 — transparent proxy**: forwarding + per-chunk flush + latency stamps, no capture. Verify: `ANTHROPIC_BASE_URL=http://localhost:8787 claude` streams normally; unit test proves non-buffering (fake upstream sends chunk 1, waits for client to observe it, then chunk 2).
- **M2 — parse + capture + DB**: `anthropic` package (fixture tests), `store` schema, `record` Recorder (degradation-ladder tests), gz captures. Verify: run a Claude Code turn, `sqlite3 tokentracer.db 'select model_req, session_id, tool_count, input_tokens, ttft_ms from requests'`; delete captures row, fact survives; two overlapping streams both land intact.
- **M3 — billing**: rate table + `Compute` + unpriced propagation. Verify: table tests incl. time-windowed rates and unknown model → `Priced:false`.
- **M4 — dashboard**: `/api/stats` fold, frontend port, inspector via `/api/capture`. Verify: live browser during a Claude Code session — tiles move, rows appear ≤2s, inspector shows all tabs incl. the full breakdown (tools sorted by size, system blocks, messages).
- **M5 — hardening + README**: error-body capture, client-abort upstream drain, graceful-shutdown flush, tee-buffer cap, quickstart README with logo, two-command setup, and the capture disk math (roughly 10–60 KB gzipped per request — a heavy day is hundreds of MB; retention is manual `DELETE FROM captures` in v1, stated as a documented ceiling, not a surprise).

## Testing strategy

- **Fixture tests** (`testdata/anthropic_capture.json.gz` — real capture: claude-sonnet-5, 119 tools, 3 system blocks, 7 messages, session id present, stop_reason tool_use): table-driven asserts on `ParseRequest` pinning every value in the extraction table above, incl. the string-vs-array content forms and system-role messages. Same fixture drives `BreakdownRequest`: pins per-tool sizes (Workflow ≈21.5 KB largest, list_projects 149 B smallest, 119 total), 3 system blocks with their `cache_control`, 7 message entries with roles/block kinds, and that section sums equal `tools_bytes`/`system_bytes`/`messages_bytes`.
- **SSE replay** (`testdata/replay.sse`, built once from the fixture's decoded blocks with synthesized usage events and thinking text — the old sidecar stored neither): unit-test `DecodeSSE` — structural comparison on fields the fixture retained (tool_use id/name/input, stop_reason), plus usage merge and full-content assembly.
- **Fake upstream**: `httptest.Server` replaying `replay.sse` as `text/event-stream` with per-event flush. E2E test: temp DB → proxy → POST fixture request → assert client got byte-identical stream, fact row has merged usage + `ttft_ms > 0` + session id, `/api/stats` prices it, `count_tokens` leaves no row.
- **Adversarial decoder inputs** (`anthropic` package): truncated SSE mid-event; `error` event after partial content; synthetic unknown event type and unknown block type (decode keeps what it understood).
- **Recorder tests** (all through `Record(Exchange)` against a real temp store — the interface is the test surface, `modernc.org/sqlite` makes the real store the cheapest fake): non-JSON request + good response → usage facts survive, `err_type='parse'`, `err_msg` "request: …", capture kept; good request + broken response → request facts survive, "response: …"; injected parse panic via the internal seam (unexported `var parseRequest`, swapped only in tests) → `'panic'`, worker continues, next Record lands; `RespTruncated` → `'oversize'` with head capture; `ClientAborted` → `aborted=1` on an otherwise-normal row; 529 with malformed error body → `'http_529'` (upstream-wins precedence); N queued + `Close()` → N rows (drain); store on a read-only DB file → no panic, zero rows, loud stderr. Deliberately untested: Record-blocks-when-full (one stdlib channel op — all flake, no information); `count_tokens`-never-recorded (that's the proxy's filter — proxy/E2E territory).
- **Proxy/E2E adversarial**: client abort mid-stream (upstream drained, usage complete, `aborted=1`); two concurrent streams through the fake upstream (both rows intact).
- **Billing/store/api**: pure table tests incl. the long-context tier boundary. The fold gets deterministic table tests (rows + rates + fixed `now` in, struct fields out — no DB, no HTTP, no clock); one contract test marshals a `statsView` and pins the JSON keys against `contracts/http-api.md`; one HTTP smoke test (real store, real handler, 200 + valid JSON) replaces the old seeded-store suite.
- **Loopback**: spoofed non-loopback RemoteAddr → 404.

## End-to-end verification

1. `go run ./cmd/tokentracer` (defaults: 8787, `https://api.anthropic.com`, `./tokentracer.db`).
2. `ANTHROPIC_BASE_URL=http://localhost:8787 claude` — short task that uses a tool.
3. Confirm: streaming feels normal; dashboard rows live; token counts match inspector raw response usage; cost nonzero and plausible; `ttft_ms < duration_ms`; `count_tokens` produces no row; `/api/stats` unreachable from another machine.
4. Ctrl-C mid-session: queued rows are flushed, DB closes cleanly, no missing tail.

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
