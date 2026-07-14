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

func TestLaunchLine(t *testing.T) {
	anthropic := launchLine(config{Port: "8787", Upstream: "https://api.anthropic.com"})
	if anthropic != "ANTHROPIC_BASE_URL=http://localhost:8787 claude" {
		t.Errorf("anthropic launch line = %q", anthropic)
	}
	vertex := launchLine(config{Port: "8787", Upstream: "https://us-east5-aiplatform.googleapis.com/v1"})
	if vertex != "CLAUDE_CODE_USE_VERTEX=1 ANTHROPIC_VERTEX_BASE_URL=http://localhost:8787 claude" {
		t.Errorf("vertex launch line = %q", vertex)
	}
}
