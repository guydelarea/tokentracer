<p align="center">
  <a href="https://github.com/guydelarea/tokentracer">
    <img width="300" alt="TokenTracer" src="web/logo.svg" />
  </a>
</p>

<p align="center">
  A local observability proxy for AI coding agents.
</p>

<p align="center">
  <a href="LICENSE"><img alt="License: MIT" src="https://img.shields.io/badge/license-MIT-blue.svg" /></a>
  <img alt="Go 1.26+" src="https://img.shields.io/badge/go-1.26%2B-00ADD8.svg" />
  <img alt="Dependencies: one" src="https://img.shields.io/badge/dependencies-1-success.svg" />
  <img alt="Status: alpha" src="https://img.shields.io/badge/status-alpha-orange.svg" />
</p>

<p align="center">
  <a href="#quickstart">Quickstart</a> &middot;
  <a href="#dashboard">Dashboard</a> &middot;
  <a href="#what-it-records">What it records</a> &middot;
  <a href="#configuration">Configuration</a> &middot;
  <a href="#contributing">Contributing</a>
</p>

<p align="center">
  <img alt="TokenTracer dashboard showing spend, cache hit rate, latency and a request log" src="docs/screenshot.png" />
</p>

---

**TokenTracer** sits between an LLM client and its model API. It forwards requests unchanged, streams responses straight back to the client, and records the traffic locally for inspection.

See what each coding-agent request costs, what is filling the context window, and the request and decoded response behind every entry. One static Go binary, one dependency, no build step.

The governing rule is **facts, not interpretations**: usage is stored exactly as the API reported it, and cost is computed when you read it. A corrected price list fixes history instead of rewriting it — there is no cost column anywhere in the database.

```
tool schemas    230.7 KB   76%     ← the request is mostly your tools, not your prompt
system prompt    28.8 KB    9%
message history  44.4 KB   15%

Workflow         21.1 KB          DesignSync  9.1 KB          list_projects  149 B
```

### Quickstart

```bash
git clone https://github.com/guydelarea/tokentracer.git
cd tokentracer
go run ./cmd/tokentracer
```

No setup, no wizard, no `.env`. It listens on `:8787` and forwards to `https://api.anthropic.com`, writing `./tokentracer.db`.

In another terminal, point Claude Code at the proxy:

```bash
ANTHROPIC_BASE_URL=http://localhost:8787 claude
```

Open [localhost:8787/dashboard](http://localhost:8787/dashboard) and work as usual. Rows land within two seconds.

Build a static binary:

```bash
CGO_ENABLED=0 go build ./cmd/tokentracer
```

### Dashboard

The dashboard polls `/api/stats` every two seconds and shows:

- **Overview:** burn rate, cache-hit rate, requests/hr, **input and output tokens**, TTFT p50/p95, and a 60-minute spend timeline stacked by class — cache read, fresh input, cache write, output.
  Input tokens count everything the model read — fresh input, cache reads *and* cache writes — because that is what answers "what did I send it". Tokens are facts: they are the numbers on this page that stay right even if the rate table is wrong.
- **Request log:** one row per exchange — model, session, op, status, latency, token mix, cost.
- **Inspector:** click any row for its billing split, system prompt, message history, decoded response, raw body — and the **breakdown**.

The breakdown is the point of the whole thing. It itemizes a single request: every tool schema, system block, and message, named and sized. On a real Claude Code turn, **tool schemas are ~76% of what goes over the wire** — 119 schemas, the largest 21 KB, the smallest 149 B, resent in full on every turn. Your prompt is not what costs you money.

Every number is folded server-side in Go. The page only words and draws them.

### Unpriced, never $0

A model with no entry in the rate table is reported as **unpriced** — a badge in the log and a counter in the header. It is never quietly billed at some neighbour's price and never silently worth $0. A wrong cost is invisible; an unpriced one is a question you can answer.

The bill follows the model that **served** the request, not the one that was asked for. Those differ more often than you would think, and the difference is money.

Rates live in `internal/billing/rates.go`, generated from [LiteLLM's price registry](https://github.com/BerriAI/litellm/blob/main/model_prices_and_context_window.json). Cache reads bill at 0.1x input, cache writes at 1.25x (5-minute) or 2x (1-hour), and requests above 200K input tokens price on Anthropic's long-context tier — which Claude Code sessions live near, so ignoring it would skew exactly the expensive requests.

> [!NOTE]
> If you are on a Claude subscription you are **not** paying per token. Read the number as *"what this usage would have cost on the API"* — the right number for comparing requests, models and habits against each other, and not your bill.

### Supported clients

v1 records the **Anthropic Messages API** (`POST /v1/messages`, streaming and not) — that is Claude Code. Any client that speaks it and can override its base URL works; existing authentication headers pass through unchanged.

| Client | Status | Connection |
| --- | --- | --- |
| [Claude Code](https://claude.com/claude-code) | Tested | `ANTHROPIC_BASE_URL=http://localhost:8787` |
| Anthropic Messages API client | Should work | Set its base URL to `http://localhost:8787` |
| [OpenCode](https://opencode.ai), [Codex](https://developers.openai.com/codex), [Cursor](https://cursor.com) | Not yet | See the [roadmap](#roadmap) |

Everything else on any other path is proxied through untouched and never recorded, including `count_tokens`.

Sessions are grouped by Claude Code's `session_id`, which it buries inside the `metadata.user_id` string. A client that sends none is recorded fine and groups under `unknown`.

### What it records

One SQLite file, and it is the only source of truth. No boot replay, no in-memory ring, no sidecar directory.

- **`requests`** — the facts. Usage verbatim from the API, latency (duration and time-to-first-token), status, stop reason, and the request's byte composition. This is what the dashboard reads and what you can query by hand.
- **`captures`** — the drill-down blobs, gzipped: the verbatim request body and the fully assembled response message. Deletable without touching a single fact.

```bash
sqlite3 tokentracer.db 'select model_req, session_id, tool_count, input_tokens, ttft_ms from requests'
```

Things worth knowing:

- **An aborted generation is still recorded.** Hit esc in Claude Code and Anthropic still bills what it generated — so the proxy detaches from the departed client, drains the upstream stream to the end, and records the real usage with `aborted=1`. That spend is invisible to every JSONL-reading tool.
- **Nothing recorded is lost.** A body the parser cannot read still lands as a row that says why, keeping the facts that needed no parser and the capture that reproduces the failure. Ctrl-C flushes the queue before the database closes.
- **The response blob is the whole assembled message** — thinking text and signature, tool inputs as real JSON, usage, the model that actually served it. Its predecessor stored a lossy summary and could never answer a question it had not already thought of.

### Logs and security

> [!WARNING]
> Request bodies are captured verbatim. **Headers are never written to disk** — your API key lives in `x-api-key` and `Authorization` and never reaches the database — but any secret or customer data you send in an agent *message* does. Treat `tokentracer.db` as sensitive material. Gzip is not encryption.

The proxy and the dashboard share one port, and the dashboard reads those captures back out. So `/dashboard`, `/api/stats` and `/api/capture` answer **404 to anything that is not this machine**, and the listener binds `127.0.0.1` besides. Two locks, because one is not enough for a file that holds every prompt you have ever sent.

To reach the dashboard from elsewhere, forward the port over SSH — `ssh -L 8787:localhost:8787 devbox` — which keeps it loopback, and is what you want.

### Disk

Captures are the expensive part, and they are bigger than you would guess. Measured on a real Claude Code turn (300 KB request, 119 tool schemas):

- **~100 KB gzipped per request**, dominated by the tool schemas resent on every turn.
- 10 requests ≈ 1 MB. A heavy day of a few thousand requests is **hundreds of MB**.

Retention is manual in v1. Deleting captures never touches the facts — the request log, all costs, and every number on the dashboard survive; only the per-request drill-down degrades to the byte splits already on the row:

```bash
sqlite3 tokentracer.db 'delete from captures'   # reclaim the space
sqlite3 tokentracer.db 'vacuum'                 # hand it back to the filesystem
```

### Analyze the database

It is plain SQLite, so it works with tools you already use. Usage by model:

```bash
sqlite3 tokentracer.db "
  select coalesce(model_served, model_req) as model,
         count(*)                  as requests,
         sum(input_tokens)         as input_tokens,
         sum(cache_read_tokens)    as cache_reads,
         sum(output_tokens)        as output_tokens
  from requests
  group by model
  order by input_tokens desc;
"
```

The rows hold raw API usage and no prices — pricing happens on read, in `internal/billing`. That is deliberate: sums like the one above are fine for tokens, but they cannot price correctly, because whether a request crossed the long-context threshold is a per-request fact that `group by` destroys.

### Configuration

Environment variables only.

| Variable | Default | Meaning |
| --- | --- | --- |
| `PORT` | `8787` | Local listen port (proxy, dashboard and API share it) |
| `UPSTREAM` | `https://api.anthropic.com` | Upstream base URL |
| `TOKENTRACER_DB` | `./tokentracer.db` | SQLite path, created on first run |

### Development

```bash
go run ./cmd/tokentracer     # start the proxy on :8787
go test ./...                # the suite
go test ./... -race
gofmt -w . && go vet ./...
```

The tests run against a real capture from a real Claude Code session (`testdata/`), and the adversarial cases are the point of them: truncated streams, unknown event types, panicking parsers, dead databases, and clients that hang up mid-generation. The one that matters most makes the fake upstream refuse to send a second chunk until the client has observed the first — so a proxy that buffers deadlocks and fails.

Layout:

```
cmd/tokentracer     wiring, config, shutdown order
internal/proxy      the client path: forward, stream, stamp, hand over. Zero parsing.
internal/record     the Recorder: never loses an exchange it saw
internal/anthropic  vendor module: parse, break down, decode SSE. Pure functions.
internal/billing    read-time pricing, generated rate table
internal/store      SQLite: schema, one-transaction writes, three read queries
internal/api        the fold — every number the dashboard shows — plus the routes
web/                the dashboard, embedded with go:embed
```

One vendor per package, one file per vendor. A second one (`internal/openai/`) follows that shape.

### Contributing

Issues and pull requests are welcome. The most valuable contributions are:

- OpenAI Chat Completions and Responses parsing, for Codex, Cursor and OpenCode.
- Request-body secret redaction.
- Updates to the rate table as models and pricing change.

Before opening a pull request:

```bash
gofmt -w . && go vet ./... && go test ./... -race
```

Keep changes focused. Add a real test for non-trivial behavior — and prefer one that fails for the right reason over one that merely passes.

### Roadmap

- [ ] OpenAI-compatible request parsing (Codex, Cursor, OpenCode)
- [ ] Sessions and Trace views
- [ ] **Improve**: ranked money-leak recommendations — unused tool schemas, exploration re-reads, cache breaks, compaction candidates
- [ ] Request-body secret redaction
- [ ] Capture auto-pruning

### License

[MIT](LICENSE) © Guy Delarea

### Credits

A clean-room rewrite of [tokentrace](https://github.com/guydelarea/tokentrace), which started as a Go port of [Matt Pocock's proxy.mjs](https://gist.github.com/mattpocock/5b3d76ea21f5f698aefded47a9cea3b1). The logo and the dashboard are the parts worth keeping; everything behind them was written fresh.
