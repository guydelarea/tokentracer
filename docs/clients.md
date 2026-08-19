# Client setup

TokenTracer records the **Anthropic Messages API** (`POST .../messages`, streaming and not), the **OpenAI Responses API** (`POST /responses` or `/v1/responses`), and **OpenAI-compatible Chat Completions** (`POST .../chat/completions`). It also understands Anthropic's **Vertex AI** spelling (`.../publishers/anthropic/models/<model>:streamRawPredict` and `:rawPredict`, where the model rides in the URL). All three match on the path suffix, so a gateway that mounts them under a route prefix — LiteLLM's `/anthropic/v1/messages` passthrough, an Azure deployment path — is recorded the same as the vendor's own. Existing authentication headers pass through unchanged.

Everything else on any other path is proxied through untouched and never recorded, including Anthropic `count_tokens`, Vertex's `count-tokens` pseudo-model, and Responses auxiliary endpoints such as `/responses/compact`.

## The setup wizard

On first run (or via `go run ./cmd/tokentracer setup`) the wizard asks one question — which clients you use — and saves the answer to `./.env`:

```text
tokentracer: first-run setup — which clients? (comma-separated, e.g. 1,5)
  1) Claude Code / Pi — Anthropic API (default)
  2) Claude Code — Vertex AI
  3) Claude Code — LiteLLM or another gateway speaking the Anthropic API
  4) Codex / OpenCode / Pi — ChatGPT login / OAuth (chatgpt.com)
  5) Codex / OpenCode / Pi — OpenAI API key (api.openai.com; not ChatGPT OAuth)
  6) Other — paste an upstream base URL
```

**Answer with as many as you run.** `1,5` puts Claude Code on Anthropic and OpenCode on OpenAI behind the same port, in the same dashboard — see [Several providers at once](multiple-upstreams.md).

- Pick **1** for anything speaking the Anthropic API — Claude Code, Pi, and OpenCode's `anthropic` provider alike.
- Pick **2** for **Vertex AI** and it asks for your region (blank = global), pointing the proxy at the right Google endpoint. Auth (your `gcloud` ADC token) passes through untouched.
- Pick **3** for **[LiteLLM](https://docs.litellm.ai/)** and it asks for the gateway's base URL (blank = `http://localhost:4000`). The proxy then sits between Claude Code and the gateway, and the gateway keeps doing its own routing — TokenTracer records what Claude Code sent and what came back, whichever model LiteLLM chose to serve it with.
- Pick **4** for the existing ChatGPT OAuth login used by Codex, OpenCode, or Pi. Pick **5** only when the client authenticates with an OpenAI API key. These upstreams are not interchangeable: sending a ChatGPT OAuth token to the public OpenAI API fails with `401 Missing scopes: api.responses.write`. TokenTracer never reads or stores the credential; the client's `Authorization` header passes through.

A set `UPSTREAMS` env var always outranks the saved answer, and non-interactive runs (pipes, CI) skip the wizard and use the defaults.

## Launch commands

The startup screen prints a paste-ready launch command for each client you chose. For reference:

```bash
# Anthropic API
ANTHROPIC_BASE_URL=http://localhost:8787 claude

# Vertex AI
CLAUDE_CODE_USE_VERTEX=1 ANTHROPIC_VERTEX_BASE_URL=http://localhost:8787 claude

# LiteLLM (or any gateway serving the Anthropic Messages API)
# The token is the gateway's key, and ANTHROPIC_MODEL names a model it serves.
ANTHROPIC_BASE_URL=http://localhost:8787 \
  ANTHROPIC_AUTH_TOKEN="$LITELLM_KEY" \
  ANTHROPIC_MODEL=claude-sonnet-5 claude

# Codex (setup 4 for ChatGPT login/OAuth; setup 5 for an OpenAI API key)
codex -c 'openai_base_url="http://localhost:8787"'

# OpenCode (setup 4 for ChatGPT login/OAuth; setup 5 for an OpenAI API key)
OPENCODE_CONFIG_CONTENT='{"provider":{"openai":{"options":{"baseURL":"http://localhost:8787"}}}}' opencode

# OpenCode on Anthropic — override that provider instead (pick 1 in setup)
OPENCODE_CONFIG_CONTENT='{"provider":{"anthropic":{"options":{"baseURL":"http://localhost:8787"}}}}' opencode

# Pi (ChatGPT login, OpenAI API, or Anthropic API)
# Add the matching provider override to ~/.pi/agent/models.json, then run pi.
# Examples:
#   {"providers":{"openai-codex":{"baseUrl":"http://localhost:8787"}}}
#   {"providers":{"openai":{"baseUrl":"http://localhost:8787"}}}
#   {"providers":{"anthropic":{"baseUrl":"http://localhost:8787"}}}
pi --provider openai-codex
```

For another OpenCode provider, select *Other* in setup and enter that provider's real base URL. Then override the same provider's `baseURL` with the local proxy, for example `{"provider":{"openrouter":{"options":{"baseURL":"http://localhost:8787"}}}}`. TokenTracer preserves the request path and headers, so the provider still receives its own authentication and wire format.

## Support matrix

| Client | Status | Connection |
| --- | --- | --- |
| [Claude Code](https://claude.com/claude-code) | Tested | `ANTHROPIC_BASE_URL=http://localhost:8787` |
| Claude Code via Vertex AI | Should work | Pick *Vertex AI* in the setup wizard, then `CLAUDE_CODE_USE_VERTEX=1 ANTHROPIC_VERTEX_BASE_URL=http://localhost:8787` |
| Claude Code via [LiteLLM](https://docs.litellm.ai/) | Supported | Pick *LiteLLM* in the setup wizard and give it the gateway's base URL, then `ANTHROPIC_BASE_URL=http://localhost:8787 ANTHROPIC_AUTH_TOKEN=$LITELLM_KEY claude`. Name the model with `ANTHROPIC_MODEL` if the gateway's model names are not Anthropic's. |
| Anthropic Messages API client | Should work | Set its base URL to `http://localhost:8787` |
| [Codex](https://developers.openai.com/codex) | Tested (WebSocket and HTTPS) | Pick *ChatGPT login* or *OpenAI API key*, then `codex -c 'openai_base_url="http://localhost:8787"'` |
| [OpenCode](https://opencode.ai) | Tested with ChatGPT login; Anthropic and OpenAI-compatible providers supported | Pick the matching setup option, then override that provider's [`baseURL`](https://opencode.ai/docs/providers) with `OPENCODE_CONFIG_CONTENT` — `openai` for Responses, `anthropic` for Messages. For a compatible non-OpenAI provider, point its own `baseURL` at the proxy after selecting *Other* in setup. |
| [Pi](https://pi.dev) | Supported for Anthropic, ChatGPT login, OpenAI Responses, and OpenAI-compatible Chat Completions providers | Pick the matching setup option, then set `providers.<provider>.baseUrl` in `~/.pi/agent/models.json` to `http://localhost:8787` and run `pi --provider <provider>`. |
| OpenAI Responses or Chat Completions client | Should work | Set its base URL to `http://localhost:8787` |
| Several of the above at once | Supported | List every upstream — `UPSTREAMS='anthropic=…,openai=…'`, or a comma-separated answer in the wizard. Each client keeps its own base URL of `http://localhost:8787`; see [Several providers at once](multiple-upstreams.md). |
| Vendor-native OpenCode transports | Not yet | Direct Gemini, Bedrock, and other non-Anthropic/non-OpenAI wire protocols are proxied unchanged but not recorded. |

Codex's persistent Responses WebSocket is proxied bidirectionally. Each `response.create` message is recorded as its own Exchange when the matching terminal response event arrives, so a long-lived socket still produces the same dashboard facts and captures as the HTTPS transport.

Anthropic sessions are grouped by Claude Code's embedded `session_id`. Responses sessions use `prompt_cache_key` first, then a session or thread id in `client_metadata` or `metadata`, then `user`; Chat Completions uses the same fallbacks without the cache key. Auxiliary model calls with no session key are still visible under `(no session id)`.

## Verify your client

This sends one real prompt through your account, so use a small model if you prefer. It saves no client configuration and reads no credentials.

1. Run the setup wizard and pick the option matching how your client authenticates — *ChatGPT login* or *OpenAI API key* for Codex, OpenCode and Pi on OpenAI; *Anthropic API* for Claude Code, OpenCode and Pi on Anthropic; *LiteLLM* for Claude Code pointed at a gateway:

   ```bash
   go run ./cmd/tokentracer setup
   go run ./cmd/tokentracer
   ```

2. In another terminal, launch your client with the line the startup log printed for your backend, and send it one prompt: `Reply with exactly: TokenTracer works`. OpenCode can do it in one non-interactive command — `opencode models openai` lists the models your account can use, and one of them substitutes for `<model>`:

   ```bash
   OPENCODE_CONFIG_CONTENT='{"provider":{"openai":{"options":{"baseURL":"http://localhost:8787"}}}}' \
     opencode run --model openai/<model> 'Reply with exactly: TokenTracer works'
   ```

3. Open [localhost:8787/dashboard](http://localhost:8787/dashboard). Within two seconds, it should show one session containing the test prompt, its token usage, and its API-equivalent cost. Click the session to inspect the request and assembled response.

If no session appears: keep TokenTracer running while you invoke the client, confirm the wizard selected the same authentication method the client uses, and check that the client is actually on the proxied provider — for OpenCode, that the model begins with the provider you overrode, such as `openai/`.

If the dashboard shows `401 Missing scopes: api.responses.write`, the client is using ChatGPT OAuth while TokenTracer is forwarding to the public OpenAI API. Run `go run ./cmd/tokentracer setup`, choose 4 for ChatGPT login/OAuth, then restart both TokenTracer and the client. If `UPSTREAM` is set in your shell, unset or update it first because it overrides `.env`. Choose 5 only for a real OpenAI API key.
