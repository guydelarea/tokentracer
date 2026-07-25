// Seeded from LiteLLM's community price registry, then kept current by hand.
//
// Source: https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json
// Generated: 2026-07-13, hand-updated 2026-07-25 (opus-5, mythos-5, sonnet-5 intro window)
//
// Hand-added rows come from Anthropic's published list prices, not the registry,
// which lags a model launch by weeks. A model that ships between regenerations is
// the whole source of UNPRICED requests, so adding the row by hand is the fix —
// but only ever with a price we can point at. A guess here is worse than a hole.
//
// Prices are USD per 1M tokens (the registry quotes per-token; these are x1e6).
// Only litellm_provider == "anthropic" claude-* models are included; Bedrock and
// Vertex spellings of the same models are handled by normalize(), not by extra rows.
//
// Ordering is load-bearing. Compute takes the FIRST rate whose key is a substring
// of the model name, so rows are sorted longest-key-first: "claude-opus-4-5" must
// be tested before a "claude-opus-4" that would otherwise swallow it and price a
// $5/1M model at the older $15/1M. TestSeedTableOrderingIsMostSpecificFirst pins this.
//
// There are deliberately NO bare family fallbacks (no lone "sonnet" or "opus" key).
// An unreleased model would silently inherit a sibling's price, and a wrong cost is
// invisible; an unpriced one is a badge in the UI. Unknown model -> Priced:false.
//
// From is the zero time on every row: these are current list prices and this project
// has no verified price history. The [From, Until) machinery exists and is tested,
// but inventing historical windows would fabricate costs, so it goes unused here.
//
// Long-context tiers: the registry carries above-200K input/output rates only for
// the sonnet-4 family. claude-sonnet-5 and the opus-4-5+ generation ship a 1M
// window at standard rates with no long-context premium, so none is asserted for
// them. That is a fact about those models, not an omission.

package billing

import "time"

// Sonnet 5 launched at an introductory rate Anthropic publishes as "$2/$10 per
// MTok through 2026-08-31". This is the one price in the table with a known end
// date, so it is the one place the [From, Until) machinery earns its keep: past
// the boundary the same key prices at the standard $3/$15, and a session traced
// on either side of it bills at what it actually cost.
var sonnet5StandardFrom = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

var Rates = []Rate{
	{Key: "claude-3-7-sonnet-20250219", InPerM: 3, OutPerM: 15},
	{Key: "claude-sonnet-4-5-20250929", InPerM: 3, OutPerM: 15, LongCtxThreshold: 200_000, LongCtxInPerM: 6, LongCtxOutPerM: 22.5},
	{Key: "claude-haiku-4-5-20251001", InPerM: 1, OutPerM: 5},
	{Key: "claude-4-sonnet-20250514", InPerM: 3, OutPerM: 15, LongCtxThreshold: 200_000, LongCtxInPerM: 6, LongCtxOutPerM: 22.5},
	{Key: "claude-opus-4-1-20250805", InPerM: 15, OutPerM: 75},
	{Key: "claude-opus-4-5-20251101", InPerM: 5, OutPerM: 25},
	{Key: "claude-opus-4-6-20260205", InPerM: 5, OutPerM: 25},
	{Key: "claude-opus-4-7-20260416", InPerM: 5, OutPerM: 25},
	{Key: "claude-sonnet-4-20250514", InPerM: 3, OutPerM: 15, LongCtxThreshold: 200_000, LongCtxInPerM: 6, LongCtxOutPerM: 22.5},
	{Key: "claude-3-haiku-20240307", InPerM: 0.25, OutPerM: 1.25},
	{Key: "claude-3-opus-20240229", InPerM: 15, OutPerM: 75},
	{Key: "claude-4-opus-20250514", InPerM: 15, OutPerM: 75},
	{Key: "claude-opus-4-20250514", InPerM: 15, OutPerM: 75},
	{Key: "claude-sonnet-4-5", InPerM: 3, OutPerM: 15, LongCtxThreshold: 200_000, LongCtxInPerM: 6, LongCtxOutPerM: 22.5},
	{Key: "claude-sonnet-4-6", InPerM: 3, OutPerM: 15},
	{Key: "claude-haiku-4-5", InPerM: 1, OutPerM: 5},
	{Key: "claude-opus-4-1", InPerM: 15, OutPerM: 75},
	{Key: "claude-opus-4-5", InPerM: 5, OutPerM: 25},
	{Key: "claude-opus-4-6", InPerM: 5, OutPerM: 25},
	{Key: "claude-opus-4-7", InPerM: 5, OutPerM: 25},
	{Key: "claude-opus-4-8", InPerM: 5, OutPerM: 25},
	{Key: "claude-mythos-5", InPerM: 10, OutPerM: 50},
	{Key: "claude-sonnet-5", InPerM: 2, OutPerM: 10, Until: sonnet5StandardFrom},
	{Key: "claude-sonnet-5", InPerM: 3, OutPerM: 15, From: sonnet5StandardFrom},
	{Key: "claude-fable-5", InPerM: 10, OutPerM: 50},
	{Key: "claude-opus-5", InPerM: 5, OutPerM: 25},
}
