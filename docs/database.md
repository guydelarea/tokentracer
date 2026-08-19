# The database

One SQLite file, and it is the only source of truth. No boot replay, no in-memory ring, no sidecar directory.

- **`requests`** — the facts. Usage verbatim from the API, latency (duration and time-to-first-token), status, stop reason, and the request's byte composition. This is what the dashboard reads and what you can query by hand.
- **`captures`** — the drill-down blobs, gzipped: the verbatim request body and the fully assembled response message. Deletable without touching a single fact.

```bash
sqlite3 tokentracer.db 'select model_req, session_id, tool_count, input_tokens, ttft_ms from requests'
```

Things worth knowing:

- **An aborted generation is still recorded.** Hit esc in a client and the upstream may still bill what it generated — so the proxy detaches from the departed client, drains the upstream stream to the end, and records the real usage with `aborted=1`. That spend is invisible to every JSONL-reading tool.
- **Nothing recorded is lost.** A body the parser cannot read still lands as a row that says why, keeping the facts that needed no parser and the capture that reproduces the failure. Ctrl-C flushes the queue before the database closes.
- **The response blob is the whole assembled response** — Anthropic message or OpenAI Responses object, including thinking/reasoning, tool inputs, usage, and the model that actually served it.

## Disk and retention

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

## Analyze it yourself

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
