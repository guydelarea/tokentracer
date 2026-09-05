# Configuration

Environment variables, or `KEY=value` lines in `./.env` (written by the first-run wizard). Precedence: shell env > `./.env` > defaults; `UPSTREAMS` outranks the legacy `UPSTREAM`.

| Variable | Default | Meaning |
| --- | --- | --- |
| `PORT` | `8787` | Local listen port (proxy, dashboard and API share it) |
| `UPSTREAM` | `https://api.anthropic.com` | Upstream base URL (`https://chatgpt.com/backend-api/codex` for ChatGPT OAuth, `https://api.openai.com/v1` only for an OpenAI API key, or `https://<region>-aiplatform.googleapis.com/v1` for Vertex) |
| `UPSTREAMS` | *(unset)* | Several upstreams behind one port: `name=url,name=url,…` — see [Several providers at once](multiple-upstreams.md) |
| `TOKENTRACER_DB` | `./tokentracer.db` | SQLite path, created on first run |
| `TOKENTRACER_RATES_URL` | LiteLLM's price registry | Where the startup price refresh reads from. `off` (or `none`) disables it — see [Price refresh](#price-refresh) |

## Price refresh

Prices live in a table compiled into the binary, hand-verified against Anthropic's and OpenAI's published pages. A model that ships after your build is not in it, and its requests show up as **unpriced** rather than at a guessed rate.

To close that gap, startup makes one `GET` to a published price registry and uses it to **fill holes only**:

- A model the built-in table already prices is left alone. The fetch can never move a verified price, and can never restore a rate tier that was removed on purpose.
- A model it does not price is added, matched on its exact name — the registry publishes bare family keys like `gpt-4`, and matching those loosely would price an unreleased `gpt-4-…` at the wrong rate instead of flagging it unpriced.
- Rows whose published cache discounts disagree with the multipliers TokenTracer bills at are skipped, not imported at a rate that would be wrong.

The fetch is bounded at 3 seconds and 8 MB, and **failure is never fatal** — an unreachable or malformed registry just means the built-in table, and the startup screen's `Pricing` line says which happened:

```
  Pricing    83 models — 28 embedded, 55 filled in from raw.githubusercontent.com
  Pricing    28 models, embedded table (refresh off)
  Pricing    28 models, embedded table — refresh failed: … no such host
```

Set `TOKENTRACER_RATES_URL=off` to make TokenTracer connect to nothing but your configured upstreams.
