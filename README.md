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
  <a href="#supported-clients">Supported clients</a> &middot;
  <a href="#what-it-records">What it records</a> &middot;
  <a href="#configuration">Configuration</a> &middot;
  <a href="#contributing">Contributing</a>
</p>

<p align="center">
  <img alt="TokenTracer dashboard showing burn rate, what could have been saved, cache hit rate and the sessions table" src="docs/screenshot.png" />
</p>

---

**TokenTracer** sits between an LLM client and its model API. It forwards requests unchanged, streams responses straight back to the client, and records the traffic locally for inspection.

It reads three wire formats — **Anthropic Messages** (direct or via Vertex AI), **OpenAI Responses** (over HTTPS and Codex's WebSocket), and **OpenAI-compatible Chat Completions** — so [Claude Code](https://claude.com/claude-code), [Codex](https://developers.openai.com/codex), [OpenCode](https://opencode.ai) and [Pi](https://pi.dev) all land in the same dashboard, session-grouped and priced the same way.

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

On first run it asks one question — which clients you use — and saves the answer to `./.env`:

```text
tokentracer: first-run setup — which clients? (comma-separated, e.g. 1,5)
  1) Claude Code / Pi — Anthropic API (default)
  2) Claude Code — Vertex AI
  3) Claude Code — LiteLLM or another gateway speaking the Anthropic API
  4) Codex / OpenCode / Pi — ChatGPT login / OAuth (chatgpt.com)
  5) Codex / OpenCode / Pi — OpenAI API key (api.openai.com; not ChatGPT OAuth)
  6) Other — paste an upstream base URL
```

**Answer with as many as you run.** `1,5` puts Claude Code on Anthropic and
OpenCode on OpenAI behind the same port, in the same dashboard — see
[Several providers at once](#several-providers-at-once).

Pick 2 for **Vertex AI** and it asks for your region (blank = global), pointing the proxy at the right Google endpoint. Auth (your `gcloud` ADC token) passes through untouched. Re-run the wizard anytime with `go run ./cmd/tokentracer setup`; a set `UPSTREAMS` env var always outranks the saved answer, and non-interactive runs (pipes, CI) skip the wizard and use the defaults.

Pick 3 for **[LiteLLM](https://docs.litellm.ai/)** and it asks for the gateway's
base URL (blank = `http://localhost:4000`). The proxy then sits between Claude
Code and the gateway, and the gateway keeps doing its own routing — TokenTracer
records what Claude Code sent and what came back, whichever model LiteLLM chose
to serve it with.

Pick 4 for the existing ChatGPT OAuth login used by Codex, OpenCode, or Pi. Pick
5 only when the client authenticates with an OpenAI API key. These upstreams
are not interchangeable: sending a ChatGPT OAuth token to the public OpenAI API
fails with `401 Missing scopes: api.responses.write`. TokenTracer never reads or
stores the credential; the client's `Authorization` header passes through.
Pick 1 for anything speaking the Anthropic API — Claude Code, Pi, and
OpenCode's `anthropic` provider alike.

Then it listens on `:8787` and forwards upstream, writing `./tokentracer.db`. In another terminal, launch your client through the proxy — the startup log prints the exact line for your backend:

```bash
# Anthropic API
ANTHROPIC_BASE_URL=http://localhost:8787 claude

# Vertex AI
CLAUDE_CODE_USE_VERTEX=1 ANTHROPIC_VERTEX_BASE_URL=http://localhost:8787 claude

# LiteLLM (or any gateway serving the Anthropic Messages API)
# The token is the gateway's key, and ANTHROPIC_MODEL names a model it serves.
ANTHROPIC_BASE_URL=http://localhost:8787 \
  ANTHROPIC_AUTH_TOKEN="$LITELLM_KEY" \
  ANTHROPIC_MODEL=claude-sonnet-5 claude

# Codex (setup 4 for ChatGPT login/OAuth; setup 5 for an OpenAI API key)
codex -c 'openai_base_url="http://localhost:8787"'

# OpenCode (setup 4 for ChatGPT login/OAuth; setup 5 for an OpenAI API key)
OPENCODE_CONFIG_CONTENT='{"provider":{"openai":{"options":{"baseURL":"http://localhost:8787"}}}}' opencode

# OpenCode on Anthropic — override that provider instead (pick 1 in setup)
OPENCODE_CONFIG_CONTENT='{"provider":{"anthropic":{"options":{"baseURL":"http://localhost:8787"}}}}' opencode

# Pi (ChatGPT login, OpenAI API, or Anthropic API)
# Add the matching provider override to ~/.pi/agent/models.json, then run pi.
# Examples:
#   {"providers":{"openai-codex":{"baseUrl":"http://localhost:8787"}}}
#   {"providers":{"openai":{"baseUrl":"http://localhost:8787"}}}
#   {"providers":{"anthropic":{"baseUrl":"http://localhost:8787"}}}
pi --provider openai-codex
```

For another OpenCode provider, select *Other* in setup and enter that
provider's real base URL. Then override the same provider's `baseURL` with the
local proxy, for example `{"provider":{"openrouter":{"options":{"baseURL":"http://localhost:8787"}}}}`.
TokenTracer preserves the request path and headers, so the provider still
receives its own authentication and wire format.

Open [localhost:8787/dashboard](http://localhost:8787/dashboard) and work as usual. Rows land within two seconds.

### Several providers at once

One proxy can front every API you use. Answer the wizard with a list — `1,5` —
or set the upstreams directly:

```bash
UPSTREAMS='anthropic=https://api.anthropic.com,openai=https://api.openai.com/v1' \
  go run ./cmd/tokentracer
```

Each client then points at the same `http://localhost:8787` and lands on its own
upstream, because the wire dialect is written into the request path:
`/v1/messages` is Anthropic, `/chat/completions` and `/responses` are OpenAI.
Claude Code and OpenCode need no extra configuration at all.

```bash
ANTHROPIC_BASE_URL=http://localhost:8787 claude                                   # → anthropic
OPENCODE_CONFIG_CONTENT='{"provider":{"openai":{"options":{"baseURL":"http://localhost:8787"}}}}' opencode  # → openai
```

**When two upstreams speak the same dialect**, the path cannot separate them —
Codex on a ChatGPT login and OpenCode on an API key both send `/responses`.
Add the collider to the list, and TokenTracer gives it a named route:

```bash
UPSTREAMS='anthropic=https://api.anthropic.com,openai=https://api.openai.com/v1,codex=https://chatgpt.com/backend-api/codex' \
  go run ./cmd/tokentracer

# the first route for a dialect answers the bare URL; the rest are addressed by name
codex -c 'openai_base_url="http://localhost:8787/tt/codex"'
```

The startup log prints the right URL for every client of every configured
upstream, so you never have to work out which need the `/tt/<name>` prefix.
Order matters: the first upstream that can serve a dialect is the one the bare
URL reaches.

**Naming a gateway** tells the router what it speaks. `http://localhost:4000`
announces nothing on its own, so call the route `anthropic` if LiteLLM is
mounted on the Messages API, or `openai` if it serves the OpenAI ones:

```bash
UPSTREAMS='anthropic=http://localhost:4000,openai=https://api.openai.com/v1' go run ./cmd/tokentracer
```

A route named neither still takes everything the named routes did not claim,
which is exactly how a single-upstream setup has always behaved.

On the dashboard, each session shows the upstream it talked to, and a filter
above the table narrows to one. Both appear only when more than one upstream is
configured.

The old single-value `UPSTREAM` still works and still means "send everything
here".

### Verify your client

This sends one real prompt through your account, so use a small model if you
prefer. It saves no client configuration and reads no credentials.

1. Run the setup wizard and pick the option matching how your client
   authenticates — *ChatGPT login* or *OpenAI API key* for Codex, OpenCode and
   Pi on OpenAI; *Anthropic API* for Claude Code, OpenCode and Pi on Anthropic;
   *LiteLLM* for Claude Code pointed at a gateway:

   ```bash
   go run ./cmd/tokentracer setup
   go run ./cmd/tokentracer
   ```

2. In another terminal, launch your client with the line the startup log
   printed for your backend, and send it one prompt:
   `Reply with exactly: TokenTracer works`. OpenCode can do it in one
   non-interactive command — `opencode models openai` lists the models your
   account can use, and one of them substitutes for `<model>`:

   ```bash
   OPENCODE_CONFIG_CONTENT='{"provider":{"openai":{"options":{"baseURL":"http://localhost:8787"}}}}' \
     opencode run --model openai/<model> 'Reply with exactly: TokenTracer works'
   ```

3. Open [localhost:8787/dashboard](http://localhost:8787/dashboard). Within
   two seconds, it should show one session containing the test prompt, its
   token usage, and its API-equivalent cost. Click the session to inspect the
   request and assembled response.

If no session appears: keep TokenTracer running while you invoke the client,
confirm the wizard selected the same authentication method the client uses, and
check that the client is actually on the proxied provider — for OpenCode, that
the model begins with the provider you overrode, such as `openai/`.

If the dashboard shows `401 Missing scopes: api.responses.write`, the client is
using ChatGPT OAuth while TokenTracer is forwarding to the public OpenAI API.
Run `go run ./cmd/tokentracer setup`, choose 4 for ChatGPT login/OAuth, then
restart both TokenTracer and the client. If `UPSTREAM` is set in your shell, unset
or update it first because it overrides `.env`. Choose 5 only for a real OpenAI
API key.

Build a static binary:

```bash
CGO_ENABLED=0 go build ./cmd/tokentracer
```

### Dashboard

The dashboard polls every two seconds. It has three screens, and they nest:

- **Overview** — burn rate, **could have saved**, cache-hit rate, requests/hr, a 60-minute spend timeline stacked by class (cache read, fresh input, cache write, output), and the **sessions table**.
  There is no flat request log. A request is not a thing anyone did — a session is — and the twenty requests that made up one turn are noise until you have picked the session they belong to.
- **Session trace** — click a session and open the on-demand **session graph** for a compact, chronological view of every prompt, tool call, result, and subagent. Click a graph operation to open its **operation flow**: the user prompt, tool calls, returned tool results, and any subagent it spawned; click a spawned subagent to open its own trace. The rest of the trace stays compact until needed. Then inspect where its money went: the context staircase (every request re-ships the whole conversation, and the cumulative-$ line over it is the integral of that climb), what the replies were made of, which requests broke the cache **and why**, what the tools have dumped into the context, and a **cut list** — the schemas it ships on every request and has never once called, priced one at a time.
- **Inspector** — click a request. Its billing split, its byte composition, system prompt, message history, decoded response, raw body.

Opening a session adds `?session=<id>` to the dashboard URL. That URL can be
bookmarked or shared, survives a refresh, and participates in browser back and
forward navigation.

**Do next** is the point of the whole thing. Each card is a fact with a price on it, ranked by what it costs, and each one is computed in Go where a test can hold it to account. On a real Claude Code session: *119 schemas ride on every request and were never invoked* — 53.7k tokens, resent every turn. Your prompt is not what costs you money.

Cache breaks are priced the only honest way: at what the request cost **above what a cache hit would have cost**. For Anthropic, an idle gap past the 5-minute TTL re-writes the entire prefix even though not one byte changed — on the session in the screenshot, that alone re-billed $0.92 of a $1.18 bill. GPT-5.6 uses OpenAI's [default 30-minute minimum cache lifetime](https://developers.openai.com/api/reference/resources/responses/methods/create), so its gap diagnosis uses that boundary instead.

Every number is folded server-side in Go. The page only words and draws them — including the advice.

### Unpriced, never $0

A model with no entry in the rate table is reported as **unpriced** — a badge in the log and a counter in the header. It is never quietly billed at some neighbour's price and never silently worth $0. A wrong cost is invisible; an unpriced one is a question you can answer.

The bill follows the model that **served** the request, not the one that was asked for. Those differ more often than you would think, and the difference is money.

Behind a gateway, that name is the gateway's to choose. A route-prefixed one — `anthropic/claude-sonnet-5`, `bedrock/us.anthropic.claude-opus-4-5-v1:0` — prices off the same row as the bare model, because rate keys match on substring. An alias of your own invention (`fast-model`) is **unpriced** until `internal/billing/rates.go` has a row for it, which is the honest answer rather than a neighbour's price. And where a gateway rebuilds usage into a single flat `cache_creation_input_tokens` total instead of Anthropic's per-TTL split, the total bills as a 5-minute write — the only TTL a client gets without asking for the 1-hour beta, and the only reading that does not silently value the write at zero.

Rates live in `internal/billing/rates.go`, seeded from [LiteLLM's price registry](https://github.com/BerriAI/litellm/blob/main/model_prices_and_context_window.json) and updated from the vendors' published model pages. GPT-5.6 Sol, Terra and Luna use [OpenAI's published standard and long-context rates](https://developers.openai.com/api/docs/pricing#text-tokens). Cache reads bill at 0.1x input and cache writes at 1.25x; Anthropic 1-hour writes bill at 2x.

> [!NOTE]
> If you are on a Claude or ChatGPT subscription you are **not** paying per token. Read the number as *"what this usage would have cost on the API"* — the right number for comparing requests, models and habits against each other, and not your bill.

### Supported clients

TokenTracer records the **Anthropic Messages API** (`POST .../messages`, streaming and not), the **OpenAI Responses API** (`POST /responses` or `/v1/responses`), and **OpenAI-compatible Chat Completions** (`POST .../chat/completions`). It also understands Anthropic's **Vertex AI** spelling (`.../publishers/anthropic/models/<model>:streamRawPredict` and `:rawPredict`, where the model rides in the URL). All three match on the path suffix, so a gateway that mounts them under a route prefix — LiteLLM's `/anthropic/v1/messages` passthrough, an Azure deployment path — is recorded the same as the vendor's own. Existing authentication headers pass through unchanged.

| Client | Status | Connection |
| --- | --- | --- |
| [Claude Code](https://claude.com/claude-code) | Tested | `ANTHROPIC_BASE_URL=http://localhost:8787` |
| Claude Code via Vertex AI | Should work | Pick *Vertex AI* in the setup wizard, then `CLAUDE_CODE_USE_VERTEX=1 ANTHROPIC_VERTEX_BASE_URL=http://localhost:8787` |
| Claude Code via [LiteLLM](https://docs.litellm.ai/) | Supported | Pick *LiteLLM* in the setup wizard and give it the gateway's base URL, then `ANTHROPIC_BASE_URL=http://localhost:8787 ANTHROPIC_AUTH_TOKEN=$LITELLM_KEY claude`. Name the model with `ANTHROPIC_MODEL` if the gateway's model names are not Anthropic's. |
| Anthropic Messages API client | Should work | Set its base URL to `http://localhost:8787` |
| [Codex](https://developers.openai.com/codex) | Tested (WebSocket and HTTPS) | Pick *ChatGPT login* or *OpenAI API key*, then `codex -c 'openai_base_url="http://localhost:8787"'` |
| [OpenCode](https://opencode.ai) | Tested with ChatGPT login; Anthropic and OpenAI-compatible providers supported | Pick the matching setup option, then override that provider's [`baseURL`](https://opencode.ai/docs/providers) with `OPENCODE_CONFIG_CONTENT` — `openai` for Responses, `anthropic` for Messages. For a compatible non-OpenAI provider, point its own `baseURL` at the proxy after selecting *Other* in setup. |
| [Pi](https://pi.dev) | Supported for Anthropic, ChatGPT login, OpenAI Responses, and OpenAI-compatible Chat Completions providers | Pick the matching setup option, then set `providers.<provider>.baseUrl` in `~/.pi/agent/models.json` to `http://localhost:8787` and run `pi --provider <provider>`. |
| OpenAI Responses or Chat Completions client | Should work | Set its base URL to `http://localhost:8787` |
| Several of the above at once | Supported | List every upstream — `UPSTREAMS='anthropic=…,openai=…'`, or a comma-separated answer in the wizard. Each client keeps its own base URL of `http://localhost:8787`; see [Several providers at once](#several-providers-at-once). |
| Vendor-native OpenCode transports | Not yet | Direct Gemini, Bedrock, and other non-Anthropic/non-OpenAI wire protocols are proxied unchanged but not recorded. |

Everything else on any other path is proxied through untouched and never recorded, including Anthropic `count_tokens`, Vertex's `count-tokens` pseudo-model, and Responses auxiliary endpoints such as `/responses/compact`.

Codex's persistent Responses WebSocket is proxied bidirectionally. Each
`response.create` message is recorded as its own Exchange when the matching
terminal response event arrives, so a long-lived socket still produces the same
dashboard facts and captures as the HTTPS transport.

Anthropic sessions are grouped by Claude Code's embedded `session_id`. Responses sessions use `prompt_cache_key` first, then a session or thread id in `client_metadata` or `metadata`, then `user`; Chat Completions uses the same fallbacks without the cache key. Auxiliary model calls with no session key are still visible under `(no session id)`.

### What it records

One SQLite file, and it is the only source of truth. No boot replay, no in-memory ring, no sidecar directory.

- **`requests`** — the facts. Usage verbatim from the API, latency (duration and time-to-first-token), status, stop reason, and the request's byte composition. This is what the dashboard reads and what you can query by hand.
- **`captures`** — the drill-down blobs, gzipped: the verbatim request body and the fully assembled response message. Deletable without touching a single fact.

```bash
sqlite3 tokentracer.db 'select model_req, session_id, tool_count, input_tokens, ttft_ms from requests'
```

Things worth knowing:

- **An aborted generation is still recorded.** Hit esc in a client and the upstream may still bill what it generated — so the proxy detaches from the departed client, drains the upstream stream to the end, and records the real usage with `aborted=1`. That spend is invisible to every JSONL-reading tool.
- **Nothing recorded is lost.** A body the parser cannot read still lands as a row that says why, keeping the facts that needed no parser and the capture that reproduces the failure. Ctrl-C flushes the queue before the database closes.
- **The response blob is the whole assembled response** — Anthropic message or OpenAI Responses object, including thinking/reasoning, tool inputs, usage, and the model that actually served it. Its predecessor stored a lossy summary and could never answer a question it had not already thought of.

### Logs and security

> [!WARNING]
> Request bodies are captured. **Headers are never written to disk** — your API key lives in `x-api-key` and `Authorization` and never reaches the database — and known credential shapes are stripped out of the bodies (below). Everything else you send in an agent *message* is stored as sent: source, customer data, whatever the session read. Treat `tokentracer.db` as sensitive material. Gzip is not encryption.

**Redaction.** Before a capture is stored, `internal/redact` replaces the things that are only ever secrets: `sk-ant-…`/`sk-…`, GitHub `ghp_…`, Google `AIza…`, Slack `xox…`, AWS key ids and `aws_secret_access_key`, JWTs, PEM private-key blocks, and named values in JSON fields, `Authorization`/`x-api-key` headers and `KEY=`/`TOKEN=`/`PASSWORD=` assignments. Each becomes `[redacted:<kind>]`, keeping the field name — that a session exported `ANTHROPIC_API_KEY` is worth knowing; what it exported is not. Responses are redacted too, since a model that echoes a pasted key back leaks it just the same.

This runs on the bytes headed for the database and nothing else. The client's stream is untouched, and every fact is folded from the verbatim body first, so no byte count, token figure or cache-prefix hash moves. It is a scalpel, not an entropy filter: it will not catch a credential in a format nobody has enumerated, and it deliberately leaves hashes, ids and base64 payloads alone rather than shredding the evidence a capture exists to be.

The proxy and the dashboard share one port, and the dashboard reads those captures back out. So `/dashboard`, `/api/stats` and `/api/capture` answer **404 to anything that is not this machine**, and the listener binds `127.0.0.1` besides. Two locks, because one is not enough for a file that holds every prompt you have ever sent.

To reach the dashboard from elsewhere, forward the port over SSH — `ssh -L 8787:localhost:8787 devbox` — which keeps it loopback, and is what you want.

### Disk

Captures are the expensive part, and they are bigger than you would guess. Measured on a real Claude Code turn (300 KB request, 119 tool schemas):

- **~100 KB gzipped per request**, dominated by the tool schemas resent on every turn.
- 10 requests ≈ 1 MB. A heavy day of a few thousand requests is **hundreds of MB**.

So the dashboard header carries the control: **keep captures forever / 24 hours / 7 days / 30 days**, plus a **purge** that drops all of them now. The window is stored in the database, applied the moment you set it, and re-applied hourly and at startup. It defaults to *forever* — retention deletes evidence, so it only runs because you asked. A sweep that deletes anything vacuums after itself, or the file would never actually shrink.

Deleting captures never touches the facts — the sessions table, every trace, all costs, and the cache diagnosis survive, because the prefix hashes and the output split live on the request row, not in the blob. Only what can be read *nowhere else* goes: the itemized schemas, the cut list, and the per-request drill-down, which degrade to the byte splits already on the row.

The equivalent by hand, if you prefer:

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

Environment variables, or `KEY=value` lines in `./.env` (written by the first-run wizard; a shell-set variable always wins over the file).

| Variable | Default | Meaning |
| --- | --- | --- |
| `PORT` | `8787` | Local listen port (proxy, dashboard and API share it) |
| `UPSTREAM` | `https://api.anthropic.com` | Upstream base URL (`https://chatgpt.com/backend-api/codex` for ChatGPT OAuth, `https://api.openai.com/v1` only for an OpenAI API key, or `https://<region>-aiplatform.googleapis.com/v1` for Vertex) |
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
cmd/tokentracer     wiring, config, setup wizard, shutdown order
internal/proxy      the client path: forward, stream, stamp, hand over — over
                    HTTPS and Codex's Responses WebSocket. Zero parsing.
internal/record     the Recorder: never loses an exchange it saw
internal/anthropic  vendor module: parse, break down, decode SSE. Pure functions.
internal/openai     vendor module: Responses and Chat Completions. Pure functions.
internal/wire       normalized seam used by the Recorder and dashboard
internal/redact     credential shapes stripped from a capture before it is stored
internal/billing    read-time pricing, generated rate table
internal/store      SQLite: schema, one-transaction writes, three read queries
internal/api        the fold — every number the dashboard shows — plus the routes
web/                the dashboard, embedded with go:embed
```

One vendor per package — `internal/openai` owns both of OpenAI's dialects — and the rest of the application only sees normalized vendor facts.

### Contributing

Issues and pull requests are welcome. The most valuable contributions are:

- Credential shapes the redactor does not know yet (`internal/redact`), with a test case each.
- Updates to the rate table as models and pricing change.

Before opening a pull request:

```bash
gofmt -w . && go vet ./... && go test ./... -race
```

Keep changes focused. Add a real test for non-trivial behavior — and prefer one that fails for the right reason over one that merely passes.

### Roadmap

- [x] Sessions and Trace views
- [x] **Improve**: ranked money-leak recommendations — unused tool schemas, exploration re-reads, cache breaks, compaction candidates
- [x] Request-body secret redaction
- [x] Capture auto-pruning
- [x] OpenAI Responses parsing (Codex, OpenCode, Pi)
- [x] OpenAI Chat Completions parsing (OpenCode/Pi-compatible providers and other compatible clients)
- [x] Codex's persistent Responses WebSocket, recorded exchange by exchange
- [x] Several upstreams behind one port, routed by wire dialect

### License

[MIT](LICENSE) © Guy Delarea

### Credits

A clean-room rewrite of [tokentrace](https://github.com/guydelarea/tokentrace), which started as a Go port of [Matt Pocock's proxy.mjs](https://gist.github.com/mattpocock/5b3d76ea21f5f698aefded47a9cea3b1). The logo and the dashboard are the parts worth keeping; everything behind them was written fresh.
