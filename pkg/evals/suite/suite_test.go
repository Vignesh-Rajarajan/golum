//go:build evals

// Package suite_test runs the versioned task dataset end to end.
//
// It lives in its own package because pkg/evals/tasks/v1 imports pkg/evals: a
// test inside package evals cannot import the dataset without a cycle.
package suite_test

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/evals"
	v1 "github.com/Vignesh-Rajarajan/golum/pkg/evals/tasks/v1"
)

// rowPacing spaces out sequential model calls. Free-tier providers enforce a
// strict per-minute quota, and the whole suite runs rarely rather than in a
// tight loop, so this is deliberately conservative.
const rowPacing = 12 * time.Second

func TestDataset(t *testing.T) {
	requireAPIKey(t)

	split, err := evals.SelectedSplit()
	if err != nil {
		t.Fatal(err)
	}
	dataset := v1.Dataset()
	if err := dataset.Validate(); err != nil {
		t.Fatalf("dataset invalid: %v", err)
	}

	tasks := dataset.Filter(split)
	if only := os.Getenv("GOLUM_EVAL_TASK"); only != "" {
		tasks = filterByID(tasks, only)
	}
	if len(tasks) == 0 {
		t.Skipf("no tasks in split %q", split)
	}

	reps := envInt("GOLUM_EVAL_REPETITIONS", 3)
	k := envInt("GOLUM_EVAL_K", reps)
	first := true

	for _, task := range tasks {
		task := task
		t.Run(task.ID, func(t *testing.T) {
			runs := make([]*evals.TaskRun, 0, reps)
			for rep := 1; rep <= reps; rep++ {
				if !first {
					time.Sleep(rowPacing)
				}
				first = false

				run, err := evals.RunTask(context.Background(), t, task)
				if err != nil {
					// Only a setup failure is structural. Everything else is
					// a measurement.
					t.Fatalf("rep %d could not start: %v", rep, err)
				}
				logRun(t, rep, run)
				writeArtifact(t, run)
				runs = append(runs, run)
			}
			report := evals.SummarizeTask(runs, k)
			evals.RecordTaskReport(report)
			logReport(t, report)
		})
	}
}

// TestRitualSkillLift compares the same task with and without an authored
// skill. Holding the task fixed and varying only what the agent wakes up with
// is what makes the difference attributable to the skill.
func TestRitualSkillLift(t *testing.T) {
	requireAPIKey(t)

	dataset := v1.Dataset()
	base, ok := dataset.Get(v1.RitualTaskID)
	if !ok {
		t.Fatalf("%s missing from the dataset", v1.RitualTaskID)
	}
	const reps = 3
	rows := evals.TaskTable("ritual-skill-effectiveness", base,
		v1.RitualBaseline(), v1.RitualCandidate(), reps)

	scoresBy := map[string][]evals.Score{}
	for i, row := range rows {
		if i > 0 {
			time.Sleep(rowPacing)
		}
		task := row.Task
		if row.Name == v1.RitualCandidate().Name {
			task = v1.WithRitualSkill(task)
		}
		t.Run(fmt.Sprintf("%s/rep%d", row.Name, row.Repetition), func(t *testing.T) {
			run, err := evals.RunTask(context.Background(), t, task)
			if err != nil {
				t.Fatalf("could not start: %v", err)
			}
			logRun(t, row.Repetition, run)
			writeArtifact(t, run)

			// The deterministic verifiers are the score. A baseline failing is
			// the signal this comparison measures, not a broken test.
			score := evals.Score{Value: 0, Rationale: describe(run)}
			if run.Passed() {
				score.Value = 1
			}
			scoresBy[row.Name] = append(scoresBy[row.Name], score)
		})
	}

	report := evals.ComputeLift(rows[0].EvalSet,
		scoresBy[v1.RitualBaseline().Name], scoresBy[v1.RitualCandidate().Name])
	evals.RecordLift(report)
	t.Logf("\n%s", report.String())
}

func filterByID(tasks []evals.Task, id string) []evals.Task {
	var out []evals.Task
	for _, task := range tasks {
		if task.ID == id {
			out = append(out, task)
		}
	}
	return out
}

func envInt(name string, fallback int) int {
	if raw := os.Getenv(name); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func describe(run *evals.TaskRun) string {
	if run.Attribution != nil {
		return fmt.Sprintf("%s: %s", run.Attribution.Kind, run.Attribution.Detail)
	}
	return "passed"
}

func logRun(t *testing.T, rep int, run *evals.TaskRun) {
	t.Helper()
	t.Logf("rep=%d outcome=%v process=%v response=%v tools=%d model_calls=%d latency=%dms",
		rep, run.OutcomePassed, run.ProcessPassed, run.ResponsePassed,
		run.Metrics.ToolCalls, run.Metrics.ModelRequests, run.Metrics.LatencyMs)
	if run.Err != nil {
		t.Logf("rep=%d run error (scored as a failure): %v", rep, run.Err)
	}
	for _, c := range append(append([]evals.Check{}, run.Outcome...), run.Process...) {
		if !c.Passed {
			t.Logf("rep=%d FAILED %s (%s): %s", rep, c.Name, c.Kind, c.Detail)
		}
	}
	if run.Attribution != nil {
		t.Logf("rep=%d first bad step=%d kind=%s detail=%s",
			rep, run.Attribution.StepIndex, run.Attribution.Kind, run.Attribution.Detail)
	}
}

func logReport(t *testing.T, r evals.TaskReport) {
	t.Helper()
	t.Logf("outcome=%.0f%% process=%.0f%% response=%.0f%% pass@%d=%.2f pass^%d=%.2f ci=[%.2f,%.2f]",
		r.OutcomePassRate*100, r.ProcessPassRate*100, r.ResponsePassRate*100,
		r.K, r.PassAtK, r.K, r.PassPowerK, r.OutcomeCI[0], r.OutcomeCI[1])
	for _, w := range r.Warnings {
		t.Logf("warning: %s", w)
	}
}

func writeArtifact(t *testing.T, run *evals.TaskRun) {
	t.Helper()
	dir, err := evals.ArtifactDir()
	if err != nil {
		t.Fatalf("artifact dir: %v", err)
	}
	if err := evals.WriteTaskRunArtifact(dir, run); err != nil {
		t.Fatalf("artifact: %v", err)
	}
}

func requireAPIKey(t *testing.T) {
	t.Helper()
	if os.Getenv("OPENAI_API_KEY") == "" && os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Fatal("set OPENAI_API_KEY or OPENROUTER_API_KEY to run evals")
	}
}
