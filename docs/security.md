# Security and redaction

> [!WARNING]
> Request bodies are captured. **Headers are never written to disk** — your API key lives in `x-api-key` and `Authorization` and never reaches the database — and known credential shapes are stripped out of the bodies (below). Everything else you send in an agent *message* is stored as sent: source, customer data, whatever the session read. Treat `tokentracer.db` as sensitive material. Gzip is not encryption.

## Redaction

Before a capture is stored, `internal/redact` replaces the things that are only ever secrets: `sk-ant-…`/`sk-…`, GitHub `ghp_…`, Google `AIza…`, Slack `xox…`, AWS key ids and `aws_secret_access_key`, JWTs, PEM private-key blocks, and named values in JSON fields, `Authorization`/`x-api-key` headers and `KEY=`/`TOKEN=`/`PASSWORD=` assignments. Each becomes `[redacted:<kind>]`, keeping the field name — that a session exported `ANTHROPIC_API_KEY` is worth knowing; what it exported is not. Responses are redacted too, since a model that echoes a pasted key back leaks it just the same.

This runs on the bytes headed for the database and nothing else. The client's stream is untouched, and every fact is folded from the verbatim body first, so no byte count, token figure or cache-prefix hash moves. It is a scalpel, not an entropy filter: it will not catch a credential in a format nobody has enumerated, and it deliberately leaves hashes, ids and base64 payloads alone rather than shredding the evidence a capture exists to be.

## Local only

The proxy and the dashboard share one port, and the dashboard reads those captures back out. So `/dashboard`, `/api/stats` and `/api/capture` answer **404 to anything that is not this machine**, and the listener binds `127.0.0.1` besides. Two locks, because one is not enough for a file that holds every prompt you have ever sent.

To reach the dashboard from elsewhere, forward the port over SSH — `ssh -L 8787:localhost:8787 devbox` — which keeps it loopback, and is what you want.

## Outbound connections

TokenTracer connects to the upstreams you configured, and to **one other host**: at startup it makes a single `GET` to a published price registry to fill gaps in its built-in rate table. It is worth being precise about it, because it is the only connection you did not ask for.

- It sends a plain GET and nothing else — no request bodies, no model names, no session ids, no identifier of any kind. Nothing recorded in your database is involved, and nothing about your traffic leaves the machine.
- It happens once, at startup, bounded to 3 seconds and 8 MB, and a failure is not fatal.
- The response can only add prices for models the built-in table has no rate for. It cannot change a price, so a compromised or wrong registry cannot silently restate what your recorded traffic cost.

`TOKENTRACER_RATES_URL=off` disables it, after which TokenTracer connects to nothing but your configured upstreams. See [Price refresh](configuration.md#price-refresh).
