// Package billing prices token usage at read time.
//
// Facts, not interpretations: the recorder stores usage verbatim as the API
// reported it, and nothing in this package is ever persisted. Cost is derived
// on the way out, which is what lets the rate table be corrected after the
// fact without rewriting history.
package billing

import (
	"strings"
	"time"
)

// Cache multipliers ride on the input base rate. Anthropic and OpenAI both bill
// reads at 0.1x and writes at 1.25x; Anthropic's 1-hour writes bill at 2.0x.
// These hold for every model currently represented in Rates.
const (
	ReadMult    = 0.1
	Write5mMult = 1.25
	Write1hMult = 2.0
)

// CacheTTL is how long a cached prefix survives with no traffic on it. An idle
// gap longer than this re-writes the whole prefix even though not one byte
// changed — a cache break the prefix hashes cannot explain, and the only one
// that is nobody's fault. The trace has to know the number to say so, and it
// belongs here with the multipliers rather than as a literal in the browser.
const CacheTTL = 5 * time.Minute

// CacheTTLFor is the minimum lifetime whose expiry can explain an otherwise
// byte-identical cache rewrite. GPT-5.6 defaults to a 30-minute prompt-cache
// TTL; current Claude cache-control blocks default to five minutes.
func CacheTTLFor(model string) time.Duration {
	if strings.Contains(normalize(model), "gpt-5.6") {
		return 30 * time.Minute
	}
	return CacheTTL
}

// What a cache break actually costs, as a share of the bill it produced.
//
// Not fudge factors: a prefix that had hit would have billed at ReadMult, so the
// *extra* a break costs is everything above that. Both fall out of the
// multipliers above, which is why neither is written as a decimal.
const (
	RebillWriteShare = 1 - ReadMult/Write5mMult // a re-written prefix vs. one that hit
	RebillFreshShare = 1 - ReadMult             // input billed fresh vs. read from cache
)

// ContextWindow is how many tokens the model will hold — the denominator of the
// trace's "% of window" gauge, and the number "compact now" is advice on the
// strength of.
//
// A window is a fact about the model, so it is keyword-matched off the id here
// like a price, not written into the page: the page cannot know which model a
// session ran on, and scoring a 1M-context session against 200k would show it as
// five times as full as it is.
//
// ponytail: a few sizes, first match wins. Add a row when a tier ships another.
func ContextWindow(model string) int64 {
	normalized := normalize(model)
	if strings.Contains(normalized, "gpt-5.6") {
		return 1_050_000
	}
	// Claude Code spells the 1M window as a "[1m]" suffix on the model id
	// (proxy.go's Vertex path shows "claude-opus-5[1m]" on the wire). Every model
	// seen carrying it is already in millionTokenModels, so this is belt and
	// braces, not the only signal — kept because the suffix is unambiguous and
	// costs nothing, not because some 200k model opts in through it. None does:
	// where 1M exists it is the default and needs no beta header.
	if strings.Contains(normalized, "[1m]") {
		return 1_000_000
	}
	for _, key := range millionTokenModels {
		if strings.Contains(normalized, key) {
			return 1_000_000
		}
	}
	return 200_000
}

// millionTokenModels are the Claude models that hold 1M tokens natively. From
// Claude 4.6 on, the full window is the default AND the maximum, billed at
// standard rates — so the bare id is already 1M and there is no "[1m]" to key
// off. Reading one of these as 200k is not a rounding error: it draws a session
// as five times as full as it is, and puts "compact now" on the screen at 40%.
//
// Matched as a substring, like a rate key, so route prefixes and the Bedrock and
// Vertex spellings resolve off the same row. Deliberately NOT a bare "opus"/
// "sonnet" family match: an older 200k sibling would inherit 1M and the gauge
// would under-read instead, which is the same lie pointing the other way.
var millionTokenModels = []string{
	"claude-opus-4-6",
	"claude-opus-4-7",
	"claude-opus-4-8",
	"claude-opus-5",
	"claude-sonnet-4-6",
	"claude-sonnet-5",
	"claude-fable-5",
	"claude-mythos-5",
	"claude-mythos-preview",
}

// ReadPerTok is what one token costs to re-read out of the cached prefix — the
// price of a schema that ships on every request and is never called. Zero when
// the model has no rate, which is how an unpriced session shows no waste rather
// than a confident $0.00.
func ReadPerTok(rates []Rate, model string, at time.Time) float64 {
	r, ok := match(rates, normalize(model), at)
	if !ok {
		return 0
	}
	return r.InPerM / 1e6 * ReadMult
}

// EstTokens is bytes → tokens, the standard 4:1 approximation. Used only where
// the API never gives a real count: the size of an individual tool schema inside
// a prefix the API prices as one lump.
func EstTokens(bytes int64) int64 { return (bytes + 2) / 4 }

// Rate is one price line: a model key, its per-million-token rates, and the
// window in which they applied.
type Rate struct {
	// Key is matched against the normalized model name — as a substring, unless
	// Exact says otherwise.
	Key string

	// Exact narrows Key to whole-name matching. It is what lets a fetched price
	// list be merged in without importing its bare family keys as substrings: the
	// registry publishes a "gpt-4" row, and as a substring key that would price
	// an unreleased "gpt-4-whatever" at gpt-4's rate instead of leaving it
	// UNPRICED — the silent wrong number rates.go exists to prevent.
	//
	// Hand-written rows stay substring-matched, which is what makes one key cover
	// the Bedrock and Vertex spellings of the same model. Nothing in rates.go
	// sets this; billing.Merge sets it on every row it takes from a fetch.
	Exact bool

	// InPerM and OutPerM are USD per 1M tokens on the standard tier.
	InPerM  float64
	OutPerM float64

	// From and Until bound the window in which this rate applied, [From, Until):
	// From is inclusive, Until is exclusive. A zero From is valid from the past;
	// a zero Until is open-ended.
	From  time.Time
	Until time.Time

	// LongCtxThreshold is the total input token count above which the premium
	// long-context rates apply. Zero means the model has no long-context tier.
	LongCtxThreshold int64
	LongCtxInPerM    float64
	LongCtxOutPerM   float64
}

// Usage is the normalized token quartet reported by the upstream: fresh input,
// cache reads, cache writes split by TTL, and output.
type Usage struct {
	In      int64
	Read    int64
	Write5m int64
	Write1h int64
	Out     int64
}

// Bill is the cost of a single request in USD, split into the four components
// the dashboard stacks. Write combines the 5-minute and 1-hour cache writes.
//
// Priced is false when no rate matched the model. Callers must surface that
// rather than treating Total as a real zero — an unpriced request and a free
// one are indistinguishable once they reach an aggregate.
type Bill struct {
	Priced bool
	In     float64
	Read   float64
	Write  float64
	Out    float64
	Total  float64
}

// Compute prices usage against rates, as of the instant at. Callers pass Rates.
func Compute(rates []Rate, model string, u Usage, at time.Time) Bill {
	r, ok := match(rates, normalize(model), at)
	if !ok {
		return Bill{Priced: false}
	}

	in, out := r.InPerM, r.OutPerM

	// The long-context premium applies strictly ABOVE the threshold, not at it:
	// A request landing exactly on a published boundary is still standard. Every
	// input component counts toward the threshold, cache reads included.
	if r.LongCtxThreshold > 0 && u.In+u.Read+u.Write5m+u.Write1h > r.LongCtxThreshold {
		in, out = r.LongCtxInPerM, r.LongCtxOutPerM
	}

	b := Bill{
		Priced: true,
		In:     cost(u.In, in),
		Read:   cost(u.Read, in*ReadMult),
		Write:  cost(u.Write5m, in*Write5mMult) + cost(u.Write1h, in*Write1hMult),
		Out:    cost(u.Out, out),
	}
	b.Total = b.In + b.Read + b.Write + b.Out
	return b
}

// match returns the first rate whose key is a substring of model and whose
// window contains at. First hit wins, so Rates is ordered most-specific-first.
func match(rates []Rate, model string, at time.Time) (Rate, bool) {
	for _, r := range rates {
		if !r.matches(model) {
			continue
		}
		// A zero From is the zero time, which precedes any real timestamp.
		if at.Before(r.From) {
			continue
		}
		if !r.Until.IsZero() && !at.Before(r.Until) {
			continue
		}
		return r, true
	}
	return Rate{}, false
}

// matches reports whether r's key covers this already-normalized model name.
//
// A substring key is the hand-written default and the reason one row prices a
// model however a gateway spelled it. An Exact key is the whole name, or the
// segment after the last '/': a gateway puts its route in front of the model
// ("anthropic/claude-sonnet-5"), and that prefix reaches the row whenever the
// response never named the model it actually served.
func (r Rate) matches(model string) bool {
	if !r.Exact {
		return strings.Contains(model, r.Key)
	}
	if model == r.Key {
		return true
	}
	if i := strings.LastIndex(model, "/"); i >= 0 {
		return model[i+1:] == r.Key
	}
	return false
}

// normalize rewrites a wire model name into the spelling the rate keys use, so
// that one key matches the same model however the gateway wrote it:
//
//	anthropic.claude-sonnet-4@20250514 -> claude-sonnet-4-20250514  (Vertex)
//	anthropic.claude-opus-4-5-v1:0     -> claude-opus-4-5           (Bedrock)
//
// Vertex separates the snapshot date with '@' where the first-party id uses '-'.
// Cutting the name at the '@' stranded every model whose only key is dated:
// "claude-opus-4@20250514" became "claude-opus-4", which no key contains, and a
// live Vertex route came out UNPRICED. Rewriting the separator lands it on the
// dated key exactly. A name with no dated row still falls through to its bare
// key, because the rewritten name still contains it.
func normalize(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "anthropic.")
	m = strings.ReplaceAll(m, "@", "-")
	if i := strings.Index(m, "-v1:"); i >= 0 {
		m = m[:i]
	}
	return m
}

// cost prices n tokens at a USD-per-million rate.
func cost(n int64, perM float64) float64 {
	return float64(n) / 1e6 * perM
}
