package redact

import (
	"strings"
	"testing"
)

// TestBytesRedactsCredentials pins one case per rule. The `keeps` field is the
// half that matters as much as the redaction: a capture with the field name
// taken out of it cannot answer "which key did this session use".
func TestBytesRedactsCredentials(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		gone  string // must not survive
		want  string // must appear
		keeps string // context that must survive, "" to skip
	}{
		{
			name:  "anthropic key",
			in:    `{"content":"export ANTHROPIC_API_KEY=sk-ant-api03-xY9kLm2nQ7rT4vW8zA1bC3dE5fG6hJ"}`,
			gone:  "sk-ant-api03",
			want:  "[redacted:anthropic-key]",
			keeps: "ANTHROPIC_API_KEY",
		},
		{
			name: "openai key",
			in:   `sk-proj-abcdefghijklmnopqrstuvwxyz0123456789`,
			gone: "abcdefghijklmnop",
			want: "[redacted:openai-key]",
		},
		{
			name: "github token",
			in:   `git remote set-url origin https://ghp_16C7e42F292c6912E7710c838347Ae178B4a@github.com/x/y`,
			gone: "ghp_16C7e42F292c6912E7710c838347Ae178B4a",
			want: "[redacted:github-token]",
			// The host survives: what the token was for is not the secret.
			keeps: "github.com/x/y",
		},
		{
			name: "google api key",
			in:   `key=AIzaSyD-1234567890abcdefghijklmnopqrstuv`,
			gone: "AIzaSyD-1234567890",
			want: "[redacted:google-key]",
		},
		{
			name: "slack token",
			in:   `xoxb-2345678901-abcdefghijklmnop`,
			gone: "xoxb-2345678901",
			want: "[redacted:slack-token]",
		},
		{
			name: "aws access key id",
			in:   `AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE`,
			gone: "AKIAIOSFODNN7EXAMPLE",
			want: "[redacted:",
		},
		{
			name: "aws secret",
			in:   `aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`,
			gone: "wJalrXUtnFEMI",
			want: "[redacted:aws-secret]",
		},
		{
			name:  "jwt",
			in:    `Cookie: session=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dQw4w9WgXcQ`,
			gone:  "eyJzdWIiOiIxMjM0NTY3ODkw",
			want:  "[redacted:jwt]",
			keeps: "Cookie: session=",
		},
		{
			name: "private key block",
			in:   "before\n-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\nx9s0\n-----END RSA PRIVATE KEY-----\nafter",
			gone: "MIIEowIBAAKCAQEA",
			want: "[redacted:private-key]",
			// The lazy match must stop at the first END, not eat the rest of the body.
			keeps: "after",
		},
		{
			name:  "authorization bearer",
			in:    `curl -H "Authorization: Bearer abc123def456ghi789jkl" https://api.example.com`,
			gone:  "abc123def456ghi789jkl",
			want:  "[redacted:credential]",
			keeps: "api.example.com",
		},
		{
			name:  "x-api-key header",
			in:    `x-api-key: 0123456789abcdef0123456789abcdef`,
			gone:  "0123456789abcdef0123456789abcdef",
			want:  "[redacted:credential]",
			keeps: "x-api-key",
		},
		{
			name:  "unknown-format env secret",
			in:    `DATABASE_PASSWORD=hunter2-correct-horse`,
			gone:  "hunter2-correct-horse",
			want:  "[redacted:credential]",
			keeps: "DATABASE_PASSWORD",
		},
		{
			name:  "json credential field",
			in:    `{"model":"claude-sonnet-5","client_secret":"7f3a9b2c8d1e4f5a6b7c"}`,
			gone:  "7f3a9b2c8d1e4f5a6b7c",
			want:  "[redacted:credential]",
			keeps: `"model":"claude-sonnet-5"`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(Bytes([]byte(c.in)))
			if strings.Contains(got, c.gone) {
				t.Errorf("secret survived redaction\n in: %s\nout: %s\nstill contains: %s", c.in, got, c.gone)
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("missing marker %q\n in: %s\nout: %s", c.want, c.in, got)
			}
			if c.keeps != "" && !strings.Contains(got, c.keeps) {
				t.Errorf("redaction ate context %q\n in: %s\nout: %s", c.keeps, c.in, got)
			}
		})
	}
}

// TestBytesLeavesOrdinaryBodiesAlone is the rule that keeps this a scalpel. A
// capture is evidence; a redactor that fires on hashes, ids and prose destroys
// the thing it is protecting. Every string here appears in real Claude Code
// traffic.
func TestBytesLeavesOrdinaryBodiesAlone(t *testing.T) {
	untouched := []string{
		`{"max_tokens":8192,"input_tokens":41,"output_tokens":1200}`,
		`{"name":"Bash","input":{"command":"git log --oneline -5"}}`,
		`commit e4feb7398a1c4d2b9f0e6a5c3d8b7f1e2a4c6d80`,
		`sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08`,
		`{"type":"tool_result","tool_use_id":"toolu_01A09q90qw90lq917835lq9"}`,
		`the token budget for this turn is spent on thinking`,
		`data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==`,
		`{"properties":{"token":{"type":"string","description":"an auth token"}}}`,
	}

	for _, s := range untouched {
		if got := string(Bytes([]byte(s))); got != s {
			t.Errorf("false positive\n in: %s\nout: %s", s, got)
		}
	}
}

func TestBytesEmptyInput(t *testing.T) {
	if got := Bytes(nil); got != nil {
		t.Errorf("Bytes(nil) = %v, want nil", got)
	}
	if got := String(""); got != "" {
		t.Errorf("String(%q) = %q, want %q", "", got, "")
	}
}

// TestStringRedactsLabel is the fact-column case: the label is the first 64
// characters of what the user typed, it is stored on the row, and it outlives
// the capture it came from.
func TestStringRedactsLabel(t *testing.T) {
	got := String("deploy with sk-ant-api03-xY9kLm2nQ7rT4vW8zA1bC3dE5fG6hJ please")
	if strings.Contains(got, "sk-ant-api03") {
		t.Errorf("label kept the key: %s", got)
	}
	if !strings.HasPrefix(got, "deploy with ") || !strings.HasSuffix(got, " please") {
		t.Errorf("label lost its context: %s", got)
	}
}
