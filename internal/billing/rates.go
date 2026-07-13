// Code generated from LiteLLM's community price registry. DO NOT EDIT BY HAND.
//
// Source: https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json
// Generated: 2026-07-13
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
// the sonnet-4 family. claude-sonnet-5 and the opus-4-5+ generation have no premium
// tier in the registry, so none is asserted for them.

package billing

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
	{Key: "claude-sonnet-5", InPerM: 2, OutPerM: 10},
	{Key: "claude-fable-5", InPerM: 10, OutPerM: 50},
}
