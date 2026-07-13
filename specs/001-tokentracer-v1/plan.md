# Implementation Plan: TokenTracer v1

**Branch**: `001-tokentracer-v1` | **Date**: 2026-07-13 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/001-tokentracer-v1/spec.md` (the TokenTracer v1 design doc, `docs/superpowers/specs/2026-07-12-tokentracer-v1-design.md`)

## Summary

TokenTracer is a local reverse proxy between an AI coding tool (v1: Claude Code via `ANTHROPIC_BASE_URL`) and the Anthropic Messages API. It streams responses through untouched, records per-request usage facts (tokens, cache behavior, latency, request-shape byte splits) into a single SQLite database, stores gzipped verbatim request/assembled-response capture blobs for drill-down, and serves a loopback-only dashboard that prices usage at read time. Clean-room Go rewrite of `/home/guy/tokentrace`; only the logo and dashboard frontend are reused.

## Technical Context

**Language/Version**: Go (latest stable; `CGO_ENABLED=0` static binary)
**Primary Dependencies**: `modernc.org/sqlite` (pure-Go SQLite driver) — the only external dependency
**Storage**: SQLite, single `tokentracer.db` file (WAL, `synchronous=NORMAL`, `busy_timeout=5000`, `foreign_keys=ON`); versioned migrations via `schema_migrations` table
**Testing**: stdlib `testing`; real-capture fixture (`testdata/anthropic_capture.json.gz`), synthesized SSE replay (`testdata/replay.sse`), `httptest.Server` fake upstream
**Target Platform**: local dev machine (Linux/macOS/WSL), loopback only
**Project Type**: single Go binary (`cmd/tokentracer`) with embedded web dashboard
**Performance Goals**: proxy path adds no perceptible latency — zero parsing on the client path, per-chunk flush; parse+insert happens off-path in a single sink worker goroutine
**Constraints**: dashboard and API bind + enforce `127.0.0.1` (defense in depth); usage stored verbatim (facts), cost computed only at read time; unknown model must surface as unpriced, never silent $0; deleting captures never touches fact rows
**Scale/Scope**: single user, one Claude Code session at a time; ~6 packages, 6 milestones (M0–M5); Anthropic `/v1/messages` only (SSE + non-streaming)

No NEEDS CLARIFICATION — all open questions were closed in the spec ("User decisions made during brainstorm" and "Decisions made closing the Plan agent's open questions").

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

`.specify/memory/constitution.md` is the unfilled template — no project principles have been ratified. No gates to enforce; check passes vacuously. (Worth running `/speckit-constitution` at some point; the spec's own invariants — facts-not-interpretations, loopback-only, proxy-never-blocks — act as de-facto principles and are carried through this plan.)

**Post-design re-check (after Phase 1)**: still no constitution; design artifacts introduce no new packages or dependencies beyond the spec. Pass.

## Project Structure

### Documentation (this feature)

```text
specs/001-tokentracer-v1/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/           # Phase 1 output
│   ├── http-api.md      # /api/stats, /api/capture, /dashboard, proxy behavior
│   └── config.md        # env vars
└── tasks.md             # Phase 2 output (/speckit-tasks — NOT created by /speckit-plan)
```

### Source Code (repository root)

```text
tokentracer/
├── go.mod                          # module github.com/guydelarea/tokentracer; require modernc.org/sqlite
├── cmd/tokentracer/main.go         # env config, wiring, ListenAndServe on 127.0.0.1:PORT
├── internal/
│   ├── proxy/proxy.go              # catch-all reverse handler, streaming tee, latency stamps, Sink handoff
│   ├── anthropic/request.go        # ParseRequest + BreakdownRequest (pure functions, no I/O)
│   ├── anthropic/sse.go            # DecodeSSE / DecodeJSON → assembled message + merged usage
│   ├── billing/billing.go          # time-keyed rate table, Compute(), unpriced handling
│   ├── store/store.go              # Open (pragmas, migrations), InsertRequest, InsertCapture
│   ├── store/queries.go            # read side: recent rows, timeline buckets, lifetime totals
│   └── api/                        # /api/stats, /api/capture, /dashboard, loopback middleware
├── web/                            # embed.go (go:embed) + index.html, app.css, app.js, logo.svg
└── testdata/
    ├── anthropic_capture.json.gz   # real fixture (moved from repo root)
    └── replay.sse                  # SSE stream synthesized from the fixture
```

**Structure Decision**: single Go module, `internal/` packages by responsibility (proxy / anthropic / billing / store / api), embedded `web/` assets. Matches the spec's repo layout verbatim.

## Implementation Milestones (from spec, each independently verifiable)

- **M0 — skeleton**: go.mod, env config, stub server on `127.0.0.1:PORT`. Verify: `CGO_ENABLED=0 go build ./...`.
- **M1 — transparent proxy**: forwarding + per-chunk flush + latency stamps, no capture. Verify: Claude Code streams normally through it; non-buffering unit test.
- **M2 — parse + capture + DB**: `anthropic` package (fixture tests), `store` schema, sink worker, gz captures. Verify: real turn lands in `sqlite3` query; deleting captures row leaves fact row.
- **M3 — billing**: rate table + `Compute` + unpriced propagation. Verify: table tests incl. time-windowed rates and unknown model → `Priced:false`.
- **M4 — dashboard**: `/api/stats` fold, frontend port, inspector via `/api/capture`. Verify: live browser during a Claude Code session.
- **M5 — hardening + README**: error-body capture, client-abort behavior, quickstart README.

## Complexity Tracking

No constitution violations (no constitution). No speculative abstractions in the design: one storage engine, one dependency, one worker goroutine, no config beyond three env vars.
