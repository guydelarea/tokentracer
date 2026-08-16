# Config Contract: TokenTracer v1

Configuration comes from environment variables or `KEY=value` lines in
`./.env`. A shell-set variable wins. The first-run wizard writes `UPSTREAMS` to
`.env`; run `go run ./cmd/tokentracer setup` to change it.

| Env | Default | Meaning |
|---|---|---|
| `PORT` | `8787` | loopback listen port (serves proxy + dashboard + API) |
| `UPSTREAMS` | — | comma-separated `name=url` upstreams, in priority order; e.g. `anthropic=https://api.anthropic.com,openai=https://api.openai.com/v1` |
| `UPSTREAM` | `https://api.anthropic.com` | single upstream base URL, honoured when `UPSTREAMS` is unset; use `https://chatgpt.com/backend-api/codex` for ChatGPT OAuth and `https://api.openai.com/v1` only for an OpenAI API key |
| `TOKENTRACER_DB` | `./tokentracer.db` | SQLite database path (created on first run) |

`UPSTREAMS` outranks `UPSTREAM`. With several configured, a request is routed by
the wire dialect in its path (`/v1/messages` → Anthropic, `/chat/completions`
and `/responses` → OpenAI), and `/tt/<name>/…` forces a named route when two
upstreams speak the same dialect. A route's name declares its dialect when the
URL cannot: `anthropic=http://localhost:4000` is a gateway on the Messages API.

Client wiring is printed at startup. For OpenCode's OpenAI provider:

```sh
OPENCODE_CONFIG_CONTENT='{"provider":{"openai":{"options":{"baseURL":"http://localhost:8787"}}}}' opencode
```

The selected upstream must match the client's credential. A ChatGPT OAuth token
sent to `api.openai.com` fails with `401 Missing scopes: api.responses.write`.
Re-run setup and choose 4 for ChatGPT login/OAuth or 5 for an OpenAI API key —
or both, if you run a client on each. If `UPSTREAMS` or `UPSTREAM` is set in the
shell, unset or update it first because it overrides the wizard's `.env` value.

Retention: manual — `sqlite3 tokentracer.db 'DELETE FROM captures'` reclaims blob space; fact rows are never auto-deleted.
