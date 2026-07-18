// Package redact removes credentials from the bodies before they are stored.
//
// A capture is the verbatim request, and a Claude Code request carries whatever
// the session read: a .env file, a pasted curl command, a shell history. The
// facts are numbers and cannot leak; the capture can. So redaction runs on the
// bytes headed for the database and on nothing else — after the facts are
// folded, so no byte count, no prefix hash and no token figure moves.
//
// The patterns are shapes that are only ever secrets. That is deliberate: this
// is a scalpel, not an entropy filter. A capture whose job is fidelity should
// not have git SHAs and base64 images chewed out of it on suspicion.
package redact

import "regexp"

// rule is one credential shape. The replacement names the kind, so a capture
// says what was taken out of it rather than leaving an unexplained hole.
type rule struct {
	re   *regexp.Regexp
	with string
}

// rules run in order, specific before generic. Order is load-bearing: the
// vendor patterns fire first, so by the time the generic assignment rules run,
// a key they would also have matched is already `[redacted:…]` text with no
// secret left in it.
//
// Every rule that matches a *name* (a JSON field, an env var, a header) keeps
// the name in capture group 1 and replaces only the value. Knowing that a
// request set ANTHROPIC_API_KEY is diagnostic; knowing what it set it to is a
// liability.
var rules = []rule{
	// Vendor key shapes — unambiguous, so they are matched whole.
	{regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{16,}`), "[redacted:anthropic-key]"},
	{regexp.MustCompile(`sk-(?:proj-)?[A-Za-z0-9_\-]{20,}`), "[redacted:openai-key]"},
	{regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`), "[redacted:github-token]"},
	{regexp.MustCompile(`AIza[A-Za-z0-9_\-]{35}`), "[redacted:google-key]"},
	{regexp.MustCompile(`xox[baprs]-[A-Za-z0-9\-]{10,}`), "[redacted:slack-token]"},
	{regexp.MustCompile(`(?:AKIA|ASIA)[0-9A-Z]{16}`), "[redacted:aws-key-id]"},

	// A JWT is three base64url segments; the middle one is the claims, which is
	// usually the account it belongs to.
	{regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{6,}`), "[redacted:jwt]"},

	// A key block is multi-line and huge; the lazy `[\s\S]*?` keeps it to one
	// block rather than swallowing everything up to the last END in the body.
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`), "[redacted:private-key]"},

	// Named values, format unknown. These are what catch the credential no list
	// has heard of yet.
	{regexp.MustCompile(`(?i)(aws_secret_access_key\s*[=:]\s*"?)[A-Za-z0-9/+=]{20,}`), "${1}[redacted:aws-secret]"},
	{regexp.MustCompile(`(?i)((?:authorization|proxy-authorization)\s*[:=]\s*"?(?:bearer|basic)\s+)[A-Za-z0-9._\-+/=]{8,}`), "${1}[redacted:credential]"},
	{regexp.MustCompile(`(?i)(x-api-key\s*[:=]\s*"?)[A-Za-z0-9_\-]{16,}`), "${1}[redacted:credential]"},

	// `export ANTHROPIC_API_KEY=…`, `DATABASE_PASSWORD=…`. The name must *end* in
	// a secret word, so MAX_TOKENS=8192 and TOKEN_COUNT=3 are untouched.
	//
	// The value cannot start with `[` — that is what stops these two generic
	// rules from re-redacting a marker a vendor rule already left behind, which
	// would replace `sk-ant-…`'s precise `[redacted:anthropic-key]` with a vague
	// `[redacted:credential]` and lose the one useful thing about the hole.
	{regexp.MustCompile(`(?i)(\b[A-Z0-9_]*(?:API[_-]?KEY|SECRET|TOKEN|PASSWORD|PASSWD)\s*=\s*"?)[^\s"';,\[][^\s"';,]{7,}`), "${1}[redacted:credential]"},

	// The same idea in JSON. The opening quote is part of the match, so
	// "max_tokens" cannot match on its "token" tail.
	{regexp.MustCompile(`(?i)("(?:api[_-]?key|secret|client[_-]?secret|password|passwd|(?:access[_-]?|refresh[_-]?)?token)"\s*:\s*")[^"\[][^"]{3,}`), "${1}[redacted:credential]"},
}

// Bytes returns b with every credential shape replaced. It never returns nil
// for a non-nil input, and returns b itself when there was nothing to take out
// — the common case, and the one worth not copying a megabyte for.
func Bytes(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	for _, r := range rules {
		if r.re.Match(b) {
			b = r.re.ReplaceAll(b, []byte(r.with))
		}
	}
	return b
}

// String is Bytes for the fact columns that carry body text: the label (the
// first 64 characters of what you typed, which is exactly where a pasted key
// lands) and an error message quoting what it choked on. Those outlive the
// capture, so they cannot be left to it.
func String(s string) string {
	if s == "" {
		return s
	}
	return string(Bytes([]byte(s)))
}
