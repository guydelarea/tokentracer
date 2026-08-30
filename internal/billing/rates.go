// Seeded from LiteLLM's community price registry, then kept current by hand
// from Anthropic and OpenAI's published model pages.
//
// Source: https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json
// OpenAI source: https://developers.openai.com/api/docs/pricing#text-tokens
// Anthropic source: https://platform.claude.com/docs/en/about-claude/pricing
// Generated: 2026-07-13, hand-verified against both vendor pages 2026-08-30
//
// Hand-added rows come from the vendors' published list prices, not the registry,
// which lags a model launch by weeks. A model that ships between regenerations is
// the whole source of UNPRICED requests, so adding the row by hand is the fix —
// but only ever with a price we can point at. A guess here is worse than a hole.
//
// Prices are USD per 1M tokens (the registry quotes per-token; these are x1e6).
// Bedrock and Vertex spellings of Claude models are handled by normalize(), not
// by extra rows. OpenAI Responses model IDs match their published names directly.
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
// Sonnet 5's introductory rate was the one dated price this table ever carried;
// Anthropic has since made $2/$10 the standard price and cancelled the increase
// that was scheduled for 2026-09-01, so the window is gone rather than expired.
//
// Long-context tiers: NO Claude model has one. Claude 4.6 and later ship a 1M
// window at standard rates, and every earlier model tops out at 200k, so there is
// no window in which a premium could apply. The registry's above-200K rates for
// the sonnet-4 family described a beta that no longer exists; carrying them
// asserted a tier the vendor has stopped publishing and no request could reach.
// The GPT-5.6 family is the only long-context tier here: it prices the WHOLE
// request at 2x input and 1.5x output once input passes 272K, which is what the
// LongCtx* fields model. The machinery stays covered by synthetic-rate tests.
//
// Published price modifiers deliberately NOT modelled here, because the facts
// they key off are not on the row: Anthropic fast mode (Opus 5 / Opus 4.8 at
// $10/$50 on speed:"fast"), US-pinned inference (inference_geo:"us", 1.1x on
// every category for 4.6+), OpenAI's own fast mode, and both vendors' 50% batch
// discount. Each would need capturing before it could be priced. Until then such
// an exchange is reported at the standard rate — under-billed for the premiums,
// over-billed for batch. This list is the known set, not a guarantee of one.

package billing

var Rates = []Rate{
	// GPT-5.6: the three variants price differently, and "gpt-5.6" is the
	// published alias for sol — it must sort after the variants so it does not
	// swallow them. 272_000 is a cliff, not a tier: one token over and the
	// entire request reprices.
	{Key: "gpt-5.6-terra", InPerM: 2, OutPerM: 12, LongCtxThreshold: 272_000, LongCtxInPerM: 4, LongCtxOutPerM: 18},
	{Key: "gpt-5.6-luna", InPerM: 0.2, OutPerM: 1.2, LongCtxThreshold: 272_000, LongCtxInPerM: 0.4, LongCtxOutPerM: 1.8},
	{Key: "gpt-5.6-sol", InPerM: 4, OutPerM: 20, LongCtxThreshold: 272_000, LongCtxInPerM: 8, LongCtxOutPerM: 30},
	// KNOWN EXCEPTION to the no-bare-family-fallback rule below. "gpt-5.6" is a
	// published alias for sol, so it earns a row — but it is matched as a
	// substring like every other key, so an unreleased "gpt-5.6-pro" would
	// silently inherit sol's price instead of coming out UNPRICED (gpt-5.5-pro
	// is $30/$180 against gpt-5.5's $5/$30, so the error would be large). The
	// ordering test cannot catch this: it compares keys to keys, never model
	// names to keys. Delete this row if a variant ships that it would swallow.
	{Key: "gpt-5.6", InPerM: 4, OutPerM: 20, LongCtxThreshold: 272_000, LongCtxInPerM: 8, LongCtxOutPerM: 30},
	{Key: "claude-3-7-sonnet-20250219", InPerM: 3, OutPerM: 15},
	{Key: "claude-sonnet-4-5-20250929", InPerM: 3, OutPerM: 15},
	{Key: "claude-haiku-4-5-20251001", InPerM: 1, OutPerM: 5},
	{Key: "claude-4-sonnet-20250514", InPerM: 3, OutPerM: 15},
	{Key: "claude-opus-4-1-20250805", InPerM: 15, OutPerM: 75},
	{Key: "claude-opus-4-5-20251101", InPerM: 5, OutPerM: 25},
	{Key: "claude-sonnet-4-20250514", InPerM: 3, OutPerM: 15},
	{Key: "claude-3-haiku-20240307", InPerM: 0.25, OutPerM: 1.25},
	// Retired on the first-party API, still served on Bedrock and Google Cloud,
	// which this proxy sits in front of. A live route with no row is an UNPRICED
	// session, and this price is published.
	{Key: "claude-3-5-haiku-20241022", InPerM: 0.8, OutPerM: 4},
	{Key: "claude-3-opus-20240229", InPerM: 15, OutPerM: 75},
	{Key: "claude-4-opus-20250514", InPerM: 15, OutPerM: 75},
	{Key: "claude-opus-4-20250514", InPerM: 15, OutPerM: 75},
	{Key: "claude-sonnet-4-5", InPerM: 3, OutPerM: 15},
	{Key: "claude-sonnet-4-6", InPerM: 3, OutPerM: 15},
	{Key: "claude-haiku-4-5", InPerM: 1, OutPerM: 5},
	{Key: "claude-opus-4-1", InPerM: 15, OutPerM: 75},
	{Key: "claude-opus-4-5", InPerM: 5, OutPerM: 25},
	{Key: "claude-opus-4-6", InPerM: 5, OutPerM: 25},
	{Key: "claude-opus-4-7", InPerM: 5, OutPerM: 25},
	{Key: "claude-opus-4-8", InPerM: 5, OutPerM: 25},
	{Key: "claude-mythos-5", InPerM: 10, OutPerM: 50},
	{Key: "claude-sonnet-5", InPerM: 2, OutPerM: 10},
	{Key: "claude-fable-5", InPerM: 10, OutPerM: 50},
	{Key: "claude-opus-5", InPerM: 5, OutPerM: 25},
}
