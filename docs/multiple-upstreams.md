# Several providers at once

One proxy can front every API you use. Answer the wizard with a list — `1,5` — or set the upstreams directly:

```bash
UPSTREAMS='anthropic=https://api.anthropic.com,openai=https://api.openai.com/v1' \
  go run ./cmd/tokentracer
```

Each client then points at the same `http://localhost:8787` and lands on its own upstream, because the wire dialect is written into the request path: `/v1/messages` is Anthropic, `/chat/completions` and `/responses` are OpenAI. Claude Code and OpenCode need no extra configuration at all.

```bash
ANTHROPIC_BASE_URL=http://localhost:8787 claude                                   # → anthropic
OPENCODE_CONFIG_CONTENT='{"provider":{"openai":{"options":{"baseURL":"http://localhost:8787"}}}}' opencode  # → openai
```

**When two upstreams speak the same dialect**, the path cannot separate them — Codex on a ChatGPT login and OpenCode on an API key both send `/responses`. Add the collider to the list, and TokenTracer gives it a named route:

```bash
UPSTREAMS='anthropic=https://api.anthropic.com,openai=https://api.openai.com/v1,codex=https://chatgpt.com/backend-api/codex' \
  go run ./cmd/tokentracer

# the first route for a dialect answers the bare URL; the rest are addressed by name
codex -c 'openai_base_url="http://localhost:8787/tt/codex"'
```

The startup log prints the right URL for every client of every configured upstream, so you never have to work out which need the `/tt/<name>` prefix. Order matters: the first upstream that can serve a dialect is the one the bare URL reaches.

**Naming a gateway** tells the router what it speaks. `http://localhost:4000` announces nothing on its own, so call the route `anthropic` if LiteLLM is mounted on the Messages API, or `openai` if it serves the OpenAI ones:

```bash
UPSTREAMS='anthropic=http://localhost:4000,openai=https://api.openai.com/v1' go run ./cmd/tokentracer
```

A route named neither still takes everything the named routes did not claim, which is exactly how a single-upstream setup has always behaved.

On the dashboard, each session shows the upstream it talked to, and a filter above the table narrows to one. Both appear only when more than one upstream is configured.

The old single-value `UPSTREAM` still works and still means "send everything here".
