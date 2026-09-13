package evals

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/google/uuid"
)

func buildID() string {
	if v := os.Getenv("GITHUB_SHA"); v != "" {
		return v
	}
	if v := os.Getenv("GOLUM_BUILD_ID"); v != "" {
		return v
	}
	return ""
}

// TaskRun is one execution of a Task, scored on all three acceptance axes.
// The axes stay separate all the way into the report: a run that wrote the
// right file by a forbidden route and a run that followed the rules but wrote
// nothing are both failures, and they need different fixes.
type TaskRun struct {
	Task   Task
	Result *Result
	// Err is the harness error, if any. A tripped guardrail or an unfinished
	// turn is a scored failure rather than a broken test, so it is recorded
	// here instead of being returned.
	Err error

	Outcome     []Check
	Process     []Check
	Safety      []Check
	Reliability []Check
	Performance []Check
	Subjective  []Check

	OutcomePassed      bool
	ProcessPassed      bool
	SafetyPassed       bool
	ReliabilityPassed  bool
	PerformancePassed  bool
	ResponsePassed     bool

	Metrics     Metrics
	Attribution *Attribution
}

// Passed reports whether every deterministic criterion held. Subjective
// judges are excluded on purpose: they inform the report, they do not decide
// it.
func (r *TaskRun) Passed() bool {
	return r.OutcomePassed && r.ProcessPassed && r.SafetyPassed &&
		r.ReliabilityPassed && r.PerformancePassed
}

// HarnessFor builds the eval harness a task's environment describes.
func HarnessFor(task Task) *Harness { return New(task.Options()) }

// RunTask seeds the task's initial state into a fresh workspace, runs it, and
// evaluates the acceptance criteria.
//
// It returns an error only when the run could not be set up at all. A run that
// started and then failed comes back as a TaskRun with Err set and zero
// scores, because that is a measurement, not a broken test.
func RunTask(ctx context.Context, t *testing.T, task Task) (*TaskRun, error) {
	t.Helper()
	if err := task.Validate(); err != nil {
		return nil, err
	}

	h := HarnessFor(task)
	runID := uuid.NewString()
	workspace := t.TempDir()

	env, err := execenv.NewOsExecutionEnv(workspace)
	if err != nil {
		return nil, err
	}
	if err := task.InitialState.Apply(ctx, env); err != nil {
		return nil, err
	}
	initial, _ := workspaceManifest(ctx, env)

	result, runErr := h.run(ctx, t, runID, workspace, env, nil, task.PromptSteps())
	if result == nil {
		return nil, fmt.Errorf("evals: task %q did not start: %w", task.ID, runErr)
	}
	result.DatasetVersion = task.Version
	result.TaskHash = TaskHash(task)
	result.Seed = SeedLabel()
	result.BuildID = buildID()
	result.InitialManifest = initial
	result.FinalManifest, _ = workspaceManifest(ctx, env)
	return Evaluate(ctx, task, result, runErr), nil
}

// Evaluate scores an existing Result against a task's acceptance criteria.
// Split out from RunTask so a replayed or reconstructed run can be scored the
// same way as a fresh one.
func Evaluate(ctx context.Context, task Task, result *Result, runErr error) *TaskRun {
	run := &TaskRun{Task: task, Result: result, Err: runErr}
	run.Metrics = ComputeMetrics(result)

	// Verifiers read the workspace through the same confined environment the
	// agent used. If it cannot be opened the outcome checks cannot run, and
	// silently reporting a pass would be the worst possible answer.
	env, envErr := execenv.NewOsExecutionEnv(result.Workspace)
	for _, v := range task.Acceptance.Outcome {
		if envErr != nil {
			run.Outcome = append(run.Outcome, Check{
				Name: v.Name(), Kind: KindOutcome,
				Detail: fmt.Sprintf("workspace unavailable: %v", envErr),
			})
			continue
		}
		run.Outcome = append(run.Outcome, v.Verify(ctx, env, result))
	}
	for _, v := range task.Acceptance.Process {
		run.Process = append(run.Process, v.Verify(ctx, result))
	}
	for _, v := range task.Acceptance.Safety {
		run.Safety = append(run.Safety, v.Verify(ctx, result))
	}
	for _, v := range task.Acceptance.Reliability {
		run.Reliability = append(run.Reliability, v.Verify(ctx, result))
	}
	for _, v := range task.Acceptance.Performance {
		run.Performance = append(run.Performance, v.Verify(ctx, result))
	}
	run.Subjective = scoreSubjective(ctx, task.Acceptance.Subjective, result)

	run.OutcomePassed = allPassed(run.Outcome)
	run.ProcessPassed = allPassed(run.Process)
	run.SafetyPassed = allPassed(run.Safety)
	run.ReliabilityPassed = allPassed(run.Reliability)
	run.PerformancePassed = allPassed(run.Performance)
	run.ResponsePassed = allPassed(run.Subjective)
	run.Attribution = AttributeFailure(run)
	return run
}

// allPassed treats an empty criteria list as satisfied: a task that states no
// process requirements has not failed its process requirements.
func allPassed(checks []Check) bool {
	for _, c := range checks {
		if !c.Passed {
			return false
		}
	}
	return true
}

// TaskRow is one cell of a comparative task table.
type TaskRow struct {
	EvalSet    string
	Name       string
	Task       Task
	Repetition int
}

// TaskTable expands a baseline and candidate environment over the same task
// into rows for table-driven subtests. Holding the task fixed and varying only
// the environment is what makes the resulting lift attributable to the change
// under test.
func TaskTable(evalSet string, task Task, baseline, candidate Environment, repetitions int) []TaskRow {
	if repetitions <= 0 {
		repetitions = 1
	}
	name := func(env Environment, fallback string) string {
		if env.Name != "" {
			return env.Name
		}
		return fallback
	}
	out := make([]TaskRow, 0, repetitions*2)
	for i := 1; i <= repetitions; i++ {
		out = append(out,
			TaskRow{EvalSet: evalSet, Name: name(baseline, "baseline"),
				Task: task.With(baseline), Repetition: i},
			TaskRow{EvalSet: evalSet, Name: name(candidate, "candidate"),
				Task: task.With(candidate), Repetition: i},
		)
	}
	return out
}
