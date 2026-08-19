# Configuration

Environment variables, or `KEY=value` lines in `./.env` (written by the first-run wizard). Precedence: shell env > `./.env` > defaults; `UPSTREAMS` outranks the legacy `UPSTREAM`.

| Variable | Default | Meaning |
| --- | --- | --- |
| `PORT` | `8787` | Local listen port (proxy, dashboard and API share it) |
| `UPSTREAM` | `https://api.anthropic.com` | Upstream base URL (`https://chatgpt.com/backend-api/codex` for ChatGPT OAuth, `https://api.openai.com/v1` only for an OpenAI API key, or `https://<region>-aiplatform.googleapis.com/v1` for Vertex) |
| `UPSTREAMS` | *(unset)* | Several upstreams behind one port: `name=url,name=url,…` — see [Several providers at once](multiple-upstreams.md) |
| `TOKENTRACER_DB` | `./tokentracer.db` | SQLite path, created on first run |
