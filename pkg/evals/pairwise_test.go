package evals

import (
	"testing"
)

func TestWinnerFromPosition(t *testing.T) {
	cases := []struct {
		position string
		swapped  bool
		want     string
	}{
		{"1", false, VerdictA},
		{"2", false, VerdictB},
		// When the sides were swapped, "Response 1" was actually candidate B.
		{"1", true, VerdictB},
		{"2", true, VerdictA},
		{"tie", false, VerdictTie},
		{"tie", true, VerdictTie},
	}
	for _, tc := range cases {
		if got := winnerFromPosition(tc.position, tc.swapped); got != tc.want {
			t.Fatalf("winnerFromPosition(%q, swapped=%v)=%q want %q",
				tc.position, tc.swapped, got, tc.want)
		}
	}
}

func TestParsePairwiseJSON(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{"plain", `{"winner":"1","rationale":"clearer"}`, "1"},
		{"fenced", "```json\n{\"winner\": \"2\"}\n```", "2"},
		{"letter_form", `{"winner":"B"}`, "2"},
		{"tie", `{"winner":"tie"}`, "tie"},
		{"unknown_is_tie", `{"winner":"neither"}`, "tie"},
		{"prose_around_json", `Sure! {"winner":"1"} hope that helps`, "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := parsePairwiseJSON(tc.raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got != tc.want {
				t.Fatalf("position=%q want %q", got, tc.want)
			}
		})
	}
	if _, _, err := parsePairwiseJSON("no json here"); err == nil {
		t.Fatal("expected an error when the response holds no JSON")
	}
}

// A fixed seed has to produce a fixed sequence of orderings, or a comparative
// run cannot be reproduced.
func TestNewSeededRandIsReproducible(t *testing.T) {
	t.Setenv(SeedEnv, "12345")
	draw := func() []int {
		r := NewSeededRand("ignored-when-env-set")
		out := make([]int, 10)
		for i := range out {
			out[i] = r.Intn(2)
		}
		return out
	}
	first, second := draw(), draw()
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("draw %d differed: %v vs %v", i, first, second)
		}
	}
}

// Without an explicit seed the sequence still has to be stable across runs,
// and different labels must not share one ordering.
func TestNewSeededRandDerivesStableSeedFromLabel(t *testing.T) {
	t.Setenv(SeedEnv, "")
	a := NewSeededRand("task-one").Intn(1 << 30)
	b := NewSeededRand("task-one").Intn(1 << 30)
	if a != b {
		t.Fatalf("same label gave different seeds: %d vs %d", a, b)
	}
	if c := NewSeededRand("task-two").Intn(1 << 30); c == a {
		t.Fatal("different labels produced the same sequence")
	}
}

func TestTallyPairwiseCountsByIdentityNotPosition(t *testing.T) {
	// A wins every time, but was shown second in half the comparisons.
	verdicts := []PairwiseVerdict{
		{Winner: VerdictA, PresentedFirst: "cand"},
		{Winner: VerdictA, PresentedFirst: "base"},
		{Winner: VerdictA, PresentedFirst: "cand"},
		{Winner: VerdictA, PresentedFirst: "base"},
	}
	got := TallyPairwise("set", "base", "cand", verdicts)
	if got.AWins != 4 || got.BWins != 0 || got.Ties != 0 {
		t.Fatalf("tally = %+v, want 4 wins for A", got)
	}
	if got.FirstPositionWinRate != 0.5 {
		t.Fatalf("first-position win rate=%v want 0.5", got.FirstPositionWinRate)
	}
}

func TestTallyPairwiseFlagsPositionBias(t *testing.T) {
	// Whoever was shown first always won: the judge is grading position.
	verdicts := []PairwiseVerdict{
		{Winner: VerdictA, PresentedFirst: "base"},
		{Winner: VerdictB, PresentedFirst: "cand"},
		{Winner: VerdictA, PresentedFirst: "base"},
		{Winner: VerdictB, PresentedFirst: "cand"},
		{Winner: VerdictA, PresentedFirst: "base"},
	}
	got := TallyPairwise("set", "base", "cand", verdicts)
	if got.FirstPositionWinRate != 1 {
		t.Fatalf("first-position win rate=%v want 1", got.FirstPositionWinRate)
	}
	if len(got.Warnings) == 0 {
		t.Fatal("a fully position-determined result should warn")
	}
}

func TestTallyPairwiseIgnoresTiesInBiasRate(t *testing.T) {
	verdicts := []PairwiseVerdict{
		{Winner: VerdictTie, PresentedFirst: "base"},
		{Winner: VerdictTie, PresentedFirst: "cand"},
		{Winner: VerdictA, PresentedFirst: "base"},
	}
	got := TallyPairwise("set", "base", "cand", verdicts)
	if got.Ties != 2 {
		t.Fatalf("ties=%d want 2", got.Ties)
	}
	if got.FirstPositionWinRate != 1 {
		t.Fatalf("bias rate=%v want 1 over the single decisive verdict", got.FirstPositionWinRate)
	}
	if got.N != 3 {
		t.Fatalf("n=%d want 3", got.N)
	}
}

func TestTallyPairwiseWarnsOnSmallSamples(t *testing.T) {
	got := TallyPairwise("set", "base", "cand", []PairwiseVerdict{{Winner: VerdictA, PresentedFirst: "base"}})
	if len(got.Warnings) == 0 {
		t.Fatal("a single comparison should carry a sample-size warning")
	}
}
