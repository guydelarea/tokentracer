# Development

```bash
go run ./cmd/tokentracer     # start the proxy on :8787
go test ./...                # the suite
go test ./... -race
gofmt -w . && go vet ./...

CGO_ENABLED=0 go build ./cmd/tokentracer   # static binary
```

The tests run against a real capture from a real Claude Code session (`testdata/`), and the adversarial cases are the point of them: truncated streams, unknown event types, panicking parsers, dead databases, and clients that hang up mid-generation. The one that matters most makes the fake upstream refuse to send a second chunk until the client has observed the first — so a proxy that buffers deadlocks and fails.

## Layout

```
cmd/tokentracer     wiring, config, setup wizard, shutdown order
internal/upstream   route table: several upstreams behind one port
internal/proxy      the client path: forward, stream, stamp, hand over — over
                    HTTPS and Codex's Responses WebSocket. Zero parsing.
internal/record     the Recorder: never loses an exchange it saw
internal/anthropic  vendor module: parse, break down, decode SSE. Pure functions.
internal/openai     vendor module: Responses and Chat Completions. Pure functions.
internal/wire       normalized seam used by the Recorder and dashboard
internal/redact     credential shapes stripped from a capture before it is stored
internal/billing    read-time pricing, generated rate table
internal/store      SQLite: schema, one-transaction writes, three read queries
internal/api        the fold — every number the dashboard shows — plus the routes
web/                the dashboard, embedded with go:embed
```

One vendor per package — `internal/openai` owns both of OpenAI's dialects — and the rest of the application only sees normalized vendor facts.

The governing rule is **facts, not interpretations**: usage is stored exactly as the API reported it, and cost is computed when you read it. A corrected price list fixes history instead of rewriting it — there is no cost column anywhere in the database.

## Contributing

Issues and pull requests are welcome. The most valuable contributions are:

- Credential shapes the redactor does not know yet (`internal/redact`), with a test case each.
- Updates to the rate table (`internal/billing/rates.go`) as models and pricing change.

Before opening a pull request:

```bash
gofmt -w . && go vet ./... && go test ./... -race
```

Keep changes focused. Add a real test for non-trivial behavior — and prefer one that fails for the right reason over one that merely passes.
