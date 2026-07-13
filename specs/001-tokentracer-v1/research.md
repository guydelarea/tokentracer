# Research: TokenTracer v1

**Phase 0 output** | Date: 2026-07-13

The spec is a post-brainstorm design doc: every open question was closed before planning (spec sections "User decisions made during brainstorm" and "Decisions made closing the Plan agent's open questions"), and the data model was validated field-by-field against a real Claude Code capture. No NEEDS CLARIFICATION remained in the Technical Context, so this document consolidates the decisions of record rather than new investigation.

## D1 — Language & project shape

- **Decision**: Go, proper multi-package layout (`internal/proxy`, `anthropic`, `billing`, `store`, `api`), real `_test.go` tests with stdlib `testing`.
- **Rationale**: predecessor project (`/home/guy/tokentrace`) proved Go fits the domain; its zero-dep/one-package purism (raw-string HTML dashboard, panic-based selftest) is the thing being rewritten away.
- **Alternatives considered**: keep single-package zero-dep style (rejected — it forced a no-backticks JS dashboard and untestable structure); other languages (never in play; the Go concepts are proven).

## D2 — Storage

- **Decision**: SQLite only, via `modernc.org/sqlite` (pure Go). One `tokentracer.db` as single source of truth. WAL, `synchronous=NORMAL`, `busy_timeout=5000`, `foreign_keys=ON`. Migrations = `schema_migrations` table + ordered SQL slices in `store.go`.
- **Rationale**: API endpoints become plain SQL queries; survives restarts without replay; `modernc.org/sqlite` keeps `CGO_ENABLED=0` static builds. A migration library is not worth a dependency for ordered SQL slices.
- **Alternatives considered**: tokentrace's append-only JSONL + gz sidecars + in-memory ring replayed on boot (rejected — boot replay, two sources of truth, no ad-hoc querying); `mattn/go-sqlite3` (rejected — cgo breaks static binary).

## D3 — Scope of v1

- **Decision**: Anthropic Messages API only (`POST /v1/messages`, SSE + non-streaming) — i.e. Claude Code via `ANTHROPIC_BASE_URL=http://localhost:8787`. `count_tokens` and non-`/v1/messages` paths are proxied but never recorded.
- **Rationale**: narrow v1 ships; the pillars delivered are Track + Understand. Improve needs a recommendation mechanism that isn't designed yet, but v1 records the facts (latency, tool bytes, called tools, cache behavior) that make it possible later.
- **Alternatives considered**: OpenAI Chat Completions/Responses, Codex/Cursor support, Sessions/Trace views (all deferred to v2+, listed in spec "Deferred").

## D4 — Capture blob format

- **Decision**: `captures` stores the verbatim gzipped request body and the **assembled** response message (all block content incl. thinking text + signature, tool_use id/name/input JSON, plus model, stop_reason, usage) as gzipped JSON — not raw SSE.
- **Rationale**: matches what the reused inspector frontend consumes and the existing fixture format; ~3× smaller than raw SSE; the lossiness lesson from tokentrace (its sidecar dropped thinking text, usage, served model) dictates the blob alone must answer any future question.
- **Alternatives considered**: raw SSE bytes (rejected — 3× bigger, inspector would need client-side SSE decoding); decoded-but-lossy sidecar like tokentrace (rejected — proven to lose design-relevant facts).

## D5 — Pricing model

- **Decision**: no cost column anywhere; usage stored verbatim; `billing.Compute` prices per row at read time from a time-keyed rate table (`[From, Until)` windows, substring match on normalized model name). Unknown model → `Priced:false`, surfaced as a UI badge + `unpricedReqs` counter. Multipliers: read 0.1, 5m write 1.25, 1h write 2.0. Seed rates verified against Anthropic's current price list **at implementation time** — do not copy tokentrace's table (contains fictional/aliased names).
- **Rationale**: facts-not-interpretations — prices change, stored costs rot; the silent-$0-for-unknown-model footgun in tokentrace is explicitly fixed.
- **Alternatives considered**: store computed cost per row (rejected — interpretation baked into facts, wrong after price changes); error on unknown model (rejected — proxy must never block the client path).

## D6 — Proxy architecture

- **Decision**: catch-all handler; buffer request body; forward with hop-by-hop headers stripped and transport compression disabled; stream back with per-chunk `Flush()` while teeing; stamp t0/TTFT/duration; hand `Exchange` to a `Sink` interface; a **single worker goroutine** does parse+insert (also serializing SQLite writes). Client abort keeps captured bytes and records the facts that arrived.
- **Rationale**: zero parsing on the client path is the proven tokentrace invariant; one worker makes SQLite write-serialization free.
- **Alternatives considered**: `httputil.ReverseProxy` (viable but the custom handler is small and needs the tee + timing stamps anyway); parse inline on the response path (rejected — risks blocking the stream).

## D7 — Dashboard & API surface

- **Decision**: reuse tokentrace's frontend (extract HTML/CSS/JS from `dashboard.go` raw string into `web/`, copy `logo.svg`), serve via `go:embed`. Keep `/api/stats` contract names where the view survives; replace sessions table with a flat request log; inspector gains a **breakdown** tab fed by `/api/capture`'s server-side `BreakdownRequest` fold. Numbers folded in Go; the page only words and draws them. Loopback enforced twice: listener binds `127.0.0.1` *and* middleware 404s non-loopback RemoteAddr. Port mechanically first (the JS has no backticks — legacy of the raw-string constraint); modernize later.
- **Rationale**: the frontend is the one explicitly reused asset; server-side folding is the proven numbers-in-Go invariant; defense in depth for a tool that holds full request captures.
- **Alternatives considered**: new frontend (rejected for v1 — reuse is the point); client-side breakdown computation (rejected — breaks numbers-in-Go).

## D8 — Operational defaults

- **Decision**: port 8787 (TokenTracer replaces tokentrace on the same port); DB path defaults to `./tokentracer.db`; capture retention manual (`DELETE FROM captures`); fourth overview tile = latency (p50/p95 TTFT); config via env vars only (`PORT`, `UPSTREAM`, `TOKENTRACER_DB`) — no `.env`, no wizard.
- **Rationale**: closes the Plan agent's open questions with the laziest workable answers; deleting captures never touches fact rows (relational form of the fact/drill-down split).
- **Alternatives considered**: new port side-by-side with tokentrace (rejected — replacement, not coexistence); auto-pruning captures (deferred to v2).

## Implementation-time verifications (not blockers)

- Anthropic list prices for the rate table — check docs when writing `billing.go` (D5).
- Exact Go version pin in `go.mod` — latest stable at implementation time.
