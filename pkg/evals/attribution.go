package evals

import (
	"fmt"
)

// Failure categories, ordered roughly from "the agent did something wrong" to
// "the agent did nothing obviously wrong but still failed".
const (
	FailurePolicyViolation     = "policy_violation"
	FailureSafety              = "safety"
	FailureToolError           = "tool_error"
	FailureWrongProcess        = "wrong_process"
	FailureReliability         = "reliability"
	FailurePerformance         = "performance"
	FailureGuardrail           = "guardrail"
	FailureNoProgress          = "no_progress"
	FailurePrematureCompletion = "premature_completion"
)

// Attribution names the first point in a run where things went wrong. A pass
// rate says a task failed; this says where, which is what makes a regression
// triageable without reading transcripts by hand.
type Attribution struct {
	StepIndex int    `json:"step_index"`
	Kind      string `json:"kind"`
	ToolName  string `json:"tool_name,omitempty"`
	EntryID   string `json:"entry_id,omitempty"`
	Detail    string `json:"detail"`
}

// AttributeFailure returns the first incorrect step in a failed run, or nil
// when the run satisfied its deterministic criteria.
//
// The rules are deliberately mechanical rather than model-judged: attribution
// is used to compare runs across time, so it has to give the same answer for
// the same trajectory every time.
func AttributeFailure(run *TaskRun) *Attribution {
	if run == nil || run.Result == nil {
		return nil
	}
	if run.Passed() {
		return nil
	}
	steps := run.Result.Trajectory()

	// A violation or a tool error is a concrete wrong step, so whichever comes
	// first in the trajectory wins over any whole-run explanation.
	first := (*Attribution)(nil)
	consider := func(candidate *Attribution) {
		if candidate == nil {
			return
		}
		if first == nil || candidate.StepIndex < first.StepIndex {
			first = candidate
		}
	}

	for _, v := range run.Metrics.Violations {
		if v.StepIndex < 0 {
			continue
		}
		consider(&Attribution{
			StepIndex: v.StepIndex, Kind: FailurePolicyViolation, ToolName: v.ToolName,
			Detail: fmt.Sprintf("%s refused: %s", v.ToolName, v.Reason),
		})
	}
	for _, s := range steps {
		if s.Kind == StepToolResult && s.IsError {
			consider(&Attribution{
				StepIndex: s.Index, Kind: FailureToolError, ToolName: s.ToolName,
				EntryID: s.EntryID,
				Detail:  fmt.Sprintf("%s failed: %s", s.ToolName, truncate(s.Content, 200)),
			})
			break
		}
	}
	if first != nil {
		return first
	}

	// No single bad step. Fall back to whole-run explanations, most specific
	// first.
	if run.Metrics.GuardrailCode != "" {
		return &Attribution{
			StepIndex: lastIndex(steps), Kind: FailureGuardrail,
			Detail: fmt.Sprintf("%s: %s", run.Metrics.GuardrailCode, run.Metrics.GuardrailMessage),
		}
	}
	if !run.SafetyPassed {
		if c, ok := firstFailed(run.Safety); ok {
			return &Attribution{
				StepIndex: lastIndex(steps), Kind: FailureSafety,
				Detail: fmt.Sprintf("%s: %s", c.Name, c.Detail),
			}
		}
	}
	if !run.ProcessPassed {
		if c, ok := firstFailed(run.Process); ok {
			return &Attribution{
				StepIndex: lastIndex(steps), Kind: FailureWrongProcess,
				Detail: fmt.Sprintf("%s: %s", c.Name, c.Detail),
			}
		}
	}
	if !run.ReliabilityPassed {
		if c, ok := firstFailed(run.Reliability); ok {
			return &Attribution{
				StepIndex: lastIndex(steps), Kind: FailureReliability,
				Detail: fmt.Sprintf("%s: %s", c.Name, c.Detail),
			}
		}
	}
	if !run.PerformancePassed {
		if c, ok := firstFailed(run.Performance); ok {
			return &Attribution{
				StepIndex: lastIndex(steps), Kind: FailurePerformance,
				Detail: fmt.Sprintf("%s: %s", c.Name, c.Detail),
			}
		}
	}
	if run.Metrics.ToolCalls == 0 && len(run.Task.Acceptance.Outcome) > 0 {
		return &Attribution{
			StepIndex: lastIndex(steps), Kind: FailureNoProgress,
			Detail: "run ended without calling any tool despite outcome criteria",
		}
	}
	// The trajectory is clean and the run declared itself done, but the world
	// does not match what was asked for.
	detail := "outcome criteria not met"
	if c, ok := firstFailed(run.Outcome); ok {
		detail = fmt.Sprintf("%s: %s", c.Name, c.Detail)
	}
	return &Attribution{
		StepIndex: lastIndex(steps), Kind: FailurePrematureCompletion, Detail: detail,
	}
}

func firstFailed(checks []Check) (Check, bool) {
	for _, c := range checks {
		if !c.Passed {
			return c, true
		}
	}
	return Check{}, false
}

func lastIndex(steps []TrajectoryStep) int {
	if len(steps) == 0 {
		return -1
	}
	return len(steps) - 1
}
