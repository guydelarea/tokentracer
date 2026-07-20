---
name: verify
description: How to run tokentracer against a fake upstream and drive the dashboard for end-to-end verification.
---

# Verifying tokentracer end-to-end

The surface is HTTP on both sides: the proxy (`POST /v1/messages`) is what a
client hits, and the dashboard (`/dashboard`, `/api/stats`, `/api/trace`,
`/api/capture`) is what a person reads. No test doubles needed — the real
binary against a scripted upstream is the cheapest harness there is.

## Recipe that works

1. Build: `go build -o /tmp/tokentracer ./cmd/tokentracer`
2. Fake upstream: a ~40-line Python `http.server` on `127.0.0.1:9797` that
   answers `POST` with a canned Anthropic Messages JSON body (non-streamed is
   fine — the recorder takes both). Choose the reply by a marker string in the
   request body if you need different response shapes per call.
3. Run: `UPSTREAM=http://127.0.0.1:9797 PORT=8787 TOKENTRACER_DB=/tmp/tt.db /tmp/tokentracer`
   — pass `UPSTREAM` explicitly or the first-run setup wizard grabs the TTY.
4. Drive the proxy like Claude Code does: `POST http://127.0.0.1:8787/v1/messages`
   with `metadata.user_id` set to a *JSON-encoded string* like
   `"{\"session_id\":\"<uuid>\"}"` — that is how sessions get their identity.
5. Read the dashboard JSON with curl, and the pixels with Playwright:
   `executablePath: '/opt/pw-browsers/chromium-1194/chrome-linux/chrome'` (in
   this container), viewport ≥1180px wide (the page has `min-width:1180px`).
   Wait ~2.5s after load/navigation — the page polls every 2s.

## Gotchas

- The dashboard is loopback-only; drive it from the same machine.
- The SPA has no history routing: navigate with the in-page `#back` control,
  never `page.goBack()`.
- `pkill -f tokentracer` kills your own harness shell (the pattern matches its
  command line). Kill by pid.
- A session with no tool schemas shows the "capture deleted" warnbox in the
  cut list — that is the no-tools path, not a bug.
