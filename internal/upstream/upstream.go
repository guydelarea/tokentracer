// Package upstream is the route table: which API a client request belongs to
// when several clients share one proxy.
//
// TokenTracer started with one upstream because one developer ran one client.
// The moment Claude Code, OpenCode and Pi all point at the same port, the proxy
// has to answer a question it never had to before — Anthropic or OpenAI, and if
// OpenAI, the API-key host or the ChatGPT one. Two of those three are decidable
// from the request itself: the wire dialect is written into the path. The third
// is not, because Codex and OpenCode send byte-identical /responses calls to
// different hosts, so the user settles it with an explicit route prefix.
//
// Nothing here talks to the network. It maps (path, headers) → route, and the
// proxy does the forwarding.
package upstream

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/guydelarea/tokentracer/internal/wire"
)

// Prefix is the escape hatch: http://localhost:8787/tt/<name>/… forces the
// named route regardless of what the path would otherwise imply. It is what
// makes Codex and OpenCode coexist, and it is the only routing rule a user
// ever has to think about.
const Prefix = "/tt/"

// Route is one configured upstream: a name the user picked (or setup picked for
// them) and the base URL every matching client path is appended to.
type Route struct {
	Name string
	URL  *url.URL
}

// Base is the route's URL as configured — what the dashboard and the launch
// lines print.
func (r Route) Base() string {
	if r.URL == nil {
		return ""
	}
	return r.URL.String()
}

// Table is the ordered set of routes. Order is the tiebreaker everywhere: when
// two routes could serve the same request, the earlier one wins, so the user
// controls the default by controlling the order in UPSTREAMS.
type Table struct {
	routes []Route
}

// New builds a table from routes already parsed. It rejects an empty set and
// duplicate names, because both turn a routing bug into a silent misroute.
func New(routes []Route) (*Table, error) {
	if len(routes) == 0 {
		return nil, fmt.Errorf("upstream: no routes configured")
	}
	seen := map[string]bool{}
	for _, r := range routes {
		if r.Name == "" {
			return nil, fmt.Errorf("upstream: route with no name")
		}
		if r.URL == nil || r.URL.Scheme == "" || r.URL.Host == "" {
			return nil, fmt.Errorf("upstream: route %q needs a scheme and host", r.Name)
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("upstream: duplicate route name %q", r.Name)
		}
		seen[r.Name] = true
	}
	return &Table{routes: append([]Route(nil), routes...)}, nil
}

// Routes returns the table in configured order.
func (t *Table) Routes() []Route { return append([]Route(nil), t.routes...) }

// Len is how many upstreams this proxy fronts. One means every legacy
// single-upstream assumption still holds.
func (t *Table) Len() int { return len(t.routes) }

// Resolve picks the route for a client request and returns the path with any
// routing prefix stripped — the path that gets appended to the route's base.
//
// The ladder, in order, and it never returns "no route": a request the proxy
// cannot classify is still a request the user is waiting on, so the last rung
// is the first configured route. Guessing beats a 500 nobody can act on.
func (t *Table) Resolve(path string, h http.Header) (Route, string) {
	if r, rest, ok := t.byPrefix(path); ok {
		return r, rest
	}

	// The dialect is written into the path, and the recorder already knows how to
	// read it — the same function that decides whether an exchange is recordable
	// decides where it goes.
	if r, ok := t.byKind(wire.KindForPath(path)); ok {
		return r, path
	}

	// Auxiliary endpoints — /v1/models, token counting, an OAuth probe — carry no
	// dialect in the path, but the client still identifies itself in the headers
	// it always sends.
	if r, ok := t.byKind(kindForHeaders(h)); ok {
		return r, path
	}
	if r, ok := t.byKind(kindForAuxPath(path)); ok {
		return r, path
	}

	return t.routes[0], path
}

// Named returns a route by name.
func (t *Table) Named(name string) (Route, bool) {
	for _, r := range t.routes {
		if r.Name == name {
			return r, true
		}
	}
	return Route{}, false
}

// Default returns the route Resolve would pick for a dialect, which is what the
// launch lines need: a client whose dialect already routes to its own upstream
// can use the bare base URL, and only a client that would collide needs /tt/.
func (t *Table) Default(k wire.Kind) (Route, bool) { return t.byKind(k) }

// byPrefix handles /tt/<name>/… — an exact route name, or nothing. An unknown
// name deliberately does not match: it falls through to detection rather than
// forwarding a typo to whatever route happens to be first.
func (t *Table) byPrefix(path string) (Route, string, bool) {
	rest, ok := strings.CutPrefix(path, Prefix)
	if !ok {
		return Route{}, "", false
	}
	name, tail, _ := strings.Cut(rest, "/")
	r, found := t.Named(name)
	if !found {
		return Route{}, "", false
	}
	return r, "/" + tail, true
}

func (t *Table) byKind(k wire.Kind) (Route, bool) {
	if k == wire.Unknown {
		return Route{}, false
	}
	for _, r := range t.routes {
		if r.Serves(k) {
			return r, true
		}
	}
	return Route{}, false
}

// Serves reports whether this route can answer a dialect.
//
// Two things decide it, in order: the route's NAME, then its host. The name
// comes first because it is the only place a user can say what a gateway
// speaks — https://api.anthropic.com announces itself, http://localhost:4000
// does not, and calling that route "anthropic" is how the user says LiteLLM is
// mounted on the Messages API. A route named after neither dialect is an
// unknown gateway, which serves whatever it was configured for, so it answers
// yes to everything and takes its position in the table as its priority.
func (r Route) Serves(k wire.Kind) bool {
	if r.URL == nil {
		return false
	}
	switch dialect := r.dialect(); dialect {
	case vendorAnthropic:
		return k == wire.Anthropic
	case vendorOpenAI:
		return k == wire.OpenAI || k == wire.OpenAIChat
	case vendorChatGPT:
		// The ChatGPT backend speaks Responses and nothing else; a
		// /chat/completions sent there is a 404 waiting to happen.
		return k == wire.OpenAI
	default:
		return true
	}
}

// Serves is the same question about a bare URL, for callers that have not built
// a Route yet.
func Serves(u *url.URL, k wire.Kind) bool { return Route{Name: NameFor(u), URL: u}.Serves(k) }

// Dialect names what this route speaks, as a word callers can switch on:
// "anthropic", "openai", "codex", or "" for a gateway that declared nothing and
// therefore answers for anything.
func (r Route) Dialect() string {
	switch r.dialect() {
	case vendorAnthropic:
		return "anthropic"
	case vendorOpenAI:
		return "openai"
	case vendorChatGPT:
		return "codex"
	}
	return ""
}

func (r Route) dialect() vendorID {
	if v := vendorForName(r.Name); v != vendorUnknown {
		return v
	}
	return vendorForHost(r.URL)
}

type vendorID int

const (
	vendorUnknown vendorID = iota
	vendorAnthropic
	vendorOpenAI
	vendorChatGPT
)

// vendorForName reads the dialect off the route's name. Dedupe can append a
// digit to break a collision, so the digits come off before the comparison —
// "anthropic2" is still an Anthropic route.
func vendorForName(name string) vendorID {
	base := strings.TrimRight(strings.ToLower(name), "0123456789")
	switch base {
	case "anthropic", "claude", "vertex":
		return vendorAnthropic
	case "openai", "openai-chat", "chat":
		return vendorOpenAI
	case "codex", "chatgpt":
		return vendorChatGPT
	}
	return vendorUnknown
}

func vendorForHost(u *url.URL) vendorID {
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "api.anthropic.com":
		return vendorAnthropic
	// Vertex serves Claude through Google's own host, so the vendor is the
	// dialect, not the domain.
	case strings.HasSuffix(host, "aiplatform.googleapis.com"):
		return vendorAnthropic
	case host == "api.openai.com":
		return vendorOpenAI
	case host == "chatgpt.com" || strings.HasSuffix(host, ".chatgpt.com"):
		return vendorChatGPT
	default:
		return vendorUnknown
	}
}

// kindForHeaders reads the dialect off headers every client of that dialect
// sends on every request, auxiliary endpoints included. anthropic-version is
// required by the Messages API; OpenAI's own beta/org headers are the mirror
// image. A bare Authorization: Bearer says nothing — both use it.
func kindForHeaders(h http.Header) wire.Kind {
	if h == nil {
		return wire.Unknown
	}
	if h.Get("anthropic-version") != "" || h.Get("x-api-key") != "" || h.Get("anthropic-beta") != "" {
		return wire.Anthropic
	}
	if h.Get("OpenAI-Beta") != "" || h.Get("OpenAI-Organization") != "" || h.Get("chatgpt-account-id") != "" {
		return wire.OpenAI
	}
	return wire.Unknown
}

// kindForAuxPath is the last hint before the fallback: the non-billed endpoints
// each dialect owns outright. /v1/models is missing on purpose — both APIs
// serve it, so it identifies nobody.
func kindForAuxPath(path string) wire.Kind {
	p := strings.TrimSuffix(path, "/")
	switch {
	case strings.HasSuffix(p, "/count_tokens"), strings.HasSuffix(p, "/complete"),
		strings.Contains(p, ":streamRawPredict"), strings.Contains(p, ":rawPredict"):
		return wire.Anthropic
	case strings.HasSuffix(p, "/completions"), strings.HasSuffix(p, "/embeddings"),
		strings.Contains(p, "/backend-api/"), strings.HasSuffix(p, "/conversation"):
		return wire.OpenAI
	}
	return wire.Unknown
}
