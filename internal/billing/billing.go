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

// Cache multipliers ride on the input base rate: Anthropic bills a cache read
// at 0.1x the input rate, a 5-minute cache write at 1.25x, and a 1-hour write
// at 2.0x. These hold for every current Claude model, which is why they live
// here as constants instead of per-rate fields.
const (
	ReadMult    = 0.1
	Write5mMult = 1.25
	Write1hMult = 2.0
)

// Rate is one price line: a model key, its per-million-token rates, and the
// window in which they applied.
type Rate struct {
	// Key is matched as a substring against the normalized model name.
	Key string

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

// Usage is the token quartet exactly as the Anthropic API reports it: fresh
// input, cache reads, cache writes split by TTL, and output.
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
	// Anthropic prices input up to and including 200K at standard rates, so a
	// request landing exactly on the boundary is still standard. Every input
	// component counts toward the threshold, cache reads included — which is
	// what pushes ordinary Claude Code turns over it.
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
		if !strings.Contains(model, r.Key) {
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

// normalize reduces a wire model name to its bare Claude family name, so that
// one key matches the same model however the gateway spelled it:
//
//	anthropic.claude-sonnet-4@20250514 -> claude-sonnet-4  (Vertex)
//	anthropic.claude-opus-4-5-v1:0     -> claude-opus-4-5  (Bedrock)
func normalize(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "anthropic.")
	if i := strings.IndexByte(m, '@'); i >= 0 {
		m = m[:i]
	}
	if i := strings.Index(m, "-v1:"); i >= 0 {
		m = m[:i]
	}
	return m
}

// cost prices n tokens at a USD-per-million rate.
func cost(n int64, perM float64) float64 {
	return float64(n) / 1e6 * perM
}
