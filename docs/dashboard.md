# The dashboard

The dashboard polls every two seconds. It has three screens, and they nest:

- **Overview** — burn rate, **could have saved**, cache-hit rate, requests/hr, a 60-minute spend timeline stacked by class (cache read, fresh input, cache write, output), and the **sessions table**.
  There is no flat request log. A request is not a thing anyone did — a session is — and the twenty requests that made up one turn are noise until you have picked the session they belong to.
- **Session trace** — click a session and open the on-demand **session graph** for a compact, chronological view of every prompt, tool call, result, and subagent. Click a graph operation to open its **operation flow**: the user prompt, tool calls, returned tool results, and any subagent it spawned; click a spawned subagent to open its own trace. The rest of the trace stays compact until needed. Then inspect where its money went: the context staircase (every request re-ships the whole conversation, and the cumulative-$ line over it is the integral of that climb), what the replies were made of, which requests broke the cache **and why**, what the tools have dumped into the context, and a **cut list** — the schemas it ships on every request and has never once called, priced one at a time.
- **Inspector** — click a request. Its billing split, its byte composition, system prompt, message history, decoded response, raw body.

Opening a session adds `?session=<id>` to the dashboard URL. That URL can be bookmarked or shared, survives a refresh, and participates in browser back and forward navigation.

**Do next** is the point of the whole thing. Each card is a fact with a price on it, ranked by what it costs, and each one is computed in Go where a test can hold it to account. On a real Claude Code session: *119 schemas ride on every request and were never invoked* — 53.7k tokens, resent every turn. Your prompt is not what costs you money.

Cache breaks are priced the only honest way: at what the request cost **above what a cache hit would have cost**. For Anthropic, an idle gap past the 5-minute TTL re-writes the entire prefix even though not one byte changed — on one real session, that alone re-billed $0.92 of a $1.18 bill. GPT-5.6 uses OpenAI's [default 30-minute minimum cache lifetime](https://developers.openai.com/api/reference/resources/responses/methods/create), so its gap diagnosis uses that boundary instead.

Every number is folded server-side in Go. The page only words and draws them — including the advice.

## Unpriced, never $0

A model with no entry in the rate table is reported as **unpriced** — a badge in the log and a counter in the header. It is never quietly billed at some neighbour's price and never silently worth $0. A wrong cost is invisible; an unpriced one is a question you can answer.

The bill follows the model that **served** the request, not the one that was asked for. Those differ more often than you would think, and the difference is money.

Behind a gateway, that name is the gateway's to choose. A route-prefixed one — `anthropic/claude-sonnet-5`, `bedrock/us.anthropic.claude-opus-4-5-v1:0` — prices off the same row as the bare model, because rate keys match on substring. An alias of your own invention (`fast-model`) is **unpriced** until `internal/billing/rates.go` has a row for it, which is the honest answer rather than a neighbour's price. And where a gateway rebuilds usage into a single flat `cache_creation_input_tokens` total instead of Anthropic's per-TTL split, the total bills as a 5-minute write — the only TTL a client gets without asking for the 1-hour beta, and the only reading that does not silently value the write at zero.

Rates live in `internal/billing/rates.go`, seeded from [LiteLLM's price registry](https://github.com/BerriAI/litellm/blob/main/model_prices_and_context_window.json) and updated from the vendors' published model pages. GPT-5.6 Sol, Terra and Luna use [OpenAI's published standard and long-context rates](https://developers.openai.com/api/docs/pricing#text-tokens). Cache reads bill at 0.1x input and cache writes at 1.25x; Anthropic 1-hour writes bill at 2x.

> [!NOTE]
> If you are on a Claude or ChatGPT subscription you are **not** paying per token. Read the number as *"what this usage would have cost on the API"* — the right number for comparing requests, models and habits against each other, and not your bill.
