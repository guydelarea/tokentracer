# Quickstart: TokenTracer v1

## Run

```sh
go run ./cmd/tokentracer
```

On first run, choose the option matching the client's authentication. For
Codex, OpenCode, or Pi, choose 4 for ChatGPT login/OAuth and 5 only for an
OpenAI API key. The startup log prints the matching client command.

## Point Claude Code at it

```sh
ANTHROPIC_BASE_URL=http://localhost:8787 claude
```

Run a short task that uses a tool.

## Point OpenCode at it

```sh
OPENCODE_CONFIG_CONTENT='{"provider":{"openai":{"options":{"baseURL":"http://localhost:8787"}}}}' opencode
```

The setup choice is load-bearing even though the launch command is the same.
ChatGPT OAuth must use the `chatgpt.com/backend-api/codex` upstream; an OpenAI
API key must use `api.openai.com/v1`. Sending ChatGPT OAuth to the public API
produces `401 Missing scopes: api.responses.write`. If `UPSTREAMS` or `UPSTREAM`
is set in the shell, it overrides the wizard's saved choice.

Both can be configured at once — answer the wizard `4,5`, or set
`UPSTREAMS='openai=https://api.openai.com/v1,codex=https://chatgpt.com/backend-api/codex'`.
The launch command above then reaches the `openai` route, and the startup log
prints the `/tt/codex` URL for the client on the other one.

## See it

Open <http://localhost:8787/dashboard> — tiles move, request rows appear within 2s, the inspector drawer shows request / response / billing / raw / **breakdown** tabs.

## Verify (end-to-end acceptance, from spec)

1. Streaming in Claude Code feels normal (no buffering).
2. Dashboard rows appear live; token counts match the inspector's raw response usage.
3. Cost is nonzero and plausible; unknown models show an **unpriced** badge, never $0.
4. `ttft_ms < duration_ms` for streamed rows.
5. `count_tokens` calls produce no row.
6. `/api/stats` is unreachable from another machine (404 / connection refused).
7. Facts survive capture deletion:

   ```sh
   sqlite3 tokentracer.db 'select model_req, session_id, tool_count, input_tokens, ttft_ms from requests'
   sqlite3 tokentracer.db 'delete from captures'   # inspector breakdown degrades, fact rows intact
   ```

## Build a static binary

```sh
CGO_ENABLED=0 go build ./cmd/tokentracer
```
