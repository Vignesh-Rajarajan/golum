package evals

import (
	"math"
	"testing"
)

const eps = 1e-9

func TestPassAtK(t *testing.T) {
	tests := []struct {
		name            string
		successes, n, k int
		want            float64
	}{
		// 1 success in 4: the chance two draws miss it is C(3,2)/C(4,2) = 3/6.
		{"one_of_four_k2", 1, 4, 2, 0.5},
		{"one_of_four_k1", 1, 4, 1, 0.25},
		{"two_of_four_k2", 2, 4, 2, 1 - 1.0/6.0},
		{"never_passed", 0, 5, 3, 0},
		{"always_passed", 5, 5, 3, 1},
		// More successes than failures leaves no way to draw k failures.
		{"failures_below_k", 4, 5, 2, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PassAtK(tc.successes, tc.n, tc.k)
			if err != nil {
				t.Fatalf("PassAtK: %v", err)
			}
			if math.Abs(got-tc.want) > eps {
				t.Fatalf("PassAtK(%d,%d,%d)=%v want %v", tc.successes, tc.n, tc.k, got, tc.want)
			}
		})
	}
}

func TestPassPowerK(t *testing.T) {
	tests := []struct {
		name            string
		successes, n, k int
		want            float64
	}{
		// Both draws must succeed: C(2,2)/C(4,2) = 1/6.
		{"two_of_four_k2", 2, 4, 2, 1.0 / 6.0},
		{"k1_is_pass_rate", 2, 4, 1, 0.5},
		{"all_pass", 4, 4, 3, 1},
		{"too_few_successes", 2, 5, 3, 0},
		{"none_pass", 0, 5, 1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PassPowerK(tc.successes, tc.n, tc.k)
			if err != nil {
				t.Fatalf("PassPowerK: %v", err)
			}
			if math.Abs(got-tc.want) > eps {
				t.Fatalf("PassPowerK(%d,%d,%d)=%v want %v", tc.successes, tc.n, tc.k, got, tc.want)
			}
		})
	}
}

// pass^k must never exceed pass@k: needing every attempt to succeed is at
// least as hard as needing one to.
func TestPassPowerKNeverExceedsPassAtK(t *testing.T) {
	for n := 1; n <= 8; n++ {
		for c := 0; c <= n; c++ {
			for k := 1; k <= n; k++ {
				at, err := PassAtK(c, n, k)
				if err != nil {
					t.Fatalf("PassAtK: %v", err)
				}
				pow, err := PassPowerK(c, n, k)
				if err != nil {
					t.Fatalf("PassPowerK: %v", err)
				}
				if pow > at+eps {
					t.Fatalf("c=%d n=%d k=%d: pass^k=%v > pass@k=%v", c, n, k, pow, at)
				}
			}
		}
	}
}

// pass@k rises with k and pass^k falls with k.
func TestPassKMonotonicity(t *testing.T) {
	const c, n = 3, 8
	prevAt, prevPow := -1.0, 2.0
	for k := 1; k <= n; k++ {
		at, err := PassAtK(c, n, k)
		if err != nil {
			t.Fatalf("PassAtK: %v", err)
		}
		pow, err := PassPowerK(c, n, k)
		if err != nil {
			t.Fatalf("PassPowerK: %v", err)
		}
		if at < prevAt-eps {
			t.Fatalf("pass@%d=%v fell below pass@%d=%v", k, at, k-1, prevAt)
		}
		if pow > prevPow+eps {
			t.Fatalf("pass^%d=%v rose above pass^%d=%v", k, pow, k-1, prevPow)
		}
		prevAt, prevPow = at, pow
	}
}

func TestPassKRejectsImpossibleSamples(t *testing.T) {
	cases := []struct {
		name            string
		successes, n, k int
	}{
		{"k_exceeds_n", 1, 3, 4},
		{"zero_samples", 0, 0, 1},
		{"zero_k", 1, 3, 0},
		{"successes_exceed_n", 4, 3, 1},
		{"negative_successes", -1, 3, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := PassAtK(tc.successes, tc.n, tc.k); err == nil {
				t.Fatal("PassAtK: expected an error")
			}
			if _, err := PassPowerK(tc.successes, tc.n, tc.k); err == nil {
				t.Fatal("PassPowerK: expected an error")
			}
		})
	}
}

func TestWilsonInterval(t *testing.T) {
	t.Run("bounds_stay_in_range_at_extremes", func(t *testing.T) {
		lo, hi := WilsonInterval(0, 5, DefaultZ)
		if lo != 0 {
			t.Fatalf("lo=%v want 0 when nothing passed", lo)
		}
		if hi <= 0 || hi >= 1 {
			t.Fatalf("hi=%v should be inside (0,1)", hi)
		}
		lo, hi = WilsonInterval(5, 5, DefaultZ)
		if hi != 1 {
			t.Fatalf("hi=%v want 1 when everything passed", hi)
		}
		if lo <= 0 || lo >= 1 {
			t.Fatalf("lo=%v should be inside (0,1)", lo)
		}
	})

	t.Run("interval_narrows_as_n_grows", func(t *testing.T) {
		loSmall, hiSmall := WilsonInterval(5, 10, DefaultZ)
		loBig, hiBig := WilsonInterval(50, 100, DefaultZ)
		if (hiBig - loBig) >= (hiSmall - loSmall) {
			t.Fatalf("interval did not narrow: n=10 width %v, n=100 width %v",
				hiSmall-loSmall, hiBig-loBig)
		}
	})

	t.Run("brackets_the_point_estimate", func(t *testing.T) {
		lo, hi := WilsonInterval(3, 10, DefaultZ)
		if lo > 0.3 || hi < 0.3 {
			t.Fatalf("interval [%v,%v] does not contain 0.3", lo, hi)
		}
	})

	t.Run("no_samples", func(t *testing.T) {
		if lo, hi := WilsonInterval(0, 0, DefaultZ); lo != 0 || hi != 0 {
			t.Fatalf("got [%v,%v] want [0,0]", lo, hi)
		}
	})
}

func TestMinSamplesWarning(t *testing.T) {
	if got := MinSamplesWarning(MinRecommendedSamples); got != "" {
		t.Fatalf("no warning expected at the threshold, got %q", got)
	}
	if got := MinSamplesWarning(2); got == "" {
		t.Fatal("expected a warning below the threshold")
	}
}

func TestSummarize(t *testing.T) {
	got := Summarize([]float64{10, 20, 30, 40})
	if got.Min != 10 || got.Max != 40 {
		t.Fatalf("min=%v max=%v want 10/40", got.Min, got.Max)
	}
	if got.Mean != 25 {
		t.Fatalf("mean=%v want 25", got.Mean)
	}
	// Nearest-rank: p50 of four samples is the 2nd, p95 is the 4th.
	if got.P50 != 20 {
		t.Fatalf("p50=%v want 20", got.P50)
	}
	if got.P95 != 40 {
		t.Fatalf("p95=%v want 40", got.P95)
	}
	if empty := Summarize(nil); empty != (Percentiles{}) {
		t.Fatalf("empty input should give a zero value, got %+v", empty)
	}
}
