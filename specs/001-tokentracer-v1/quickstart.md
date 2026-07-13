# Quickstart: TokenTracer v1

## Run

```sh
go run ./cmd/tokentracer        # defaults: PORT=8787, UPSTREAM=https://api.anthropic.com, ./tokentracer.db
```

## Point Claude Code at it

```sh
ANTHROPIC_BASE_URL=http://localhost:8787 claude
```

Run a short task that uses a tool.

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
