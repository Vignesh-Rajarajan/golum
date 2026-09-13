package evals

import (
	"fmt"
	"os"
)

// RegressionKind classifies why a task's pass rate changed.
type RegressionKind string

const (
	RegressionCorrectness  RegressionKind = "correctness"
	RegressionProcess      RegressionKind = "process"
	RegressionSafety       RegressionKind = "safety"
	RegressionReliability  RegressionKind = "reliability"
	RegressionPerformance  RegressionKind = "performance"
	RegressionJudgeQuality RegressionKind = "judge_quality"
	RegressionDataset      RegressionKind = "dataset"
	RegressionInfra        RegressionKind = "infrastructure"
)

// Regression is one task-level delta against a baseline report.
type Regression struct {
	TaskID string         `json:"task_id"`
	Kind   RegressionKind `json:"kind"`
	Detail string         `json:"detail"`
}

// RegressionReport is the merge-gate comparison of two eval reports.
type RegressionReport struct {
	Regressions []Regression `json:"regressions"`
}

// Failed reports whether any real regression was found.
func (r RegressionReport) Failed() bool { return len(r.Regressions) > 0 }

// ExitCode is 1 when a real regression exists, else 0.
func (r RegressionReport) ExitCode() int {
	if r.Failed() {
		return 1
	}
	return 0
}

// CompareToBaseline classifies per-axis regressions. A TaskHash mismatch is a
// dataset change, not a behavioral regression.
func CompareToBaseline(prev, cur Report) RegressionReport {
	byID := map[string]TaskReport{}
	for _, t := range prev.Tasks {
		byID[t.TaskID] = t
	}
	var out RegressionReport
	for _, t := range cur.Tasks {
		old, ok := byID[t.TaskID]
		if !ok {
			continue
		}
		if old.TaskHash != "" && t.TaskHash != "" && old.TaskHash != t.TaskHash {
			out.Regressions = append(out.Regressions, Regression{
				TaskID: t.TaskID, Kind: RegressionDataset,
				Detail: fmt.Sprintf("hash %s -> %s", old.TaskHash, t.TaskHash),
			})
			continue
		}
		add := func(kind RegressionKind, oldRate, newRate float64, label string) {
			if newRate+1e-9 < oldRate {
				out.Regressions = append(out.Regressions, Regression{
					TaskID: t.TaskID, Kind: kind,
					Detail: fmt.Sprintf("%s %.2f -> %.2f", label, oldRate, newRate),
				})
			}
		}
		add(RegressionCorrectness, old.OutcomePassRate, t.OutcomePassRate, "outcome")
		add(RegressionProcess, old.ProcessPassRate, t.ProcessPassRate, "process")
		add(RegressionSafety, old.SafetyPassRate, t.SafetyPassRate, "safety")
		add(RegressionReliability, old.ReliabilityPassRate, t.ReliabilityPassRate, "reliability")
		add(RegressionPerformance, old.PerformancePassRate, t.PerformancePassRate, "performance")
		add(RegressionJudgeQuality, old.ResponsePassRate, t.ResponsePassRate, "response")
	}
	return out
}

// WriteExit writes a non-zero process status for merge gates. Tests should
// call ExitCode instead of this.
func (r RegressionReport) WriteExit() {
	os.Exit(r.ExitCode())
}
