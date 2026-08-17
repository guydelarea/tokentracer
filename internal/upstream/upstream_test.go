package upstream

import (
	"net/http"
	"testing"

	"github.com/guydelarea/tokentracer/internal/wire"
)

func table(t *testing.T, spec string) *Table {
	t.Helper()
	routes, err := Parse(spec)
	if err != nil {
		t.Fatalf("Parse(%q): %v", spec, err)
	}
	tbl, err := New(Dedupe(routes))
	if err != nil {
		t.Fatalf("New(%q): %v", spec, err)
	}
	return tbl
}

// One upstream is the shape every install shipped with: everything goes there,
// including paths whose dialect that upstream does not speak. Anything else
// would be a behaviour change for users who never asked for routing.
func TestSingleUpstreamTakesEverything(t *testing.T) {
	tbl := table(t, "https://api.anthropic.com")
	for _, path := range []string{"/v1/messages", "/v1/responses", "/chat/completions", "/anything"} {
		got, rest := tbl.Resolve(path, nil)
		if got.Name != "anthropic" || rest != path {
			t.Errorf("Resolve(%q) = %q %q, want everything on the only route", path, got.Name, rest)
		}
	}
}

// The headline case: Claude Code and OpenCode on one port, each landing on its
// own API with no per-client routing config at all.
func TestDialectRouting(t *testing.T) {
	tbl := table(t, "anthropic=https://api.anthropic.com,openai=https://api.openai.com/v1")

	cases := map[string]string{
		"/v1/messages":           "anthropic",
		"/messages":              "anthropic",
		"/anthropic/v1/messages": "anthropic",
		"/responses":             "openai",
		"/v1/responses":          "openai",
		"/chat/completions":      "openai",
		"/v1/chat/completions":   "openai",
	}
	for path, want := range cases {
		got, rest := tbl.Resolve(path, nil)
		if got.Name != want {
			t.Errorf("Resolve(%q) = %q, want %q", path, got.Name, want)
		}
		if rest != path {
			t.Errorf("Resolve(%q) rewrote the path to %q", path, rest)
		}
	}
}

// Codex and OpenCode send byte-identical /responses to different hosts. Nothing
// in the request can tell them apart, so the prefix does — and it is the only
// thing a user has to configure to run both.
func TestPrefixSettlesTheCollision(t *testing.T) {
	tbl := table(t, "openai=https://api.openai.com/v1,codex=https://chatgpt.com/backend-api/codex")

	if got, rest := tbl.Resolve("/responses", nil); got.Name != "openai" || rest != "/responses" {
		t.Errorf("bare /responses = %q %q, want the first Responses route", got.Name, rest)
	}
	got, rest := tbl.Resolve("/tt/codex/responses", nil)
	if got.Name != "codex" {
		t.Errorf("prefixed route = %q, want codex", got.Name)
	}
	if rest != "/responses" {
		t.Errorf("prefix survived into the upstream path: %q", rest)
	}
}

// A typo'd prefix must not silently become the first route's problem: it falls
// through to ordinary detection, so the client sees the 404 its own API gives
// rather than a request forwarded somewhere it never named.
func TestUnknownPrefixFallsThrough(t *testing.T) {
	tbl := table(t, "anthropic=https://api.anthropic.com,openai=https://api.openai.com/v1")
	got, rest := tbl.Resolve("/tt/typo/v1/messages", nil)
	if got.Name != "anthropic" {
		t.Errorf("Resolve = %q, want detection to still read the dialect", got.Name)
	}
	if rest != "/tt/typo/v1/messages" {
		t.Errorf("rest = %q, want the path left alone when no route matched the prefix", rest)
	}
}

// The ChatGPT backend speaks Responses only. A /chat/completions must not land
// there just because it is the only OpenAI-ish route configured.
func TestChatGPTBackendDoesNotClaimChatCompletions(t *testing.T) {
	tbl := table(t, "codex=https://chatgpt.com/backend-api/codex,gw=http://localhost:4000")
	if got, _ := tbl.Resolve("/chat/completions", nil); got.Name != "gw" {
		t.Errorf("Resolve(/chat/completions) = %q, want the gateway", got.Name)
	}
	if got, _ := tbl.Resolve("/responses", nil); got.Name != "codex" {
		t.Errorf("Resolve(/responses) = %q, want codex", got.Name)
	}
}

// Auxiliary endpoints carry no dialect in the path. The headers a client always
// sends do.
func TestHeaderFallback(t *testing.T) {
	tbl := table(t, "openai=https://api.openai.com/v1,anthropic=https://api.anthropic.com")

	anthropicHdr := http.Header{"Anthropic-Version": {"2023-06-01"}}
	if got, _ := tbl.Resolve("/v1/models", anthropicHdr); got.Name != "anthropic" {
		t.Errorf("/v1/models with anthropic-version = %q, want anthropic", got.Name)
	}
	// With nothing to go on, the first route wins — a guess beats a 500 the user
	// cannot act on.
	if got, _ := tbl.Resolve("/v1/models", nil); got.Name != "openai" {
		t.Errorf("/v1/models bare = %q, want the first route", got.Name)
	}
}

// Vertex serves Claude through Google's host, so it must answer for the
// Anthropic dialect despite the domain.
func TestVertexIsAnAnthropicRoute(t *testing.T) {
	tbl := table(t, "vertex=https://us-east5-aiplatform.googleapis.com/v1,openai=https://api.openai.com/v1")
	path := "/projects/p/locations/us-east5/publishers/anthropic/models/claude-opus-5:streamRawPredict"
	if got, _ := tbl.Resolve(path, nil); got.Name != "vertex" {
		t.Errorf("Resolve(vertex path) = %q, want vertex", got.Name)
	}
}

// Order is the tiebreaker, and it is the user's lever: whichever upstream they
// listed first is the default for the dialects it serves.
func TestOrderDecidesTheDefault(t *testing.T) {
	first := table(t, "a=http://gw-a.test,b=http://gw-b.test")
	if got, _ := first.Resolve("/v1/messages", nil); got.Name != "a" {
		t.Errorf("Resolve = %q, want the first gateway", got.Name)
	}
	second := table(t, "b=http://gw-b.test,a=http://gw-a.test")
	if got, _ := second.Resolve("/v1/messages", nil); got.Name != "b" {
		t.Errorf("Resolve = %q, want the first gateway", got.Name)
	}
}

// A gateway URL says nothing about what it speaks, so the route's name does.
// This is what makes "LiteLLM on the Messages API plus OpenAI" routable at all.
func TestNameDeclaresTheDialectAGatewayURLCannot(t *testing.T) {
	tbl := table(t, "anthropic=http://localhost:4000,openai=http://localhost:4001")

	if got, _ := tbl.Resolve("/v1/messages", nil); got.Name != "anthropic" {
		t.Errorf("Resolve(/v1/messages) = %q, want the route named anthropic", got.Name)
	}
	if got, _ := tbl.Resolve("/chat/completions", nil); got.Name != "openai" {
		t.Errorf("Resolve(/chat/completions) = %q, want the route named openai", got.Name)
	}
	// And an unnamed gateway still answers for everything, as before.
	any := table(t, "gw=http://localhost:4000")
	if got, _ := any.Resolve("/responses", nil); got.Name != "gw" {
		t.Errorf("Resolve = %q, want the unnamed gateway to keep taking everything", got.Name)
	}
}

// Dedupe breaks a name collision with a digit, which must not cost the route
// the dialect its name declared.
func TestDigitSuffixKeepsTheDialect(t *testing.T) {
	tbl := table(t, "openai=https://api.openai.com/v1,anthropic=https://api.anthropic.com,anthropic=http://localhost:4000")
	routes := tbl.Routes()
	if routes[2].Name != "anthropic2" {
		t.Fatalf("names = %+v", routes)
	}
	if !routes[2].Serves(wire.Anthropic) || routes[2].Serves(wire.OpenAI) {
		t.Errorf("anthropic2 lost its dialect")
	}
}

func TestParse(t *testing.T) {
	routes, err := Parse("anthropic=https://api.anthropic.com/, https://api.openai.com/v1 ,gw=localhost:4000")
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ name, url string }{
		{"anthropic", "https://api.anthropic.com"},
		{"openai", "https://api.openai.com/v1"}, // named from its host
		{"gw", "http://localhost:4000"},         // bare host:port gets a scheme
	}
	if len(routes) != len(want) {
		t.Fatalf("got %d routes, want %d", len(routes), len(want))
	}
	for i, w := range want {
		if routes[i].Name != w.name || routes[i].Base() != w.url {
			t.Errorf("route %d = %q %q, want %q %q", i, routes[i].Name, routes[i].Base(), w.name, w.url)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, spec := range []string{"", "   ", "openai=", "name=://nope"} {
		if _, err := Parse(spec); err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", spec)
		}
	}
}

// A URL with a query string is still a URL, not a name=value pair.
func TestParseDoesNotSplitOnAQueryString(t *testing.T) {
	routes, err := Parse("https://gw.test/v1?key=abc")
	if err != nil {
		t.Fatal(err)
	}
	if routes[0].Base() != "https://gw.test/v1?key=abc" {
		t.Errorf("Base() = %q", routes[0].Base())
	}
}

func TestDedupeKeepsTheFirstAndRenamesCollisions(t *testing.T) {
	routes, err := Parse("a=https://one.test,b=https://one.test,a=https://two.test")
	if err != nil {
		t.Fatal(err)
	}
	got := Format(Dedupe(routes))
	if got != "a=https://one.test,a2=https://two.test" {
		t.Errorf("Dedupe = %q", got)
	}
}

func TestNewRejectsDuplicateNames(t *testing.T) {
	routes, err := Parse("a=https://one.test,a=https://two.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(routes); err == nil {
		t.Error("duplicate names were accepted; /tt/a would be ambiguous")
	}
}
