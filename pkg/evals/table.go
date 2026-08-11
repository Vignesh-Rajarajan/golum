package evals

import (
	"fmt"
	"strings"
	"sync"
)

// Row is one cell of a comparative harness table (baseline or candidate × repetition).
type Row struct {
	EvalSet    string
	Name       string
	Harness    *Harness
	Repetition int
}

// Table expands baseline and candidate harnesses into rows for table-driven subtests.
// Harness names must be stable; repetitions default to 1 when <= 0. evalSet is
// carried on every Row so callers can pass it straight to ComputeLift/RecordLift
// without holding a second copy of the string themselves.
func Table(evalSet string, baseline, candidate *Harness, repetitions int) []Row {
	if repetitions <= 0 {
		repetitions = 1
	}
	out := make([]Row, 0, repetitions*2)
	for i := 1; i <= repetitions; i++ {
		out = append(out, Row{EvalSet: evalSet, Name: baseline.Name, Harness: baseline, Repetition: i})
		out = append(out, Row{EvalSet: evalSet, Name: candidate.Name, Harness: candidate, Repetition: i})
	}
	return out
}

// LiftReport summarizes pass-rate lift of a candidate over a baseline.
type LiftReport struct {
	EvalSet                             string
	N                                   int
	BaselinePassRate, CandidatePassRate float64
	LiftPercentagePoints                float64
	BaselineAvgScore, CandidateAvgScore float64
}

// ComputeLift treats a score of Value >= 1 as a pass (matching pi's reporter).
func ComputeLift(evalSet string, baselineScores, candidateScores []Score) LiftReport {
	n := len(baselineScores)
	if len(candidateScores) < n {
		n = len(candidateScores)
	}
	r := LiftReport{EvalSet: evalSet, N: n}
	if n == 0 {
		return r
	}
	var basePass, candPass int
	var baseSum, candSum float64
	for i := 0; i < n; i++ {
		baseSum += baselineScores[i].Value
		candSum += candidateScores[i].Value
		if baselineScores[i].Value >= 1 {
			basePass++
		}
		if candidateScores[i].Value >= 1 {
			candPass++
		}
	}
	r.BaselinePassRate = float64(basePass) / float64(n)
	r.CandidatePassRate = float64(candPass) / float64(n)
	r.LiftPercentagePoints = (r.CandidatePassRate - r.BaselinePassRate) * 100
	r.BaselineAvgScore = baseSum / float64(n)
	r.CandidateAvgScore = candSum / float64(n)
	return r
}

func (r LiftReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "eval_set=%q n=%d\n", r.EvalSet, r.N)
	fmt.Fprintf(&b, "  baseline pass_rate=%.1f%% avg_score=%.3f\n", r.BaselinePassRate*100, r.BaselineAvgScore)
	fmt.Fprintf(&b, "  candidate pass_rate=%.1f%% avg_score=%.3f\n", r.CandidatePassRate*100, r.CandidateAvgScore)
	fmt.Fprintf(&b, "  lift=%+.1f pp\n", r.LiftPercentagePoints)
	return b.String()
}

var (
	liftMu      sync.Mutex
	liftReports []LiftReport
)

// RecordLift appends a comparative report for TestMain aggregation.
func RecordLift(r LiftReport) {
	liftMu.Lock()
	defer liftMu.Unlock()
	liftReports = append(liftReports, r)
}

// SnapshotLifts returns a copy of recorded lift reports (for tests / TestMain).
func SnapshotLifts() []LiftReport {
	liftMu.Lock()
	defer liftMu.Unlock()
	out := make([]LiftReport, len(liftReports))
	copy(out, liftReports)
	return out
}
