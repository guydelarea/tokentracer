package rates

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guydelarea/tokentracer/internal/billing"
)

// doc wraps model entries into a registry document.
func doc(entries ...string) []byte {
	return []byte("{" + strings.Join(entries, ",") + "}")
}

// model writes one well-formed first-party chat entry whose cache rates agree
// with billing's multipliers, so that a test can vary exactly one thing.
func model(name, provider string, inPerTok, outPerTok float64) string {
	return fmt.Sprintf(`%q:{"litellm_provider":%q,"mode":"chat",
		"input_cost_per_token":%v,"output_cost_per_token":%v,
		"cache_read_input_token_cost":%v,"cache_creation_input_token_cost":%v}`,
		name, provider, inPerTok, outPerTok,
		inPerTok*billing.ReadMult, inPerTok*billing.Write5mMult)
}

func keys(rs []billing.Rate) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Key)
	}
	return out
}

func mustParse(t *testing.T, d []byte) []billing.Rate {
	t.Helper()
	got, err := Parse(d)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return got
}

// The happy path: a first-party chat model becomes a Rate in USD per 1M tokens,
// and it is exact-matched so it can never swallow a sibling.
func TestParseMapsAFirstPartyChatModel(t *testing.T) {
	got := mustParse(t, doc(model("claude-neue-9", "anthropic", 0.000003, 0.000015)))

	if len(got) != 1 {
		t.Fatalf("rates = %v, want exactly one", keys(got))
	}
	r := got[0]
	if r.Key != "claude-neue-9" || r.InPerM != 3 || r.OutPerM != 15 {
		t.Errorf("rate = %+v, want claude-neue-9 at 3/15 per 1M", r)
	}
	if !r.Exact {
		t.Error("fetched rate is not Exact — a substring key from a fetch can swallow an unreleased sibling")
	}
	if r.LongCtxThreshold != 0 {
		t.Errorf("LongCtxThreshold = %d, want 0: registry long-context tiers are deliberately not imported",
			r.LongCtxThreshold)
	}
}

// Everything the registry publishes that this proxy must not price. Each of
// these leaves the model UNPRICED, which is the intended outcome.
func TestParseDropsRowsThatCannotBePricedHere(t *testing.T) {
	cases := []struct {
		name  string
		entry string
		why   string
	}{
		{"gateway spelling", model("vertex_ai/claude-neue-9", "anthropic", 0.000003, 0.000015),
			"the first-party key already prices it, via normalize"},
		{"fine tune", model("ft:gpt-9-2027-01-01", "openai", 0.000003, 0.000015),
			"fine-tunes bill their own way"},
		{"other vendor", model("grok-9", "xai", 0.000003, 0.000015),
			"no vendor module speaks its dialect"},
		{"zero price", model("claude-neue-9", "anthropic", 0, 0.000015),
			"a zero price is a missing price, not a free model"},
		{"non-chat mode", `"text-embedding-9":{"litellm_provider":"openai","mode":"embedding",
			"input_cost_per_token":0.000003,"output_cost_per_token":0.000015}`,
			"an embedding is not an Exchange"},
		{"missing output price", `"claude-neue-9":{"litellm_provider":"anthropic","mode":"chat",
			"input_cost_per_token":0.000003}`,
			"half a price is not a price"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mustParse(t, doc(c.entry)); len(got) != 0 {
				t.Errorf("kept %v, want it dropped — %s", keys(got), c.why)
			}
		})
	}
}

// The reason the cache rates are read at all. billing applies ReadMult as a
// package constant, so a model whose published discount disagrees would have its
// cache reads — the token category that dominates agent traffic — billed several
// times wrong, and silently. Real examples: gpt-4o reads at 0.5x its input rate,
// the gpt-4.1 family at 0.25x, against the 0.1x assumed here.
func TestParseDropsModelsThatContradictTheCacheMultipliers(t *testing.T) {
	const in = 0.000005
	cases := []struct {
		name  string
		entry string
	}{
		{"read discount is 0.5x, not 0.1x", fmt.Sprintf(
			`"gpt-9":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":%v,
			"output_cost_per_token":0.00002,"cache_read_input_token_cost":%v}`, in, in*0.5)},
		{"read discount is 0.025x, not 0.1x", fmt.Sprintf(
			`"claude-neue-9":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":%v,
			"output_cost_per_token":0.00005,"cache_read_input_token_cost":%v}`, in, in*0.025)},
		{"5m write is 2x, not 1.25x", fmt.Sprintf(
			`"claude-neue-9":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":%v,
			"output_cost_per_token":0.00005,"cache_creation_input_token_cost":%v}`, in, in*2)},
		{"1h write is 3x, not 2x", fmt.Sprintf(
			`"claude-neue-9":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":%v,
			"output_cost_per_token":0.00005,"cache_creation_input_token_cost_above_1hr":%v}`, in, in*3)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mustParse(t, doc(c.entry)); len(got) != 0 {
				t.Errorf("kept %v, but billing would bill its cache tokens at the wrong rate", keys(got))
			}
		})
	}
}

// An absent cache rate is not a disagreement — a model with no published cache
// price bills no cache tokens either way.
func TestParseKeepsAModelWithNoPublishedCacheRates(t *testing.T) {
	got := mustParse(t, doc(`"gpt-9":{"litellm_provider":"openai","mode":"chat",
		"input_cost_per_token":0.000005,"output_cost_per_token":0.00002}`))

	if len(got) != 1 || got[0].Key != "gpt-9" {
		t.Fatalf("rates = %v, want gpt-9 kept", keys(got))
	}
}

// Float noise in a published price must not cost a model its row.
func TestParseToleratesRoundingInThePublishedCacheRate(t *testing.T) {
	got := mustParse(t, doc(`"gpt-9":{"litellm_provider":"openai","mode":"chat",
		"input_cost_per_token":0.000003,"output_cost_per_token":0.000015,
		"cache_read_input_token_cost":0.00000030000000000000004}`))

	if len(got) != 1 {
		t.Fatalf("rates = %v, want the row kept despite float noise", keys(got))
	}
}

// One unreadable model must not cost us the rest of the list — the same
// degradation habit the recorder has.
func TestParseSkipsMalformedEntriesAndKeepsTheRest(t *testing.T) {
	got := mustParse(t, doc(
		`"broken":"this entry is a string, not an object"`,
		model("claude-neue-9", "anthropic", 0.000003, 0.000015),
	))

	if len(got) != 1 || got[0].Key != "claude-neue-9" {
		t.Fatalf("rates = %v, want the one good row", keys(got))
	}
}

// A document that is not JSON has nothing to skip past, so it is an error and
// the caller falls back to the embedded table.
func TestParseRejectsADocumentThatIsNotJSON(t *testing.T) {
	if _, err := Parse([]byte("<html>404</html>")); err == nil {
		t.Fatal("Parse accepted HTML — a proxy error page would become a price list")
	}
}

// Same document, same table: the fold must not reorder under the user because a
// map iterated differently.
func TestParseIsDeterministic(t *testing.T) {
	d := doc(
		model("gpt-9", "openai", 0.000005, 0.00002),
		model("claude-neue-9", "anthropic", 0.000003, 0.000015),
		model("gpt-9-mini", "openai", 0.0000001, 0.0000004),
	)
	first := keys(mustParse(t, d))
	for i := 0; i < 20; i++ {
		if got := keys(mustParse(t, d)); !equal(got, first) {
			t.Fatalf("run %d = %v, want %v", i, got, first)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFetchReadsAPublishedList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(doc(model("claude-neue-9", "anthropic", 0.000003, 0.000015)))
	}))
	defer srv.Close()

	got, err := Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 || got[0].Key != "claude-neue-9" {
		t.Fatalf("rates = %v, want claude-neue-9", keys(got))
	}
}

// Every way the fetch can go wrong is an error the caller turns into "run on the
// embedded table, and say why". None of them may return a partial list.
func TestFetchFailsLoudlyRatherThanPartially(t *testing.T) {
	t.Run("non-200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "gone", http.StatusNotFound)
		}))
		defer srv.Close()

		if _, err := Fetch(context.Background(), srv.URL); err == nil {
			t.Fatal("Fetch accepted a 404 body as a price list")
		}
	})

	t.Run("oversize", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Valid JSON, just far too much of it: the cap must fire on the byte
			// count, before anything is parsed.
			fmt.Fprintf(w, `{"padding":%q}`, strings.Repeat("x", maxBytes+1))
		}))
		defer srv.Close()

		if _, err := Fetch(context.Background(), srv.URL); err == nil {
			t.Fatal("Fetch read past the size cap")
		}
	})

	t.Run("host hangs", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-release
		}))
		defer srv.Close()
		defer close(release)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Fetch(ctx, srv.URL); err == nil {
			t.Fatal("Fetch ignored its context — a wedged host would delay startup")
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := srv.URL
		srv.Close() // nothing is listening now

		if _, err := Fetch(context.Background(), url); err == nil {
			t.Fatal("Fetch reported success against a closed port")
		}
	})
}

// The long-context threshold is in the FIELD NAME, not a value, so it has to be
// read out of the key. billing.Merge decides whether it may be applied.
func TestParseReadsACompleteLongContextTier(t *testing.T) {
	got := mustParse(t, doc(`"gpt-9":{"litellm_provider":"openai","mode":"chat",
		"input_cost_per_token":0.000004,"output_cost_per_token":0.00002,
		"input_cost_per_token_above_272k_tokens":0.000008,
		"output_cost_per_token_above_272k_tokens":0.00003}`))

	if len(got) != 1 {
		t.Fatalf("rates = %v, want one", keys(got))
	}
	r := got[0]
	if r.LongCtxThreshold != 272_000 || r.LongCtxInPerM != 8 || r.LongCtxOutPerM != 30 {
		t.Errorf("tier = %d/%v/%v, want 272000 at 8/30 per 1M",
			r.LongCtxThreshold, r.LongCtxInPerM, r.LongCtxOutPerM)
	}
}

// Half a tier would reprice a whole request off one published number and one
// invented one, and two halves that disagree about where the tier starts
// describe no threshold at all.
func TestParseIgnoresAnIncompleteLongContextTier(t *testing.T) {
	cases := []struct{ name, fields string }{
		{"input only", `"input_cost_per_token_above_272k_tokens":0.000008`},
		{"output only", `"output_cost_per_token_above_272k_tokens":0.00003`},
		{"thresholds disagree", `"input_cost_per_token_above_272k_tokens":0.000008,
			"output_cost_per_token_above_200k_tokens":0.00003`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mustParse(t, doc(`"gpt-9":{"litellm_provider":"openai","mode":"chat",
				"input_cost_per_token":0.000004,"output_cost_per_token":0.00002,`+c.fields+`}`))

			if len(got) != 1 {
				t.Fatalf("rates = %v, want the row itself kept", keys(got))
			}
			if got[0].LongCtxThreshold != 0 {
				t.Errorf("tier = %d, want none — half a tier is not a tier", got[0].LongCtxThreshold)
			}
		})
	}
}

// The list spells the same field several ways for service tiers this proxy
// cannot observe on a request — flex, priority, batch. Reading one as the
// standard long-context rate would price every large request at the wrong tier.
func TestParseIgnoresServiceTierSpellingsOfTheLongContextFields(t *testing.T) {
	got := mustParse(t, doc(`"gpt-9":{"litellm_provider":"openai","mode":"chat",
		"input_cost_per_token":0.000004,"output_cost_per_token":0.00002,
		"input_cost_per_token_above_272k_tokens_flex":0.000004,
		"output_cost_per_token_above_272k_tokens_flex":0.000015,
		"input_cost_per_token_above_272k_tokens_priority":0.000016,
		"output_cost_per_token_above_272k_tokens_priority":0.00006}`))

	if len(got) != 1 {
		t.Fatalf("rates = %v, want one", keys(got))
	}
	if got[0].LongCtxThreshold != 0 {
		t.Errorf("tier = %d at %v/%v, want none — those are flex and priority rates",
			got[0].LongCtxThreshold, got[0].LongCtxInPerM, got[0].LongCtxOutPerM)
	}
}
