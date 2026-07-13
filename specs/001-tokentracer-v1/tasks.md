# Tasks: TokenTracer v1

**Input**: Design documents from `/specs/001-tokentracer-v1/`
**Prerequisites**: plan.md, spec.md, data-model.md, contracts/http-api.md, contracts/config.md, research.md, quickstart.md

**Tests**: INCLUDED — the spec's testing strategy explicitly requires them ("real `_test.go` tests", fixture tests, adversarial inputs). Write each story's tests first and watch them fail before implementing.

**Organization**: The spec's milestones M1–M4 are the user stories (each is already defined as independently verifiable); M0 is Setup, M5 is Polish.

| Story | Milestone | Delivers |
|---|---|---|
| US1 (P1) | M1 | Transparent proxy — Claude Code streams through unchanged |
| US2 (P2) | M2 | Every exchange recorded — facts + captures in SQLite |
| US3 (P3) | M3 | Usage priced at read time — never a silent $0 |
| US4 (P4) | M4 | Dashboard — live spend, latency, full request breakdown |

## Format: `[ID] [P?] [Story] Description`

---

## Phase 1: Setup (M0 — skeleton)

**Purpose**: Buildable module, env config, stub server. Verify: `CGO_ENABLED=0 go build ./...`

- [X] T001 Initialize `go.mod` (module `github.com/guydelarea/tokentracer`, latest stable Go) and `go get modernc.org/sqlite` — the only external dependency
- [X] T002 Create `cmd/tokentracer/main.go`: env config per `contracts/config.md` (`PORT`=8787, `UPSTREAM`=https://api.anthropic.com, `TOKENTRACER_DB`=./tokentracer.db), stub `http.Server` bound to `127.0.0.1:PORT`
- [X] T003 [P] Move fixture `2026-07-10T10-00-39-308_anthropic_35.json.gz` → `testdata/anthropic_capture.json.gz`
- [X] T004 [P] Copy `/home/guy/tokentrace/design/logo.svg` → `web/logo.svg`

**Checkpoint**: `CGO_ENABLED=0 go build ./...` succeeds.

---

## Phase 2: Foundational (blocking prerequisites)

**Purpose**: The handoff vocabulary and the shared test stream — every story touches one or both.

- [X] T005 Create `internal/record/record.go` with the **types only**: `Exchange{Start, TTFT, Duration, Method, Path, Status, Streamed, ReqBody, RespBody, RespTruncated, ClientAborted}` and `Sink interface { Record(Exchange) }` (Recorder implementation comes in US2; proxy imports these in US1)
- [X] T006 Synthesize `testdata/replay.sse` from the fixture's decoded blocks: `message_start` (model + usage incl. cache_creation 5m/1h split), `content_block_start/_delta/_stop` for text/thinking/tool_use (with `input_json_delta` accumulation), `message_delta` (stop_reason + output_tokens) — the old sidecar stored no usage and no thinking text, so synthesize both; used by proxy tests, decoder tests, and E2E

**Checkpoint**: `go vet ./...` clean — user stories can begin.

---

## Phase 3: User Story 1 — Transparent proxy (P1, M1) 🎯 MVP

**Goal**: `ANTHROPIC_BASE_URL=http://localhost:8787 claude` works with zero perceptible change; latency stamped; recordable Exchanges handed to a Sink.

**Independent Test**: run Claude Code through it with a no-op Sink — streaming feels normal; the non-buffering unit test proves per-chunk delivery.

### Tests for User Story 1 ⚠️ write first, watch them fail

- [X] T007 [US1] Write `internal/proxy/proxy_test.go` against an `httptest.Server` fake upstream and a fake `record.Sink` (appends to slice): **non-buffering** (upstream sends chunk 1, blocks until the client observes it, then chunk 2); **filter** (`POST /v1/messages` → one Exchange handed over; `POST /v1/messages/count_tokens` and `GET /other` → proxied, zero Exchanges); **abort drain** (client disconnects mid-stream → upstream read to EOF, Exchange has `ClientAborted=true` and the complete body); **tee cap** (response > cap → `RespTruncated=true`, head kept, client still got full stream); **concurrency** (two overlapping streams → two intact Exchanges); **hop-by-hop headers stripped**

### Implementation for User Story 1

- [X] T008 [US1] Implement `internal/proxy/proxy.go`: catch-all `http.Handler` — buffer request body; forward to `UPSTREAM` with hop-by-hop headers stripped and transport compression disabled; stream back with per-chunk `http.Flusher.Flush()` while teeing (8 MB cap → `RespTruncated`); stamp t0 / TTFT (first upstream body byte) / duration (EOF); on client abort detach from client context and drain upstream to EOF with 30s cap → `ClientAborted`; zero parsing on the client path; post-stream, hand `record.Exchange` to `record.Sink` for `POST /v1/messages` only
- [X] T009 [US1] Wire proxy into `cmd/tokentracer/main.go` with a no-op Sink; verify live: `ANTHROPIC_BASE_URL=http://localhost:8787 claude` on a short tool-using task — streaming feels normal

**Checkpoint**: MVP — TokenTracer is a working transparent proxy.

---

## Phase 4: User Story 2 — Record every exchange (P2, M2)

**Goal**: Facts + captures land in `tokentracer.db`; the Recorder never loses an exchange it saw.

**Independent Test**: feed Exchanges (including hostile ones) to `Record()` against a temp store — rows verifiable via `sqlite3`; no proxy needed.

### Tests for User Story 2 ⚠️ write first, watch them fail

- [X] T010 [P] [US2] Write `internal/anthropic/anthropic_test.go` fixture tests: `ParseRequest` pins the extraction table (model `claude-sonnet-5`, session id via regex on `metadata.user_id`, streamed, 7 turns, 119 tools, bytes 311871 total / 236217 tools / 29469 system / 45459 messages, label = first **user-role** text ≤64 chars skipping system-role messages, string-vs-array content both handled); `BreakdownRequest` pins per-tool sizes (Workflow ≈21.5 KB max, list_projects 149 B min, 119 entries sorted desc), 3 system blocks with `cache_control`, 7 messages with roles/block kinds, **section sums == fact byte columns**; `DecodeSSE` on `testdata/replay.sse` (usage merge later-wins, assembled thinking + tool_use input, stop_reason); adversarial decoder inputs: truncated SSE mid-event, `error` event after partial content, unknown event type + unknown block type skipped (decode keeps what it understood); non-2xx `error.type`/`error.message` extraction
- [X] T011 [P] [US2] Write `internal/store/store_test.go`: `Open` applies pragmas (WAL, `synchronous=NORMAL`, `busy_timeout=5000`, `foreign_keys=ON`) and migrations idempotently; `InsertExchange` writes fact row + capture in **one transaction**; `DELETE FROM captures` leaves fact rows intact

### Implementation for User Story 2

- [X] T012 [US2] Implement `internal/anthropic/anthropic.go`: `ParseRequest`, `BreakdownRequest` (shared marshaling rules so section sums equal fact byte columns), `DecodeSSE`, `DecodeJSON`, non-2xx error extraction — pure functions, no I/O, unknown fields/events/blocks skipped never fatal
- [X] T013 [US2] Implement `internal/store/store.go`: `Open(path)` (pragmas, `schema_migrations` + ordered SQL slices; `requests` incl. `aborted` column + `captures` per data-model.md), `InsertExchange` (one tx, gzipped blobs, bodies only — never headers)
- [X] T014 [US2] Write `internal/record/record_test.go` — all through `Record(Exchange)` + a real temp store: happy path (fixture request + replay.sse → every fact column + capture); bad request + good response (usage facts survive, `err_type='parse'`, `err_msg` "request: …", capture kept); good request + broken response ("response: …"); injected parse panic via internal seam → `'panic'`, worker continues, next Record lands; `RespTruncated` → `'oversize'` with head capture; `ClientAborted` → `aborted=1` on otherwise-normal row; 529 with malformed error body → `'http_529'` (upstream-wins precedence); N queued + `Close()` → N rows; store on read-only DB file → no panic, zero rows, loud stderr
- [X] T015 [US2] Implement the Recorder in `internal/record/record.go`: `New(st *store.Store)`, bounded queue (256, unexported const), blocking `Record`, single worker, per-side degradation ladder with `err_type` precedence, retry-once-then-stderr-drop ladder bottom, capture kept on every rung, `Close()` drains unconditionally, unexported `var parseRequest = anthropic.ParseRequest` internal seam (test-only swap)
- [X] T016 [US2] Wire into `cmd/tokentracer/main.go`: `store.Open` → `record.New` → proxy Sink = Recorder; SIGINT/SIGTERM shutdown order `Server.Shutdown(ctx)` → `recorder.Close()` → `store.Close()` with one deadline in main
- [X] T017 [US2] Write E2E test `cmd/tokentracer/e2e_test.go`: temp DB → proxy → POST fixture request against fake upstream replaying `replay.sse` → client got byte-identical stream; fact row has merged usage, `ttft_ms > 0`, session id; `count_tokens` leaves no row; two overlapping streams both land

**Checkpoint**: real Claude Code turn lands — `sqlite3 tokentracer.db 'select model_req, session_id, tool_count, input_tokens, ttft_ms from requests'`; delete captures row, facts survive.

---

## Phase 5: User Story 3 — Price usage at read time (P3, M3)

**Goal**: `billing.Compute` prices any usage against a time-keyed rate table; unknown model surfaces as unpriced, never $0.

**Independent Test**: pure table tests — no proxy, no store.

### Tests for User Story 3 ⚠️ write first, watch them fail

- [X] T018 [P] [US3] Write `internal/billing/billing_test.go`: model normalization (strip `anthropic.` prefix, `@date` suffix), substring match first-hit-wins, `[From, Until)` windows, multipliers (read 0.1, write-5m 1.25, write-1h 2.0), long-context tier boundary (at and above threshold — total input = input + cache reads + writes), unknown model → `Priced:false`

### Implementation for User Story 3

- [X] T019 [P] [US3] Generate the seed rate table into `internal/billing/rates.go` from LiteLLM's `model_prices_and_context_window.json`, verified against Anthropic's current price list — generated, not hand-typed; do NOT copy tokentrace's table (fictional/aliased names)
- [X] T020 [US3] Implement `internal/billing/billing.go`: `Rate{Key, InPerM, OutPerM, From, Until, LongCtxThreshold, LongCtxInPerM, LongCtxOutPerM}`, `Compute(model, usage, at) Bill{Priced, In, Read, Write, Out, Total}`

**Checkpoint**: table tests green, incl. time-windowed rates and unpriced propagation.

---

## Phase 6: User Story 4 — Dashboard (P4, M4)

**Goal**: loopback-only dashboard: tiles, 60-min stacked timeline, request log, inspector with full breakdown.

**Independent Test**: fold table tests with seeded rows + fixed `now`; then live browser during a Claude Code session.

### Tests for User Story 4 ⚠️ write first, watch them fail

- [X] T021 [US4] Write `internal/api/fold_test.go`: deterministic table tests (rows + rates + fixed `now` in, struct fields out) for burn now/avg, cache hit rates, p50/p95 TTFT, peak minute, 60 minute-buckets stacked by class, `unpricedReqs`; contract test marshals a `statsView` and pins the JSON keys against `contracts/http-api.md`
- [X] T022 [P] [US4] Write `internal/api/api_test.go`: loopback middleware (spoofed non-loopback `RemoteAddr` → 404); HTTP smoke (real temp store, `GET /api/stats` → 200 + valid JSON); `GET /api/capture` with unknown/deleted id → 404

### Implementation for User Story 4

- [X] T023 [P] [US4] Implement `internal/store/queries.go`: `Lifetime()` (usage cols + model + ts, all rows), `Window(since)` (full rows), `Recent(n)` — targeted SELECTs, zero aggregation (long-context tier makes pricing non-additive)
- [X] T024 [US4] Implement `internal/api/fold.go`: `statsView` (json tags ARE the `/api/stats` contract) + pure `fold(lifetime, window, recent, rates, now)` — all pricing and aggregation per row, `now` as parameter; `ponytail:` note on the full-scan ceiling and max-rowid cache upgrade path
- [X] T025 [US4] Implement `internal/api/api.go`: routes `GET /dashboard`, `GET /web/*` (go:embed), `GET /api/stats` (query → fold → encode), `GET /api/capture?id=N` (gunzip → `{request, response, breakdown}` with `breakdown` = `anthropic.BreakdownRequest` folded server-side; deleted → 404), loopback middleware on dashboard/API routes
- [X] T026 [US4] Port frontend: extract from `/home/guy/tokentrace/dashboard.go` raw string → `web/index.html`, `web/app.css`, `web/app.js` + `web/embed.go` (go:embed). Keep: palette (`#2fbf87` cache-read, `#5aa2f7` input, `#d9a04e` write, `#ededed` output, `#ff5a5a` error), stat tiles, 60-min stacked timeline, `esc()` discipline, 2s poll. Replace sessions table with flat request log (tgrid row style). Inspector drawer tabs: request / response / billing / raw + **breakdown** (stacked tools/system/history bar + itemized tables from `/api/capture`; falls back to fact-row bar if capture deleted). Add latency tile (p50/p95 TTFT) + unpriced badge. Port mechanically — the JS deliberately has no backticks; modernize later
- [X] T027 [US4] Wire api + web into `cmd/tokentracer/main.go`; extend `cmd/tokentracer/e2e_test.go`: `/api/stats` prices the recorded exchange (nonzero cost, `priced:true`)

**Checkpoint**: live browser during a Claude Code session — tiles move, rows appear ≤2s, inspector shows all tabs incl. full breakdown sorted by size.

---

## Phase 7: Polish & cross-cutting (M5)

- [X] T028 [P] Write `README.md`: logo, two-command quickstart (per quickstart.md), cost framed as "estimated API-equivalent cost" (subscription users don't pay per token), capture disk math (~10–60 KB gzipped per request; heavy days add up; retention is manual `DELETE FROM captures` in v1)
- [X] T029 Verify shutdown E2E: Ctrl-C mid-session → queued rows flushed, DB closes cleanly, no missing tail (quickstart step 4)
- [X] T030 Run the full quickstart.md acceptance checklist (streaming normal; live rows; tokens match raw usage; cost plausible; `ttft_ms < duration_ms`; `count_tokens` no row; non-loopback 404; facts survive capture deletion)
- [X] T031 [P] Final pass: `gofmt`, `go vet ./...`, `CGO_ENABLED=0 go build ./cmd/tokentracer` produces a static binary

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (P1)** → **Foundational (P2)** → user stories
- **US1** needs only T005 from Foundational (the Exchange/Sink types)
- **US2** needs T005 + T006; integrates with US1's proxy at T016 but is independently testable through `Record(Exchange)` alone
- **US3** is pure — needs only Setup; can run fully parallel to US1/US2
- **US4** needs US2 (rows to read) and US3 (rates to price with)
- **Polish** needs US1–US4

### Story Dependency Graph

```
Setup ─→ Foundational ─→ US1 (proxy) ──┐
                     └─→ US2 (record) ─┼─→ US4 (dashboard) ─→ Polish
Setup ──────────────────→ US3 (billing) ┘
```

### Within Each Story

Tests first (fail) → implementation → wiring in main.go → checkpoint verification.

---

## Parallel Examples

```text
# Setup:              T003 (fixture move) ∥ T004 (logo copy)
# US2 tests:          T010 (anthropic tests) ∥ T011 (store tests)
# Across stories:     all of US3 (T018–T020) ∥ US1/US2
# US4:                T022 (api tests) ∥ T023 (queries.go); T021 blocks T024
# Polish:             T028 (README) ∥ T031 (final build pass)
```

---

## Implementation Strategy

**MVP first**: Setup → Foundational → US1. Stop, run Claude Code through it, validate streaming. That alone replaces nothing but proves the riskiest invariant (non-buffering, abort drain) early.

**Incremental delivery**: US2 makes it useful (facts in SQLite, queryable by hand). US3+US4 make it a product. Each checkpoint is independently demoable; commit after each task or logical group.

**Suggested MVP scope**: US1 only (T001–T009).
