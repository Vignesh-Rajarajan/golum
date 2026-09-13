package evals

import "testing"

func TestCompareToBaselineClassifiesAxes(t *testing.T) {
	prev := Report{Tasks: []TaskReport{{
		TaskID: "t", TaskHash: "abc",
		OutcomePassRate: 1, ProcessPassRate: 1, SafetyPassRate: 1,
		ReliabilityPassRate: 1, PerformancePassRate: 1, ResponsePassRate: 1,
	}}}
	cur := Report{Tasks: []TaskReport{{
		TaskID: "t", TaskHash: "abc",
		OutcomePassRate: 0, ProcessPassRate: 1, SafetyPassRate: 0.5,
		ReliabilityPassRate: 1, PerformancePassRate: 1, ResponsePassRate: 0.2,
	}}}
	got := CompareToBaseline(prev, cur)
	if !got.Failed() || got.ExitCode() != 1 {
		t.Fatal("expected a failing report")
	}
	kinds := map[RegressionKind]bool{}
	for _, r := range got.Regressions {
		kinds[r.Kind] = true
	}
	if !kinds[RegressionCorrectness] || !kinds[RegressionSafety] || !kinds[RegressionJudgeQuality] {
		t.Fatalf("kinds=%v", kinds)
	}
}

func TestCompareToBaselineDatasetChange(t *testing.T) {
	prev := Report{Tasks: []TaskReport{{TaskID: "t", TaskHash: "old", OutcomePassRate: 1}}}
	cur := Report{Tasks: []TaskReport{{TaskID: "t", TaskHash: "new", OutcomePassRate: 0}}}
	got := CompareToBaseline(prev, cur)
	if len(got.Regressions) != 1 || got.Regressions[0].Kind != RegressionDataset {
		t.Fatalf("%+v", got.Regressions)
	}
}
