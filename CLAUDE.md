# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Active Technologies

- Go (latest stable; `CGO_ENABLED=0` static binary) + `modernc.org/sqlite` (pure-Go SQLite driver) — the only external dependency

## What this is

TokenTracer is a local observability proxy for AI coding agents (Claude Code, Codex, OpenCode, Pi). It sits between an LLM client and its API, forwards traffic unchanged, and records every exchange into one SQLite file with a dashboard on the same port.

## Commands

```bash
go run ./cmd/tokentracer            # start proxy + dashboard on :8787
go run ./cmd/tokentracer setup      # re-run the first-run wizard (writes ./.env)
go test ./...                       # full suite
go test ./... -race
go test ./internal/api -run TestFold   # one package / one test
CGO_ENABLED=0 go build ./cmd/tokentracer

# required before any PR:
gofmt -w . && go vet ./... && go test ./... -race
```

- First run with a TTY and no `UPSTREAM`/`UPSTREAMS` set launches an interactive wizard — in scripts, set `UPSTREAM=...` (or `UPSTREAMS='name=url,...'`) to skip it. `TOKENTRACER_DB` and `PORT` override the DB path and port.
- The dashboard (`web/`) is vanilla JS embedded with `go:embed` — no build step, no npm.

## Governing rule: facts, not interpretations

Usage is stored **verbatim as the API reported it**; cost is computed at read time in `internal/billing`. There is deliberately **no cost column anywhere in the database** — a corrected price list fixes history instead of rewriting it. A model missing from the rate table (`internal/billing/rates.go`) is reported as **unpriced**, never silently $0 or a neighbour's price. Keep this split intact: facts on the row, interpretation in the fold.

## Domain language

`CONTEXT.md` defines the project vocabulary — **Exchange** (one round-trip, the unit of recording), **Recorder**, **Fact**, **Capture**, **Breakdown**, **Fold**, **Bill**, **Unpriced**, **Aborted Exchange**, **Degradation ladder**, **Vendor**, **Session** — each with terms to avoid. Use these words in code, comments, and commit messages.

## Architecture

Request flow: client → `internal/proxy` (forwards and streams, **zero parsing**) → upstream. When the exchange completes, the proxy hands it to `internal/record` (the Recorder), which uses `internal/wire` to normalize it, redacts the capture via `internal/redact`, and writes facts + gzipped capture through `internal/store`. The dashboard reads back through `internal/api`, which computes every displayed number server-side (the fold).

```
cmd/tokentracer     wiring, config/.env, setup wizard, shutdown order
internal/upstream   route table: several upstreams behind one port, routed by
                    wire dialect in the request path; /tt/<name>/ prefix for
                    same-dialect colliders; first route for a dialect is default
internal/proxy      the client path over HTTPS and Codex's Responses WebSocket.
                    Never parses bodies; a client abort detaches and drains the
                    upstream so the billed tokens are still recorded (aborted=1)
internal/record     the Recorder: guarantees no Exchange it saw is ever lost
                    (degradation ladder: parse failure/panic/oversize still
                    produce a row that says why)
internal/anthropic  vendor module: Messages API + Vertex spelling, SSE decode
internal/openai     vendor module: Responses + Chat Completions. Both vendor
                    modules are pure functions; one vendor per package
internal/wire       the normalized seam — Observation/RequestFacts/ResponseFacts.
                    Everything outside the vendor packages sees only this
internal/redact     credential shapes stripped from capture bytes only; the
                    client's live stream is never touched
internal/billing    read-time pricing; rate table in rates.go (substring match
                    on model name, so route-prefixed names price correctly)
internal/rates      startup price refresh: fetches a published registry and maps
                    it to billing.Rate. billing.Merge layers it UNDER the table
                    in rates.go — fills holes only, exact-match keys only, never
                    overrides a hand-verified price. Failure is never fatal
internal/store      SQLite: two tables — `requests` (facts) and `captures`
                    (gzipped request body + assembled response; deletable
                    without touching facts)
internal/api        the fold + HTTP routes (/dashboard, /api/*); also capture
                    retention sweeps
web/                the dashboard SPA, embedded with go:embed
```

Cross-cutting facts worth knowing before touching multiple packages:

- **Sessions** are how the dashboard groups everything. Anthropic requests carry Claude Code's `session_id` inside `metadata.user_id` (a JSON-encoded string); OpenAI Responses falls back through `prompt_cache_key` → `client_metadata`/`metadata` ids → `user`.
- **Shutdown order matters** (`cmd/tokentracer/main.go`): server stops first, then the retention sweeper, then the proxy (ends hijacked WebSockets), then the Recorder drains its queue, then the store closes. This order is the "nothing recorded is lost" guarantee — don't reorder it.
- **Security posture**: dashboard/API answer 404 to non-local requests *and* the listener binds `127.0.0.1`. Headers are never written to disk. Don't weaken either lock.
- Config precedence: shell env > `./.env` (written by the wizard) > defaults; `UPSTREAMS` outranks legacy `UPSTREAM`.

## Tests

Tests run against a real Claude Code capture (`testdata/`), and adversarial cases are the point: truncated streams, unknown event types, panicking parsers, dead databases, clients that hang up mid-generation. The most important proxy test makes the fake upstream refuse to send a second chunk until the client observed the first — a proxy that buffers deadlocks and fails it. Prefer a test that fails for the right reason over one that merely passes; new non-trivial behavior needs one.

## Startup UX

Startup output is a banner plus a paste-ready launch command per configured upstream/client (`startupScreen` in `cmd/tokentracer/main.go`). Extend that screen; never add `log.Printf` spam to startup.
