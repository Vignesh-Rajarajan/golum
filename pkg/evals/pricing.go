package evals

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// PriceTableEnv names the environment variable pointing at a JSON price table.
const PriceTableEnv = "GOLUM_EVAL_PRICES"

// ModelPrice is USD per one million tokens.
type ModelPrice struct {
	InputPerMTok  float64 `json:"input_per_mtok"`
	OutputPerMTok float64 `json:"output_per_mtok"`
}

// PriceTable maps model name to price.
type PriceTable map[string]ModelPrice

var (
	priceOnce  sync.Once
	priceTable PriceTable
	priceErr   error
)

// LoadPriceTable reads the table named by GOLUM_EVAL_PRICES once per process.
// A missing variable is not an error: it means costs are simply unavailable.
// A present-but-broken file is an error, because silently pricing a run at
// zero is worse than refusing to price it.
func LoadPriceTable() (PriceTable, error) {
	priceOnce.Do(func() {
		path := os.Getenv(PriceTableEnv)
		if path == "" {
			return
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			priceErr = fmt.Errorf("evals: read %s=%s: %w", PriceTableEnv, path, err)
			return
		}
		var table PriceTable
		if err := json.Unmarshal(raw, &table); err != nil {
			priceErr = fmt.Errorf("evals: parse price table %s: %w", path, err)
			return
		}
		priceTable = table
	})
	return priceTable, priceErr
}

// EstimateCost prices a run. ok is false when no table is configured or the
// model is not in it; callers must report that as unavailable rather than
// folding it into a total as zero.
func EstimateCost(model string, inputTokens, outputTokens int) (float64, bool) {
	table, err := LoadPriceTable()
	if err != nil || table == nil {
		return 0, false
	}
	price, ok := table[model]
	if !ok {
		return 0, false
	}
	const perMillion = 1_000_000.0
	cost := float64(inputTokens)/perMillion*price.InputPerMTok +
		float64(outputTokens)/perMillion*price.OutputPerMTok
	return cost, true
}

// PricingWarning describes why cost is missing, for inclusion in a report.
func PricingWarning(model string) string {
	table, err := LoadPriceTable()
	switch {
	case err != nil:
		return err.Error()
	case table == nil:
		return fmt.Sprintf("cost unavailable: %s is not set", PriceTableEnv)
	default:
		if _, ok := table[model]; !ok {
			return fmt.Sprintf("cost unavailable: no price for model %q", model)
		}
	}
	return ""
}
