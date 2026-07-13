<img src="web/logo.svg" alt="TokenTracer" width="72">

# TokenTracer

A local proxy between an AI coding tool and its LLM API that records what every request **actually** cost, and shows where the money leaks.

Facts are recorded verbatim from the wire. Interpretations — cost, burn rate, cache hit rate, the per-request breakdown — are computed at read time, so a corrected price list fixes history instead of rewriting it.

## Quickstart

```sh
go run ./cmd/tokentracer                        # :8787 → https://api.anthropic.com, ./tokentracer.db
ANTHROPIC_BASE_URL=http://localhost:8787 claude # point Claude Code at it
```

Then open **<http://localhost:8787/dashboard>**.

Streaming feels exactly as it did — the proxy does zero parsing on the client path and flushes every chunk as it arrives. Rows appear within two seconds.

## What you get

- **Live spend** — burn rate, cache hit rate, requests/hr, TTFT p50/p95, and a 60-minute spend timeline stacked by class (cache read / fresh input / cache write / output).
- **A flat request log** — one row per exchange: model, session, op, status, latency, token mix, cost.
- **A full request breakdown** — click any row. Every tool schema, system block, and message, named and sized. This is the point of the whole thing: in a real Claude Code turn, **tool schemas are ~76% of the request** (119 schemas, the largest 21 KB, the smallest 149 B). Your prompt is not what costs you money.
- **An unpriced badge, never a silent $0** — a model with no rate in the table is reported as unpriced. A wrong cost is invisible; an unpriced one is a badge.

## Config

Env vars only. No `.env`, no flags, no wizard.

| Env | Default | Meaning |
|---|---|---|
| `PORT` | `8787` | loopback listen port (proxy + dashboard + API) |
| `UPSTREAM` | `https://api.anthropic.com` | upstream base URL |
| `TOKENTRACER_DB` | `./tokentracer.db` | SQLite path, created on first run |

Build a static binary (no cgo, one dependency):

```sh
CGO_ENABLED=0 go build ./cmd/tokentracer
```

## What it records

One SQLite file. `requests` holds the facts — usage verbatim from the API, latency, status, and the request's byte composition. `captures` holds the gzipped verbatim request body and the assembled response message, for drill-down.

**There is no cost column, anywhere.** Prices change; stored costs rot.

```sh
sqlite3 tokentracer.db 'select model_req, session_id, tool_count, input_tokens, ttft_ms from requests'
```

Things worth knowing:

- **Captures store bodies only, never headers.** Your API key lives in `x-api-key` and never touches the database.
- **An aborted generation is still recorded.** Hit esc in Claude Code and Anthropic still bills the tokens it generated — so the proxy detaches from the client, drains the upstream stream to the end, and records the real usage with `aborted=1`. That spend is invisible to every JSONL-reading tool.
- **The dashboard is loopback-only**, twice over: the listener binds `127.0.0.1`, and the dashboard routes 404 any non-loopback caller. It holds full request captures, so one lock is not enough.
- **Nothing recorded is lost.** A body the parser can't read still lands as a row that says why (`err_type`), keeping the facts that needed no parser and the capture that reproduces the failure. Ctrl-C flushes the queue before the database closes.

## Cost is an estimate, not an invoice

The dashboard prices tokens against Anthropic's public per-token list rates. If you are on a Claude subscription you are **not** paying per token — read the number as *"what this usage would have cost on the API"*. It is the right number for comparing requests, models, and habits against each other; it is not your bill.

## Disk

Captures are the expensive part, and they are bigger than you would guess. Measured on a real Claude Code turn (300 KB request, 119 tool schemas):

- **~100 KB gzipped per request**, dominated by the tool schemas that get resent on every single turn.
- 10 requests ≈ 1 MB. A heavy day of a few thousand requests is **hundreds of MB**.

Retention is manual in v1. Deleting captures never touches the facts — the request log, all costs, and every number on the dashboard survive; only the per-request drill-down degrades to the byte splits already on the row:

```sh
sqlite3 tokentracer.db 'delete from captures'   # reclaim the space
sqlite3 tokentracer.db 'vacuum'                 # actually return it to the filesystem
```

## Scope

v1 records the Anthropic Messages API (`POST /v1/messages`, streaming and not) — i.e. Claude Code. Everything else is proxied untouched and never recorded, including `count_tokens`.

## Tests

```sh
go test ./...          # fixture, decoder, recorder-ladder, fold, proxy, and end-to-end tests
go test ./... -race
```

The suite runs against a real capture from a real Claude Code session (`testdata/`), including the adversarial cases: truncated streams, unknown event types, panicking parsers, dead databases, and clients that hang up mid-generation.
