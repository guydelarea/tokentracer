# Config Contract: TokenTracer v1

Env vars only — no `.env` file, no flags, no setup wizard.

| Env | Default | Meaning |
|---|---|---|
| `PORT` | `8787` | loopback listen port (serves proxy + dashboard + API) |
| `UPSTREAM` | `https://api.anthropic.com` | upstream base URL |
| `TOKENTRACER_DB` | `./tokentracer.db` | SQLite database path (created on first run) |

Client wiring: `ANTHROPIC_BASE_URL=http://localhost:8787 claude`.

Retention: manual — `sqlite3 tokentracer.db 'DELETE FROM captures'` reclaims blob space; fact rows are never auto-deleted.
