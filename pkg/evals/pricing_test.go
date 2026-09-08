package evals

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// resetPricing clears the process-wide price cache so each test can install
// its own table.
func resetPricing(t *testing.T) {
	t.Helper()
	reset := func() {
		priceOnce = sync.Once{}
		priceTable = nil
		priceErr = nil
	}
	reset()
	t.Cleanup(reset)
}

func writePriceTable(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write price table: %v", err)
	}
	return path
}

func TestEstimateCost(t *testing.T) {
	resetPricing(t)
	t.Setenv(PriceTableEnv, writePriceTable(t,
		`{"gpt-test": {"input_per_mtok": 2.5, "output_per_mtok": 10}}`))

	cost, ok := EstimateCost("gpt-test", 1_000_000, 500_000)
	if !ok {
		t.Fatal("expected a priced model to report a cost")
	}
	if want := 2.5 + 5.0; math.Abs(cost-want) > 1e-9 {
		t.Fatalf("cost=%v want %v", cost, want)
	}
}

// An unpriced model must report unavailable rather than zero: folding it into
// a total as free is how a cost report quietly becomes a lie.
func TestEstimateCostUnknownModel(t *testing.T) {
	resetPricing(t)
	t.Setenv(PriceTableEnv, writePriceTable(t, `{"gpt-test": {"input_per_mtok": 1}}`))

	if _, ok := EstimateCost("some-other-model", 1000, 1000); ok {
		t.Fatal("an unpriced model must not report a cost")
	}
	if w := PricingWarning("some-other-model"); !strings.Contains(w, "no price for model") {
		t.Fatalf("warning=%q should name the missing model", w)
	}
}

func TestEstimateCostWithoutTable(t *testing.T) {
	resetPricing(t)
	t.Setenv(PriceTableEnv, "")

	if _, ok := EstimateCost("gpt-test", 1000, 1000); ok {
		t.Fatal("no table means no cost")
	}
	table, err := LoadPriceTable()
	if err != nil || table != nil {
		t.Fatalf("got table=%v err=%v, want both empty", table, err)
	}
	if w := PricingWarning("gpt-test"); !strings.Contains(w, PriceTableEnv) {
		t.Fatalf("warning=%q should name %s", w, PriceTableEnv)
	}
}

// A table that is configured but broken is an error: the user asked for costs
// and silently getting none is worse than being told why.
func TestBrokenPriceTableIsAnError(t *testing.T) {
	t.Run("malformed_json", func(t *testing.T) {
		resetPricing(t)
		t.Setenv(PriceTableEnv, writePriceTable(t, `{not json`))
		if _, err := LoadPriceTable(); err == nil {
			t.Fatal("expected a parse error")
		}
		if _, ok := EstimateCost("gpt-test", 1, 1); ok {
			t.Fatal("a broken table must not price anything")
		}
	})
	t.Run("missing_file", func(t *testing.T) {
		resetPricing(t)
		t.Setenv(PriceTableEnv, filepath.Join(t.TempDir(), "absent.json"))
		if _, err := LoadPriceTable(); err == nil {
			t.Fatal("expected a read error")
		}
	})
}
