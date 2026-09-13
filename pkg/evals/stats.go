package evals

import (
	"fmt"
	"math"
	"sort"
)

// MinRecommendedSamples is the point below which a pass rate says more about
// luck than about the agent.
const MinRecommendedSamples = 5

// DefaultZ is the z-score for a 95% confidence interval.
const DefaultZ = 1.96

// PassAtK estimates the chance that at least one of k independent attempts
// passes, given c successes observed in n samples. This measures a capability
// ceiling: whether the agent can do the task at all when given retries.
//
// It uses the unbiased estimator 1 - C(n-c, k) / C(n, k) rather than
// resampling, so a single set of n runs answers the question for every k <= n.
func PassAtK(successes, n, k int) (float64, error) {
	if err := checkSample(successes, n, k); err != nil {
		return 0, err
	}
	failures := n - successes
	if failures < k {
		// Fewer failures than draws: at least one draw must be a success.
		return 1, nil
	}
	return 1 - hypergeometricRatio(failures, n, k), nil
}

// PassPowerK estimates the chance that all k independent attempts pass. This
// measures reliability, which is the number that matters for anything run
// unattended: an agent with pass@5 of 1.0 and pass^5 of 0.2 fails four times
// out of five when nobody is retrying for it.
func PassPowerK(successes, n, k int) (float64, error) {
	if err := checkSample(successes, n, k); err != nil {
		return 0, err
	}
	if successes < k {
		return 0, nil
	}
	return hypergeometricRatio(successes, n, k), nil
}

// hypergeometricRatio computes C(pool, k) / C(n, k) without building large
// factorials, which overflow float64 well before n gets interesting.
func hypergeometricRatio(pool, n, k int) float64 {
	ratio := 1.0
	for i := 0; i < k; i++ {
		ratio *= float64(pool-i) / float64(n-i)
	}
	return ratio
}

func checkSample(successes, n, k int) error {
	if n <= 0 {
		return fmt.Errorf("evals: need at least one sample, got n=%d", n)
	}
	if k <= 0 {
		return fmt.Errorf("evals: k must be positive, got k=%d", k)
	}
	if k > n {
		return fmt.Errorf("evals: cannot estimate k=%d from n=%d samples", k, n)
	}
	if successes < 0 || successes > n {
		return fmt.Errorf("evals: successes=%d out of range for n=%d", successes, n)
	}
	return nil
}

// WilsonInterval returns a confidence interval for a pass rate. The Wilson
// score interval is used instead of the normal approximation because eval
// sample sizes are small and pass rates cluster at 0 and 1, exactly where the
// normal approximation produces bounds outside [0,1].
func WilsonInterval(successes, n int, z float64) (lo, hi float64) {
	if n <= 0 {
		return 0, 0
	}
	if z <= 0 {
		z = DefaultZ
	}
	nf := float64(n)
	p := float64(successes) / nf
	z2 := z * z
	denom := 1 + z2/nf
	center := (p + z2/(2*nf)) / denom
	margin := z * math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf)) / denom
	return clamp01(center - margin), clamp01(center + margin)
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

// MinSamplesWarning returns a warning when n is too small for the resulting
// rate to mean much, and "" otherwise. Reports surface this so a 100% pass
// rate over two runs is not read as a 100% pass rate.
func MinSamplesWarning(n int) string {
	if n >= MinRecommendedSamples {
		return ""
	}
	return fmt.Sprintf("n=%d is below the recommended minimum of %d; "+
		"treat this rate as indicative only", n, MinRecommendedSamples)
}

// Percentiles summarizes a latency or cost distribution.
type Percentiles struct {
	P50  float64 `json:"p50"`
	P95  float64 `json:"p95"`
	Mean float64 `json:"mean"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
}

// Summarize computes percentiles over values. An empty input yields a zero
// value.
func Summarize(values []float64) Percentiles {
	if len(values) == 0 {
		return Percentiles{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	return Percentiles{
		P50:  quantile(sorted, 0.50),
		P95:  quantile(sorted, 0.95),
		Mean: sum / float64(len(sorted)),
		Min:  sorted[0],
		Max:  sorted[len(sorted)-1],
	}
}

// quantile uses nearest-rank on already-sorted input: with the handful of
// samples an eval produces, interpolating between two runs would invent a
// latency that never happened.
func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(q * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
