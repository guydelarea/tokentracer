<p align="center">
  <a href="https://github.com/guydelarea/tokentracer">
    <img width="300" alt="TokenTracer" src="web/logo.svg" />
  </a>
</p>

<p align="center">
  A local observability proxy for AI coding agents.
</p>

<p align="center">
  <a href="LICENSE"><img alt="License: MIT" src="https://img.shields.io/badge/license-MIT-blue.svg" /></a>
  <img alt="Go 1.26+" src="https://img.shields.io/badge/go-1.26%2B-00ADD8.svg" />
  <img alt="Dependencies: one" src="https://img.shields.io/badge/dependencies-1-success.svg" />
  <img alt="Status: alpha" src="https://img.shields.io/badge/status-alpha-orange.svg" />
</p>

---

**TokenTracer** sits between an LLM client and its model API. It forwards requests unchanged, streams responses straight back, and records every exchange into one local SQLite file with a dashboard on the same port. [Claude Code](https://claude.com/claude-code), [Codex](https://developers.openai.com/codex), [OpenCode](https://opencode.ai) and [Pi](https://pi.dev) all land in the same dashboard — session-grouped, priced the same way. See what each request costs, what is filling the context window, and the request and decoded response behind every entry. One static Go binary, one dependency, no build step.

### Quickstart

```bash
git clone https://github.com/guydelarea/tokentracer.git
cd tokentracer
go run ./cmd/tokentracer
```

On first run it asks one question — which clients you use — and saves the answer to `./.env`. Then it listens on `:8787` and prints a paste-ready launch command for each client you chose. In another terminal, run yours:

```bash
ANTHROPIC_BASE_URL=http://localhost:8787 claude
```

Open [localhost:8787/dashboard](http://localhost:8787/dashboard) and work as usual. Rows land within two seconds. Setup for every other client — Codex, OpenCode, Pi, Vertex AI, LiteLLM — is in **[docs/clients.md](docs/clients.md)**.

<p align="center">
  <img alt="TokenTracer dashboard showing burn rate, what could have been saved, cache hit rate and the sessions table" src="docs/screenshot.png" />
</p>

### Using it

Three screens, and they nest: the **overview** (burn rate, cache-hit rate, could-have-saved, and the sessions table), a **session trace** (where the money went: the context staircase, cache breaks and why, and a cut list of tool schemas shipped every turn and never called), and an **inspector** for any single request (billing split, system prompt, message history, decoded response).

Everything is recorded in plain SQLite — usage verbatim as the API reported it, cost computed at read time, and a model missing from the rate table reported as **unpriced**, never silently $0. Headers are never written to disk, known credential shapes are redacted from captures, and the dashboard answers only this machine.

> [!NOTE]
> On a Claude or ChatGPT subscription you are not paying per token — read the numbers as *"what this usage would have cost on the API"*.

### Supported clients and providers

TokenTracer reads three wire formats — **Anthropic Messages** (direct or via Vertex AI), **OpenAI Responses** (HTTPS and Codex's WebSocket), and **OpenAI-compatible Chat Completions** — and can front [several providers at once](docs/multiple-upstreams.md) behind one port.

| Client | Works with |
| --- | --- |
| [Claude Code](https://claude.com/claude-code) | Anthropic API *(tested)*, Vertex AI, [LiteLLM](https://docs.litellm.ai/) or another Anthropic-speaking gateway |
| [Codex](https://developers.openai.com/codex) | ChatGPT login/OAuth or OpenAI API key *(tested, WebSocket and HTTPS)* |
| [OpenCode](https://opencode.ai) | ChatGPT login *(tested)*, OpenAI API key, Anthropic API, OpenAI-compatible providers |
| [Pi](https://pi.dev) | Anthropic API, ChatGPT login, OpenAI API key, OpenAI-compatible providers |
| Anything else speaking those APIs | Point its base URL at `http://localhost:8787` |

Other wire protocols (direct Gemini, Bedrock, …) are proxied unchanged but not recorded. The full support matrix, with the exact connection line for each client, is in [docs/clients.md](docs/clients.md).

### Docs

- **[Client setup](docs/clients.md)** — the wizard, launch commands, the support matrix, verifying your client
- **[Several providers at once](docs/multiple-upstreams.md)** — many upstreams behind one port
- **[The dashboard](docs/dashboard.md)** — the three screens, cache-break pricing, unpriced models and the rate table
- **[The database](docs/database.md)** — schema, disk and retention, querying it yourself
- **[Security and redaction](docs/security.md)** — what is stored, what is stripped, why it is loopback-only
- **[Configuration](docs/configuration.md)** — environment variables and `.env`
- **[Development](docs/development.md)** — commands, tests, layout, contributing

### License

[MIT](LICENSE) © Guy Delarea

TokenTracer started life as a Go port of [Matt Pocock's proxy.mjs](https://gist.github.com/mattpocock/5b3d76ea21f5f698aefded47a9cea3b1) and has since been rewritten from the ground up.
