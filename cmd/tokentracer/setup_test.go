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
	if len(anthropic) != 1 || anthropic[0] != "Claude Code: ANTHROPIC_BASE_URL=http://localhost:8787 claude" {
		t.Errorf("anthropic launch lines = %q", anthropic)
	}
	vertex := launchLines(config{Port: "8787", Upstream: "https://us-east5-aiplatform.googleapis.com/v1"})
	if len(vertex) != 1 || vertex[0] != "Claude Code: CLAUDE_CODE_USE_VERTEX=1 ANTHROPIC_VERTEX_BASE_URL=http://localhost:8787 claude" {
		t.Errorf("vertex launch lines = %q", vertex)
	}
	for _, upstream := range []string{"https://api.openai.com/v1", "https://chatgpt.com/backend-api/codex"} {
		lines := launchLines(config{Port: "8787", Upstream: upstream})
		if len(lines) != 2 {
			t.Fatalf("OpenAI launch lines = %q", lines)
		}
		if lines[0] != `Codex: codex -c 'openai_base_url="http://localhost:8787"'` {
			t.Errorf("Codex launch line = %q", lines[0])
		}
		if lines[1] != `OpenCode: OPENCODE_CONFIG_CONTENT='{"provider":{"openai":{"options":{"baseURL":"http://localhost:8787"}}}}' opencode` {
			t.Errorf("OpenCode launch line = %q", lines[1])
		}
	}
}
