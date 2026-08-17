package upstream

import (
	"fmt"
	"net/url"
	"strings"
)

// Parse reads the UPSTREAMS config: a comma-separated list of name=url pairs,
// in priority order.
//
//	anthropic=https://api.anthropic.com,openai=https://api.openai.com/v1
//
// A bare URL with no name gets one from its host, so the single-upstream config
// TokenTracer shipped with parses unchanged and keeps working.
func Parse(spec string) ([]Route, error) {
	var out []Route
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, raw := splitPair(part)
		u, err := parseBase(raw)
		if err != nil {
			return nil, fmt.Errorf("upstream %q: %w", part, err)
		}
		if name == "" {
			name = NameFor(u)
		}
		out = append(out, Route{Name: name, URL: u})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("upstream: %q named no upstreams", spec)
	}
	return out, nil
}

// splitPair separates name=url. The split is on the FIRST '=', and only when
// what precedes it looks like a name rather than a scheme — "https://…" has no
// '=' before its scheme, but a bare "localhost:4000/x?a=b" could, and that is
// a URL, not a pair.
func splitPair(part string) (name, raw string) {
	eq := strings.Index(part, "=")
	if eq < 0 {
		return "", part
	}
	head := part[:eq]
	if strings.ContainsAny(head, ":/") {
		return "", part
	}
	return strings.TrimSpace(head), strings.TrimSpace(part[eq+1:])
}

// parseBase normalizes a base URL the way the setup wizard already does for a
// pasted gateway: a bare host:port gets the http scheme a local gateway is
// served on, and the trailing slash goes, since every client path is appended
// to this one.
func parseBase(raw string) (*url.URL, error) {
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "/")
	if raw == "" {
		return nil, fmt.Errorf("empty URL")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("needs a scheme and host")
	}
	return u, nil
}

// NameFor is the short label a URL implies — what an unnamed upstream is called
// on the dashboard, in the launch lines, and in the /tt/ prefix. The four hosts
// setup offers get stable names; anything else is named after its host, so two
// gateways never collide.
func NameFor(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "api.anthropic.com":
		return "anthropic"
	case strings.HasSuffix(host, "aiplatform.googleapis.com"):
		return "vertex"
	case host == "api.openai.com":
		return "openai"
	case host == "chatgpt.com" || strings.HasSuffix(host, ".chatgpt.com"):
		return "codex"
	}
	return sanitize(host)
}

// sanitize keeps a name usable as a URL path segment, since it is one.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		return "upstream"
	}
	return name
}

// Format renders a table back into the UPSTREAMS spec, which is what setup
// writes to .env.
func Format(routes []Route) string {
	parts := make([]string, 0, len(routes))
	for _, r := range routes {
		parts = append(parts, r.Name+"="+r.Base())
	}
	return strings.Join(parts, ",")
}

// Dedupe drops routes whose base URL was already claimed, keeping the first,
// and renames a collision rather than rejecting it — setup can offer the same
// upstream twice and the user should not have to care.
func Dedupe(routes []Route) []Route {
	seenURL := map[string]bool{}
	seenName := map[string]bool{}
	out := make([]Route, 0, len(routes))
	for _, r := range routes {
		base := r.Base()
		if seenURL[base] {
			continue
		}
		seenURL[base] = true
		name := r.Name
		for i := 2; seenName[name]; i++ {
			name = fmt.Sprintf("%s%d", r.Name, i)
		}
		seenName[name] = true
		out = append(out, Route{Name: name, URL: r.URL})
	}
	return out
}
