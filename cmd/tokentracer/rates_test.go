package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/guydelarea/tokentracer/internal/billing"
)

// epochForTest is any instant inside every rate window in the table.
var epochForTest = time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)

// A refresh is a convenience, never a dependency. Every way it can fail leaves
// the process running on the prices that were compiled in, and says so on the
// one line the startup screen gives it.
func TestRefreshRatesFallsBackToTheEmbeddedTable(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	notJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>not a price list</html>"))
	}))
	defer notJSON.Close()

	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer missing.Close()

	cases := []struct {
		name, url, wantLine string
	}{
		{"disabled", "", "refresh off"},
		{"unreachable host", deadURL, "refresh failed"},
		{"not a price list", notJSON.URL, "refresh failed"},
		{"404", missing.URL, "refresh failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			table, line := refreshRates(c.url)

			if len(table) != len(billing.Rates) {
				t.Errorf("table = %d rows, want the embedded %d", len(table), len(billing.Rates))
			}
			if !strings.Contains(line, c.wantLine) {
				t.Errorf("startup line = %q, want it to mention %q", line, c.wantLine)
			}
		})
	}
}

// The happy path: a model the embedded table has no row for is priced after the
// refresh, and the line says where the prices came from.
func TestRefreshRatesFillsHolesAndNamesTheHost(t *testing.T) {
	const model = "claude-neue-9-20270101"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"` + model + `":{"litellm_provider":"anthropic","mode":"chat",
			"input_cost_per_token":0.000004,"output_cost_per_token":0.00002}}`))
	}))
	defer srv.Close()

	table, line := refreshRates(srv.URL)

	if len(table) != len(billing.Rates)+1 {
		t.Fatalf("table = %d rows, want the embedded %d plus one", len(table), len(billing.Rates)+1)
	}
	if !strings.Contains(line, "1 filled in from "+hostOf(srv.URL)) {
		t.Errorf("startup line = %q, want it to name the host it fetched from", line)
	}
	if got := billing.Compute(table, model, billing.Usage{In: 1_000_000}, epochForTest); !got.Priced {
		t.Errorf("%s still UNPRICED after a successful refresh", model)
	}
}

// "off" is the switch for an air-gapped machine, or anyone who would rather this
// binary made no connection it was not asked to make.
func TestRatesURLCanBeTurnedOff(t *testing.T) {
	for _, v := range []string{"off", "OFF", "none"} {
		t.Setenv("TOKENTRACER_RATES_URL", v)
		if got := ratesURL(); got != "" {
			t.Errorf("TOKENTRACER_RATES_URL=%s gave %q, want the refresh disabled", v, got)
		}
	}

	t.Setenv("TOKENTRACER_RATES_URL", "https://example.invalid/prices.json")
	if got := ratesURL(); got != "https://example.invalid/prices.json" {
		t.Errorf("ratesURL = %q, want the override", got)
	}
}
