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

To close that gap, startup makes one `GET` to a published price registry and brings the table up to date from it. Two different things happen:

- **Reprice.** A model the built-in table already knows takes the published price. Because cost is computed at read time, this corrects **history as well as new traffic** — the same property that lets a corrected rate table fix the past instead of rewriting it.
- **Add.** A model the table has no rate for is appended, matched on its **exact** name. The registry publishes bare family keys like `gpt-4`, and matching those loosely would price an unreleased `gpt-4-…` at the wrong rate instead of flagging it unpriced.

What the registry may **not** do is change how the table is keyed:

- It cannot add a rate tier. Whether a model has a long-context premium stays hand-verified — the registry still publishes above-200K tiers for models that dropped them, and they would otherwise return on every boot. A tier the table already declares does get its numbers refreshed.
- It cannot drop a verified tier either, so GPT-5.6 above 272K keeps pricing correctly even if a published row omits it.
- Rows whose published cache discounts disagree with the multipliers TokenTracer bills at are skipped entirely, rather than imported at a price it would then bill wrongly.

The fetch is bounded at 3 seconds and 8 MB, and **failure is never fatal** — an unreachable or malformed registry just means the built-in table. The `Pricing` line says exactly what happened, and **names every model whose price moved**:

```
  Pricing    84 models from raw.githubusercontent.com — 28 embedded, 56 added, 1 repriced (gpt-5.6)
  Pricing    28 models, embedded table (refresh off)
  Pricing    28 models, embedded table — refresh failed: … no such host
```

Set `TOKENTRACER_RATES_URL=off` to pin prices to the built-in table and make TokenTracer connect to nothing but your configured upstreams. See [Outbound connections](security.md#outbound-connections) for what repricing means for trust.
