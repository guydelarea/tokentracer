package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guydelarea/tokentracer/internal/upstream"
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
	if err := writeEnvFile(path, map[string]string{"UPSTREAMS": "anthropic=https://example.test/v1"}); err != nil {
		t.Fatal(err)
	}

	t.Setenv("UPSTREAMS", "")
	os.Unsetenv("UPSTREAMS")
	loadEnvFile(path)
	if got := os.Getenv("UPSTREAMS"); got != "anthropic=https://example.test/v1" {
		t.Errorf("UPSTREAMS from file = %q", got)
	}

	t.Setenv("UPSTREAMS", "anthropic=https://shell-wins.test")
	loadEnvFile(path)
	if got := os.Getenv("UPSTREAMS"); got != "anthropic=https://shell-wins.test" {
		t.Errorf("UPSTREAMS = %q, want the shell's value to survive the file", got)
	}
}

// The single-upstream config every install shipped with still loads, and still
// means "everything goes here".
func TestLoadConfigHonoursLegacyUpstream(t *testing.T) {
	t.Setenv("UPSTREAMS", "")
	os.Unsetenv("UPSTREAMS")
	t.Setenv("UPSTREAM", "https://api.anthropic.com")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Routes) != 1 || cfg.Upstream() != "https://api.anthropic.com" || cfg.Routes[0].Name != "anthropic" {
		t.Errorf("legacy UPSTREAM did not become one named route: %+v", cfg.Routes)
	}
}

// UPSTREAMS outranks UPSTREAM, so a re-run of setup is not silently ignored
// because an old key is still sitting in .env.
func TestLoadConfigPrefersUpstreams(t *testing.T) {
	t.Setenv("UPSTREAM", "https://api.anthropic.com")
	t.Setenv("UPSTREAMS", "openai=https://api.openai.com/v1,codex=https://chatgpt.com/backend-api/codex")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Routes) != 2 || cfg.Routes[0].Name != "openai" || cfg.Routes[1].Name != "codex" {
		t.Fatalf("routes = %+v", cfg.Routes)
	}
	if cfg.Upstream() != "https://api.openai.com/v1" {
		t.Errorf("Upstream() = %q, want the first route", cfg.Upstream())
	}
}

// …but a shell-set UPSTREAM still outranks a saved UPSTREAMS, because a
// variable exported in front of the command is a deliberate override of the
// wizard's answer, and the docs promise the shell wins over .env.
func TestShellUpstreamOutranksASavedList(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := writeEnvFile(path, map[string]string{"UPSTREAMS": "openai=https://api.openai.com/v1"}); err != nil {
		t.Fatal(err)
	}

	t.Setenv("UPSTREAMS", "")
	os.Unsetenv("UPSTREAMS")
	t.Setenv("UPSTREAM", "https://api.anthropic.com")

	// the same three steps main() takes, in the same order
	shellUpstream := shellOnlyUpstream()
	loadEnvFile(path)
	if shellUpstream {
		os.Unsetenv("UPSTREAMS")
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Routes) != 1 || cfg.Upstream() != "https://api.anthropic.com" {
		t.Errorf("routes = %+v, want the shell's single upstream", cfg.Routes)
	}
}

func TestLoadConfigRejectsAMalformedList(t *testing.T) {
	t.Setenv("UPSTREAMS", "openai=")
	if _, err := loadConfig(); err == nil {
		t.Error("a route with no URL was accepted; half a route table is worse than none")
	}
}

func TestLaunchLines(t *testing.T) {
	anthropic := launchLines(config{Port: "8787", Routes: mustRoutes(t, "https://api.anthropic.com")})
	if len(anthropic) != 3 ||
		anthropic[0] != "Claude Code: ANTHROPIC_BASE_URL=http://localhost:8787 claude" ||
		anthropic[1] != `OpenCode: OPENCODE_CONFIG_CONTENT='{"provider":{"anthropic":{"options":{"baseURL":"http://localhost:8787"}}}}' opencode` ||
		anthropic[2] != `Pi: set ~/.pi/agent/models.json providers.anthropic.baseUrl="http://localhost:8787", then run pi --provider anthropic` {
		t.Errorf("anthropic launch lines = %q", anthropic)
	}
	vertex := launchLines(config{Port: "8787", Routes: mustRoutes(t, "https://us-east5-aiplatform.googleapis.com/v1")})
	if len(vertex) != 1 || vertex[0] != "Claude Code: CLAUDE_CODE_USE_VERTEX=1 ANTHROPIC_VERTEX_BASE_URL=http://localhost:8787 claude" {
		t.Errorf("vertex launch lines = %q", vertex)
	}
	for base, provider := range map[string]string{"https://api.openai.com/v1": "openai", "https://chatgpt.com/backend-api/codex": "openai-codex"} {
		lines := launchLines(config{Port: "8787", Routes: mustRoutes(t, base)})
		if len(lines) != 3 {
			t.Fatalf("OpenAI launch lines = %q", lines)
		}
		auth := "OpenAI API key"
		if provider == "openai-codex" {
			auth = "ChatGPT login/OAuth"
		}
		if lines[0] != `Codex (`+auth+`): codex -c 'openai_base_url="http://localhost:8787"'` {
			t.Errorf("Codex launch line = %q", lines[0])
		}
		if lines[1] != `OpenCode (`+auth+`): OPENCODE_CONFIG_CONTENT='{"provider":{"openai":{"options":{"baseURL":"http://localhost:8787"}}}}' opencode` {
			t.Errorf("OpenCode launch line = %q", lines[1])
		}
		wantPi := `Pi: set ~/.pi/agent/models.json providers.` + provider + `.baseUrl="http://localhost:8787", then run pi --provider ` + provider
		if lines[2] != wantPi {
			t.Errorf("Pi launch line = %q", lines[2])
		}
	}
	// A gateway serves every dialect it was configured for on one base URL, so
	// every client that could be pointed at it gets a line.
	gateway := launchLines(config{Port: "8787", Routes: mustRoutes(t, "http://localhost:4000")})
	if len(gateway) != 3 ||
		gateway[0] != "Claude Code: ANTHROPIC_BASE_URL=http://localhost:8787 ANTHROPIC_AUTH_TOKEN=<gateway key> claude" ||
		gateway[1] != `Codex: codex -c 'openai_base_url="http://localhost:8787"'` ||
		gateway[2] != "OpenCode/Pi: set the selected provider's base URL to http://localhost:8787" {
		t.Errorf("gateway launch lines = %q", gateway)
	}
}

// The mixed case, which is the whole point: Claude Code and OpenCode reach
// their own upstreams on the bare URL, and only Codex — which would collide
// with OpenCode's Responses traffic — is told to use a prefix.
func TestLaunchLinesForSeveralUpstreams(t *testing.T) {
	lines := launchLines(config{Port: "8787", Routes: mustRoutes(t,
		"anthropic=https://api.anthropic.com,openai=https://api.openai.com/v1,codex=https://chatgpt.com/backend-api/codex")})
	joined := strings.Join(lines, "\n")

	for _, want := range []string{
		"[anthropic → https://api.anthropic.com]",
		"Claude Code: ANTHROPIC_BASE_URL=http://localhost:8787 claude",
		`OpenCode (OpenAI API key): OPENCODE_CONFIG_CONTENT='{"provider":{"openai":{"options":{"baseURL":"http://localhost:8787"}}}}' opencode`,
		`Codex (ChatGPT login/OAuth): codex -c 'openai_base_url="http://localhost:8787/tt/codex"'`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("launch lines missing %q:\n%s", want, joined)
		}
	}
	// The Anthropic block must not claim the prefix — it is the only Anthropic
	// route, so detection already sends it there.
	if strings.Contains(joined, "http://localhost:8787/tt/anthropic") {
		t.Errorf("anthropic was given a prefix it does not need:\n%s", joined)
	}
}

func mustRoutes(t *testing.T, spec string) []upstream.Route {
	t.Helper()
	routes, err := upstream.Parse(spec)
	if err != nil {
		t.Fatalf("mustRoutes(%q): %v", spec, err)
	}
	return upstream.Dedupe(routes)
}

func TestAuthNotice(t *testing.T) {
	cases := map[string]string{
		"https://chatgpt.com/backend-api/codex":  "ChatGPT login/OAuth required; do not use an OpenAI API key",
		"https://api.openai.com/v1":              `OpenAI API key required; ChatGPT OAuth will fail with "Missing scopes: api.responses.write"`,
		"https://API.OPENAI.COM/v1":              `OpenAI API key required; ChatGPT OAuth will fail with "Missing scopes: api.responses.write"`,
		"https://api.anthropic.com":              "",
		"https://gateway.example/api.openai.com": "",
	}
	for upstreamURL, want := range cases {
		if got := authNotice(upstreamURL); got != want {
			t.Errorf("authNotice(%q) = %q, want %q", upstreamURL, got, want)
		}
	}
}

// With several upstreams, "the upstream" no longer identifies which one a
// warning is about, so each notice is named. With one, the name would be noise.
func TestAuthNoticesName_TheUpstreamOnlyWhenThereAreSeveral(t *testing.T) {
	one := authNotices(mustRoutes(t, "https://api.openai.com/v1"))
	if len(one) != 1 || strings.HasPrefix(one[0], "openai — ") {
		t.Errorf("single-upstream notices = %q, want the bare warning", one)
	}

	several := authNotices(mustRoutes(t,
		"anthropic=https://api.anthropic.com,openai=https://api.openai.com/v1,codex=https://chatgpt.com/backend-api/codex"))
	if len(several) != 2 {
		t.Fatalf("notices = %q, want one per OpenAI-host route", several)
	}
	if !strings.HasPrefix(several[0], "openai — ") || !strings.HasPrefix(several[1], "codex — ") {
		t.Errorf("notices = %q, want each named", several)
	}
}

// The wizard's answers, without a terminal. One choice is one upstream; a list
// of choices is a list of upstreams, in the order given.
func TestChooseUpstreams(t *testing.T) {
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
		"default":            {[]string{""}, "anthropic=https://api.anthropic.com"},
		"vertex":             {[]string{"2", "us-east5"}, "vertex=https://us-east5-aiplatform.googleapis.com/v1"},
		"litellm default":    {[]string{"3", ""}, "anthropic=" + liteLLMDefault},
		"litellm pasted":     {[]string{"3", "http://gateway.internal:4000/"}, "anthropic=http://gateway.internal:4000"},
		"litellm bare host":  {[]string{"3", "gateway.internal:4000"}, "anthropic=http://gateway.internal:4000"},
		"chatgpt login":      {[]string{"4"}, "codex=https://chatgpt.com/backend-api/codex"},
		"openai api key":     {[]string{"5"}, "openai=https://api.openai.com/v1"},
		"other pasted":       {[]string{"6", "https://openrouter.ai/api/v1"}, "openrouter-ai=https://openrouter.ai/api/v1"},
		"other left blank":   {[]string{"6", ""}, "anthropic=https://api.anthropic.com"},
		"unknown choice":     {[]string{"nine"}, "anthropic=https://api.anthropic.com"},
		"litellm https kept": {[]string{"3", "https://gateway.example/llm"}, "anthropic=https://gateway.example/llm"},

		// The multi-client answers, which are the reason the prompt takes a list.
		"claude code and opencode": {
			[]string{"1,5"},
			"anthropic=https://api.anthropic.com,openai=https://api.openai.com/v1",
		},
		"space separated": {
			[]string{"5 4"},
			"openai=https://api.openai.com/v1,codex=https://chatgpt.com/backend-api/codex",
		},
		// The same upstream twice is the user being imprecise, not an error.
		"duplicates collapse": {[]string{"1,1"}, "anthropic=https://api.anthropic.com"},
	}
	for name, tc := range cases {
		if got := upstream.Format(chooseUpstreams(answers(tc.replies...))); got != tc.want {
			t.Errorf("%s: chooseUpstreams(%q) = %q, want %q", name, tc.replies, got, tc.want)
		}
	}
}
