package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVertexUpstream(t *testing.T) {
	cases := map[string]string{
		"":         "https://aiplatform.googleapis.com/v1",
		"global":   "https://aiplatform.googleapis.com/v1",
		"us-east5": "https://us-east5-aiplatform.googleapis.com/v1",
	}
	for region, want := range cases {
		if got := vertexUpstream(region); got != want {
			t.Errorf("vertexUpstream(%q) = %q, want %q", region, got, want)
		}
	}
}

// The wizard's answer round-trips through the file, and the shell env outranks it.
func TestEnvFileRoundTripAndPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := writeEnvFile(path, map[string]string{"UPSTREAM": "https://example.test/v1"}); err != nil {
		t.Fatal(err)
	}

	t.Setenv("UPSTREAM", "")
	os.Unsetenv("UPSTREAM")
	loadEnvFile(path)
	if got := os.Getenv("UPSTREAM"); got != "https://example.test/v1" {
		t.Errorf("UPSTREAM from file = %q", got)
	}

	t.Setenv("UPSTREAM", "https://shell-wins.test")
	loadEnvFile(path)
	if got := os.Getenv("UPSTREAM"); got != "https://shell-wins.test" {
		t.Errorf("UPSTREAM = %q, want the shell's value to survive the file", got)
	}
}

func TestLaunchLines(t *testing.T) {
	anthropic := launchLines(config{Port: "8787", Upstream: "https://api.anthropic.com"})
	if len(anthropic) != 3 ||
		anthropic[0] != "Claude Code: ANTHROPIC_BASE_URL=http://localhost:8787 claude" ||
		anthropic[1] != `OpenCode: OPENCODE_CONFIG_CONTENT='{"provider":{"anthropic":{"options":{"baseURL":"http://localhost:8787"}}}}' opencode` ||
		anthropic[2] != `Pi: set ~/.pi/agent/models.json providers.anthropic.baseUrl="http://localhost:8787", then run pi --provider anthropic` {
		t.Errorf("anthropic launch lines = %q", anthropic)
	}
	vertex := launchLines(config{Port: "8787", Upstream: "https://us-east5-aiplatform.googleapis.com/v1"})
	if len(vertex) != 1 || vertex[0] != "Claude Code: CLAUDE_CODE_USE_VERTEX=1 ANTHROPIC_VERTEX_BASE_URL=http://localhost:8787 claude" {
		t.Errorf("vertex launch lines = %q", vertex)
	}
	for upstream, provider := range map[string]string{"https://api.openai.com/v1": "openai", "https://chatgpt.com/backend-api/codex": "openai-codex"} {
		lines := launchLines(config{Port: "8787", Upstream: upstream})
		if len(lines) != 3 {
			t.Fatalf("OpenAI launch lines = %q", lines)
		}
		if lines[0] != `Codex: codex -c 'openai_base_url="http://localhost:8787"'` {
			t.Errorf("Codex launch line = %q", lines[0])
		}
		if lines[1] != `OpenCode: OPENCODE_CONFIG_CONTENT='{"provider":{"openai":{"options":{"baseURL":"http://localhost:8787"}}}}' opencode` {
			t.Errorf("OpenCode launch line = %q", lines[1])
		}
		wantPi := `Pi: set ~/.pi/agent/models.json providers.` + provider + `.baseUrl="http://localhost:8787", then run pi --provider ` + provider
		if lines[2] != wantPi {
			t.Errorf("Pi launch line = %q", lines[2])
		}
	}
	// A gateway serves every dialect it was configured for on one base URL, so
	// every client that could be pointed at it gets a line.
	gateway := launchLines(config{Port: "8787", Upstream: "http://localhost:4000"})
	if len(gateway) != 3 ||
		gateway[0] != "Claude Code: ANTHROPIC_BASE_URL=http://localhost:8787 ANTHROPIC_AUTH_TOKEN=<gateway key> claude" ||
		gateway[1] != `Codex: codex -c 'openai_base_url="http://localhost:8787"'` ||
		gateway[2] != "OpenCode/Pi: set the selected provider's base URL to http://localhost:8787" {
		t.Errorf("gateway launch lines = %q", gateway)
	}
}

// The wizard's answers, without a terminal: each choice implies one upstream.
func TestChooseUpstream(t *testing.T) {
	answers := func(replies ...string) func(string) string {
		i := 0
		return func(string) string {
			if i >= len(replies) {
				return ""
			}
			reply := replies[i]
			i++
			return reply
		}
	}
	cases := map[string]struct {
		replies []string
		want    string
	}{
		"default":            {[]string{""}, "https://api.anthropic.com"},
		"vertex":             {[]string{"2", "us-east5"}, "https://us-east5-aiplatform.googleapis.com/v1"},
		"litellm default":    {[]string{"3", ""}, liteLLMDefault},
		"litellm pasted":     {[]string{"3", "http://gateway.internal:4000/"}, "http://gateway.internal:4000"},
		"litellm bare host":  {[]string{"3", "gateway.internal:4000"}, "http://gateway.internal:4000"},
		"chatgpt login":      {[]string{"4"}, "https://chatgpt.com/backend-api/codex"},
		"openai api key":     {[]string{"5"}, "https://api.openai.com/v1"},
		"other pasted":       {[]string{"6", "https://openrouter.ai/api/v1"}, "https://openrouter.ai/api/v1"},
		"other left blank":   {[]string{"6", ""}, "https://api.anthropic.com"},
		"unknown choice":     {[]string{"nine"}, "https://api.anthropic.com"},
		"litellm https kept": {[]string{"3", "https://gateway.example/llm"}, "https://gateway.example/llm"},
	}
	for name, tc := range cases {
		if got := chooseUpstream(answers(tc.replies...)); got != tc.want {
			t.Errorf("%s: chooseUpstream(%q) = %q, want %q", name, tc.replies, got, tc.want)
		}
	}
}
