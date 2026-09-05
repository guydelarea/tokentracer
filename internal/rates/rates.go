// Package rates fetches a published price list at startup and maps it onto
// billing.Rate, so that a model which shipped after this binary was built is
// priced instead of counted as UNPRICED.
//
// This is the only outbound connection TokenTracer makes that the user did not
// configure, and it is deliberately weak: the fetch is bounded, a failure is
// never fatal, and nothing it returns can change a price that was verified by
// hand. billing.Merge enforces that last part — the rule is stated there.
//
// What gets dropped, and why, is most of this file. A community registry is not
// a vendor price sheet: it carries gateway spellings this proxy never sees, bare
// family keys that would swallow unreleased siblings, and per-model cache
// discounts that contradict the multipliers billing applies as constants. Each
// filter below is one of those. A row that trips any of them is left out, which
// leaves its model UNPRICED — the outcome this project prefers to a confident
// wrong number.
package rates

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"

	"github.com/guydelarea/tokentracer/internal/billing"
)

const (
	// DefaultURL is the community registry internal/billing/rates.go was seeded
	// from. The same list, read at runtime instead of at authoring time.
	DefaultURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

	// maxBytes bounds what a redirect, a mirror or a bad day can make this
	// process allocate. The registry is ~2 MB and grows with the model count, so
	// this is years of headroom and still a number a laptop shrugs at.
	maxBytes = 8 << 20

	// multiplierTolerance is how far a published cache rate may sit from the
	// multiplier billing assumes before its row is dropped. Published prices are
	// round numbers, so 2% absorbs float noise and nothing else: gpt-4o's 0.5x
	// read discount is five times out and gets dropped, which is the point.
	multiplierTolerance = 0.02
)

// vendors are the providers whose dialects this proxy actually speaks. A row for
// anything else describes traffic that cannot reach us, and its model name
// cannot appear on our wire.
var vendors = map[string]bool{"anthropic": true, "openai": true}

// Fetch reads the price list at url. The context bounds the whole call, and the
// caller is expected to give it a short one: nothing downstream of this is worth
// delaying a proxy for.
func Fetch(ctx context.Context, url string) ([]billing.Rate, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rates: %s", resp.Status)
	}

	// Reading one byte past the cap is what separates "ends exactly at the limit"
	// from "was cut off here", so an oversize list is reported rather than parsed
	// as far as it happened to get.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBytes {
		return nil, fmt.Errorf("rates: price list larger than %d bytes", maxBytes)
	}
	return Parse(body)
}

// Parse maps a registry document onto the rates worth keeping, sorted by key so
// that the same document always produces the same table.
//
// Entries are decoded one at a time: a single malformed model must not cost us
// the other three thousand. An entry that will not decode is skipped exactly
// like one that fails a filter. Only a document that is not JSON at all is an
// error, because then there is nothing to skip past.
func Parse(doc []byte) ([]billing.Rate, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(doc, &raw); err != nil {
		return nil, fmt.Errorf("rates: %w", err)
	}

	out := make([]billing.Rate, 0, len(raw))
	for name, blob := range raw {
		var e entry
		if err := json.Unmarshal(blob, &e); err != nil {
			continue
		}
		if r, ok := rateFor(name, e); ok {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// entry is the handful of fields worth reading out of a registry model. Each
// model carries dozens more — capability flags, batch, flex and priority tiers,
// image and audio rates — and every one of them is deliberately absent: billing
// prices a token quartet, and a field it cannot apply is a field that can only
// mislead whoever reads this struct next.
//
// The prices are pointers because absent and zero are different claims. A zero
// input cost would be a free model; an absent one is a model the registry has no
// price for, which is what UNPRICED already means.
type entry struct {
	Provider string `json:"litellm_provider"`
	Mode     string `json:"mode"`

	In  *float64 `json:"input_cost_per_token"`
	Out *float64 `json:"output_cost_per_token"`

	// The cache rates are read only to be checked against billing's multipliers
	// and then thrown away. See multipliersHold.
	Read    *float64 `json:"cache_read_input_token_cost"`
	Write5m *float64 `json:"cache_creation_input_token_cost"`
	Write1h *float64 `json:"cache_creation_input_token_cost_above_1hr"`
}

// rateFor turns one registry entry into a Rate, or reports that it must not
// become one. Every rejection here leaves a model UNPRICED on purpose.
func rateFor(name string, e entry) (billing.Rate, bool) {
	// A '/' is a gateway's spelling of a model ("vertex_ai/claude-opus-5"), and
	// most of the registry's keys carry one. Dropping them loses nothing: the
	// first-party key prices those already, because billing.normalize rewrites
	// the Bedrock and Vertex forms onto it and a route prefix is matched off the
	// last path segment.
	if strings.Contains(name, "/") {
		return billing.Rate{}, false
	}
	// "ft:gpt-4o-2024-08-06" is a fine-tune's price. Fine-tunes bill their own
	// way, and the name cannot reach a row as written.
	if strings.HasPrefix(name, "ft:") {
		return billing.Rate{}, false
	}
	// The registry also prices embeddings, images, audio and reranking. None of
	// them is an Exchange this proxy records.
	if e.Mode != "chat" || !vendors[e.Provider] {
		return billing.Rate{}, false
	}
	if e.In == nil || e.Out == nil || *e.In <= 0 || *e.Out <= 0 {
		return billing.Rate{}, false
	}
	if !multipliersHold(e) {
		return billing.Rate{}, false
	}
	// Exact is set again by billing.Merge, which is the seam that actually owns
	// the guarantee. It is set here too so that a Rate from this package is never
	// briefly a substring key in some future caller's hands.
	return billing.Rate{
		Key:     name,
		InPerM:  *e.In * 1e6,
		OutPerM: *e.Out * 1e6,
		Exact:   true,
	}, true
}

// multipliersHold reports whether billing's cache constants actually describe
// this model's published cache rates.
//
// billing applies ReadMult, Write5mMult and Write1hMult to every model as
// package constants, on the strength of them holding for every hand-verified
// row. Across the registry they hold for barely half the chat models: gpt-4o
// reads at 0.5x its input rate and the gpt-4.1 family at 0.25x, against the 0.1x
// assumed here. Importing such a row would misprice its cache reads — the token
// category that dominates agent traffic — by several times, and silently.
//
// So the published cache rates are read as a check and then discarded. A model
// whose rates disagree with the constants is not imported at all, because the
// constants are what would be used to bill it.
func multipliersHold(e entry) bool {
	return ratioHolds(e.Read, *e.In, billing.ReadMult) &&
		ratioHolds(e.Write5m, *e.In, billing.Write5mMult) &&
		ratioHolds(e.Write1h, *e.In, billing.Write1hMult)
}

// ratioHolds checks one published cache rate against the multiplier billing
// would apply. An absent rate is not a disagreement: a model with no published
// cache price is usually one that does not cache, and bills no cache tokens
// either way.
func ratioHolds(cost *float64, in, mult float64) bool {
	if cost == nil {
		return true
	}
	return math.Abs(*cost/in-mult) <= multiplierTolerance*mult
}
