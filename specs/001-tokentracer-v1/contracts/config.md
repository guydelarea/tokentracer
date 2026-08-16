# Config Contract: TokenTracer v1

Configuration comes from environment variables or `KEY=value` lines in
`./.env`. A shell-set variable wins. The first-run wizard writes `UPSTREAM` to
`.env`; run `go run ./cmd/tokentracer setup` to change it.

| Env | Default | Meaning |
|---|---|---|
| `PORT` | `8787` | loopback listen port (serves proxy + dashboard + API) |
| `UPSTREAM` | `https://api.anthropic.com` | upstream base URL; use `https://chatgpt.com/backend-api/codex` for ChatGPT OAuth and `https://api.openai.com/v1` only for an OpenAI API key |
| `TOKENTRACER_DB` | `./tokentracer.db` | SQLite database path (created on first run) |

Client wiring is printed at startup. For OpenCode's OpenAI provider:

```sh
OPENCODE_CONFIG_CONTENT='{"provider":{"openai":{"options":{"baseURL":"http://localhost:8787"}}}}' opencode
```

The selected upstream must match the client's credential. A ChatGPT OAuth token
sent to `api.openai.com` fails with `401 Missing scopes: api.responses.write`.
Re-run setup and choose 4 for ChatGPT login/OAuth or 5 for an OpenAI API key. If
`UPSTREAM` is set in the shell, unset or update it first because it overrides
the wizard's `.env` value.

Retention: manual — `sqlite3 tokentracer.db 'DELETE FROM captures'` reclaims blob space; fact rows are never auto-deleted.
