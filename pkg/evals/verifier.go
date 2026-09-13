package evals

import (
	"context"
	"fmt"
	"strings"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
)

// VerifierKind identifies which acceptance axis a Check belongs to.
type VerifierKind string

const (
	KindOutcome      VerifierKind = "outcome"
	KindProcess      VerifierKind = "process"
	KindSafety       VerifierKind = "safety"
	KindReliability  VerifierKind = "reliability"
	KindPerformance  VerifierKind = "performance"
	KindSubjective   VerifierKind = "subjective"
)

// Check is one verifier's judgment.
type Check struct {
	Name   string       `json:"name"`
	Kind   VerifierKind `json:"kind"`
	Passed bool         `json:"passed"`
	// Score is 1 or 0 for deterministic verifiers and graded in [0,1] for
	// subjective judges.
	Score  float64 `json:"score"`
	Detail string  `json:"detail,omitempty"`
}

// OutcomeVerifier checks the state the run left behind. It reads through
// execenv.ExecutionEnv rather than os directly so a verifier is subject to the
// same workspace confinement as the agent it grades.
type OutcomeVerifier interface {
	Name() string
	Verify(ctx context.Context, env execenv.ExecutionEnv, r *Result) Check
}

// ProcessVerifier checks how the run reached its result, reading the
// trajectory rather than the workspace.
type ProcessVerifier interface {
	Name() string
	Verify(ctx context.Context, r *Result) Check
}

// SafetyVerifier is a hard gate: a fluent answer must never compensate for a
// policy, sandbox, or secret-handling violation.
type SafetyVerifier interface {
	Name() string
	Verify(ctx context.Context, r *Result) Check
}

// ReliabilityVerifier checks restart, replay, cancellation, and concurrency
// contracts that are independent of whether the workspace is correct.
type ReliabilityVerifier interface {
	Name() string
	Verify(ctx context.Context, r *Result) Check
}

// PerformanceVerifier checks latency, token, cost, output, and call budgets.
type PerformanceVerifier interface {
	Name() string
	Verify(ctx context.Context, r *Result) Check
}

type outcomeFunc struct {
	name string
	fn   func(ctx context.Context, env execenv.ExecutionEnv, r *Result) (bool, string)
}

func (o outcomeFunc) Name() string { return o.name }

func (o outcomeFunc) Verify(ctx context.Context, env execenv.ExecutionEnv, r *Result) Check {
	if r == nil {
		return Check{Name: o.name, Kind: KindOutcome, Detail: "nil result"}
	}
	passed, detail := o.fn(ctx, env, r)
	return Check{Name: o.name, Kind: KindOutcome, Passed: passed, Score: boolScore(passed), Detail: detail}
}

// OutcomeFunc adapts a plain predicate into an OutcomeVerifier.
func OutcomeFunc(name string, fn func(ctx context.Context, env execenv.ExecutionEnv, r *Result) (bool, string)) OutcomeVerifier {
	return outcomeFunc{name: name, fn: fn}
}

type processFunc struct {
	name string
	fn   func(r *Result) (bool, string)
}

func (p processFunc) Name() string { return p.name }

func (p processFunc) Verify(_ context.Context, r *Result) Check {
	if r == nil {
		return Check{Name: p.name, Kind: KindProcess, Detail: "nil result"}
	}
	passed, detail := p.fn(r)
	return Check{Name: p.name, Kind: KindProcess, Passed: passed, Score: boolScore(passed), Detail: detail}
}

// ProcessFunc adapts a plain predicate into a ProcessVerifier.
func ProcessFunc(name string, fn func(r *Result) (bool, string)) ProcessVerifier {
	return processFunc{name: name, fn: fn}
}

type axisFunc struct {
	name string
	kind VerifierKind
	fn   func(r *Result) (bool, string)
}

func (a axisFunc) Name() string { return a.name }

func (a axisFunc) Verify(_ context.Context, r *Result) Check {
	if r == nil {
		return Check{Name: a.name, Kind: a.kind, Detail: "nil result"}
	}
	passed, detail := a.fn(r)
	return Check{Name: a.name, Kind: a.kind, Passed: passed, Score: boolScore(passed), Detail: detail}
}

// SafetyFunc adapts a predicate into a SafetyVerifier.
func SafetyFunc(name string, fn func(r *Result) (bool, string)) SafetyVerifier {
	return axisFunc{name: name, kind: KindSafety, fn: fn}
}

// ReliabilityFunc adapts a predicate into a ReliabilityVerifier.
func ReliabilityFunc(name string, fn func(r *Result) (bool, string)) ReliabilityVerifier {
	return axisFunc{name: name, kind: KindReliability, fn: fn}
}

// PerformanceFunc adapts a predicate into a PerformanceVerifier.
func PerformanceFunc(name string, fn func(r *Result) (bool, string)) PerformanceVerifier {
	return axisFunc{name: name, kind: KindPerformance, fn: fn}
}

func boolScore(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// --- outcome verifiers ---

// FileEquals passes when path holds exactly want, ignoring surrounding
// whitespace.
//
// The expectation is part of the verifier's name, not just its behaviour: a
// task's fingerprint is built from verifier names, so a check whose name hid
// what it expected would let an edited expectation pass as the same task.
func FileEquals(path, want string) OutcomeVerifier {
	return OutcomeFunc(fmt.Sprintf("file_equals(%s == %q)", path, truncate(want, 60)),
		func(ctx context.Context, env execenv.ExecutionEnv, _ *Result) (bool, string) {
			got, err := env.ReadTextFile(ctx, path)
			if err != nil {
				return false, fmt.Sprintf("read %s: %v", path, err)
			}
			if strings.TrimSpace(got) == strings.TrimSpace(want) {
				return true, ""
			}
			return false, fmt.Sprintf("%s = %q, want %q", path, truncate(got, 200), truncate(want, 200))
		})
}

// FileContains passes when path exists and contains substr.
func FileContains(path, substr string) OutcomeVerifier {
	return OutcomeFunc(fmt.Sprintf("file_contains(%s ~ %q)", path, truncate(substr, 60)),
		func(ctx context.Context, env execenv.ExecutionEnv, _ *Result) (bool, string) {
			got, err := env.ReadTextFile(ctx, path)
			if err != nil {
				return false, fmt.Sprintf("read %s: %v", path, err)
			}
			if strings.Contains(got, substr) {
				return true, ""
			}
			return false, fmt.Sprintf("%s does not contain %q", path, truncate(substr, 120))
		})
}

// FileAbsent passes when path does not exist. Use it to check that a run left
// forbidden ground alone.
func FileAbsent(path string) OutcomeVerifier {
	return OutcomeFunc(fmt.Sprintf("file_absent(%s)", path),
		func(ctx context.Context, env execenv.ExecutionEnv, _ *Result) (bool, string) {
			if _, err := env.Stat(ctx, path); err != nil {
				return true, ""
			}
			return false, fmt.Sprintf("%s exists but should not", path)
		})
}

// CommandSucceeds passes when command exits 0 in the workspace. This is how a
// task asserts "the tests pass" without trusting the agent's claim that they
// do.
func CommandSucceeds(command string) OutcomeVerifier {
	return OutcomeFunc(fmt.Sprintf("command_succeeds(%s)", command),
		func(ctx context.Context, env execenv.ExecutionEnv, _ *Result) (bool, string) {
			res, err := env.Exec(ctx, command, execenv.ExecOptions{})
			if err != nil {
				return false, fmt.Sprintf("exec: %v", err)
			}
			if res.TimedOut {
				return false, "command timed out"
			}
			if res.ExitCode == 0 {
				return true, ""
			}
			return false, fmt.Sprintf("exit %d: %s", res.ExitCode,
				truncate(strings.TrimSpace(res.Stderr+res.Stdout), 300))
		})
}

// FinalAnswerEquals passes when the run's final output matches want after
// trimming.
func FinalAnswerEquals(want string) OutcomeVerifier {
	return OutcomeFunc(fmt.Sprintf("final_answer_equals(%q)", truncate(want, 60)),
		func(_ context.Context, _ execenv.ExecutionEnv, r *Result) (bool, string) {
			if strings.TrimSpace(r.Output) == strings.TrimSpace(want) {
				return true, ""
			}
			return false, fmt.Sprintf("output = %q, want %q", truncate(r.Output, 200), want)
		})
}

// FinalAnswerContains passes when the run's final output contains substr.
func FinalAnswerContains(substr string) OutcomeVerifier {
	return OutcomeFunc(fmt.Sprintf("final_answer_contains(%q)", truncate(substr, 60)),
		func(_ context.Context, _ execenv.ExecutionEnv, r *Result) (bool, string) {
			if strings.Contains(r.Output, substr) {
				return true, ""
			}
			return false, fmt.Sprintf("output %q does not contain %q", truncate(r.Output, 200), substr)
		})
}

// --- process verifiers ---

// ToolUsed passes when the run called name at least once.
func ToolUsed(name string) ProcessVerifier {
	return ProcessFunc(fmt.Sprintf("tool_used(%s)", name), func(r *Result) (bool, string) {
		if n := countToolCalls(r, name); n > 0 {
			return true, ""
		}
		return false, fmt.Sprintf("%s was never called", name)
	})
}

// ToolNotUsed passes when the run never called name.
func ToolNotUsed(name string) ProcessVerifier {
	return ProcessFunc(fmt.Sprintf("tool_not_used(%s)", name), func(r *Result) (bool, string) {
		if n := countToolCalls(r, name); n == 0 {
			return true, ""
		}
		return false, fmt.Sprintf("%s was called", name)
	})
}

// ToolCallOrder passes when the named tools appear in this relative order.
// Other calls may be interleaved; only the sequence of these names matters.
func ToolCallOrder(names ...string) ProcessVerifier {
	return ProcessFunc(fmt.Sprintf("tool_call_order(%s)", strings.Join(names, "->")),
		func(r *Result) (bool, string) {
			want := 0
			for _, step := range r.Trajectory() {
				if want >= len(names) {
					break
				}
				if step.Kind == StepToolCall && step.ToolName == names[want] {
					want++
				}
			}
			if want == len(names) {
				return true, ""
			}
			return false, fmt.Sprintf("stopped waiting for %q after matching %d of %d",
				names[want], want, len(names))
		})
}

// MaxToolCalls passes when the run used at most n tool calls, bounding an
// otherwise-correct run that flailed to get there.
func MaxToolCalls(n int) ProcessVerifier {
	return ProcessFunc(fmt.Sprintf("max_tool_calls(%d)", n), func(r *Result) (bool, string) {
		got := countToolCalls(r, "")
		if got <= n {
			return true, ""
		}
		return false, fmt.Sprintf("%d tool calls, limit %d", got, n)
	})
}

// NoToolErrors passes when no tool call returned an error.
func NoToolErrors() ProcessVerifier {
	return ProcessFunc("no_tool_errors", func(r *Result) (bool, string) {
		for _, step := range r.Trajectory() {
			if step.Kind == StepToolResult && step.IsError {
				return false, fmt.Sprintf("step %d: %s failed: %s",
					step.Index, step.ToolName, truncate(step.Content, 200))
			}
		}
		return true, ""
	})
}

// ToolResultsWithinBytes verifies the harness recorded and enforced its
// result bound. It catches regressions where a tool can flood the next model
// request despite the configured limit.
func ToolResultsWithinBytes(max int) ProcessVerifier {
	return ProcessFunc(fmt.Sprintf("tool_results_within_bytes(%d)", max), func(r *Result) (bool, string) {
		for _, step := range r.Trajectory() {
			if step.Kind != StepToolResult {
				continue
			}
			if step.OutputBytes > max && !step.Truncated {
				return false, fmt.Sprintf("step %d: %s recorded %d bytes without truncation", step.Index, step.ToolName, step.OutputBytes)
			}
			if len(step.Content) > max {
				return false, fmt.Sprintf("step %d: %s exposed %d bytes, limit %d", step.Index, step.ToolName, len(step.Content), max)
			}
		}
		return true, ""
	})
}

// ToolTruncationsHaveArtifact passes when every truncated tool result points
// at a spilled artifact rather than dropping the full payload.
func ToolTruncationsHaveArtifact() ProcessVerifier {
	return ProcessFunc("tool_truncations_have_artifact", func(r *Result) (bool, string) {
		for _, step := range r.Trajectory() {
			if step.Kind != StepToolResult || !step.Truncated {
				continue
			}
			if step.ArtifactPath == "" {
				return false, fmt.Sprintf("step %d: %s truncated without an artifact", step.Index, step.ToolName)
			}
		}
		return true, ""
	})
}

// ToolSucceeded passes when name was called at least once and that call's
// result was not an error. "Called once" is not the same as "successfully satisfied."
func ToolSucceeded(name string) ProcessVerifier {
	return ProcessFunc(fmt.Sprintf("tool_succeeded(%s)", name), func(r *Result) (bool, string) {
		called := map[string]bool{}
		for _, step := range r.Trajectory() {
			if step.Kind == StepToolCall && step.ToolName == name {
				called[step.ToolCallID] = true
			}
			if step.Kind == StepToolResult && !step.IsError {
				if step.ToolName == name || called[step.ToolCallID] {
					return true, ""
				}
			}
		}
		if len(called) == 0 {
			return false, fmt.Sprintf("%s was never called", name)
		}
		return false, fmt.Sprintf("%s was called but never succeeded", name)
	})
}

// OperationOutcome passes when the durable operation finished with want.
func OperationOutcome(want string) ReliabilityVerifier {
	return ReliabilityFunc(fmt.Sprintf("operation_outcome(%s)", want), func(r *Result) (bool, string) {
		got := ""
		for _, rec := range r.Records {
			if rec.Type == "operation_finished" {
				got = rec.Outcome
			}
		}
		if got == want {
			return true, ""
		}
		if got == "" {
			return false, "no operation_finished record"
		}
		return false, fmt.Sprintf("outcome=%s want %s", got, want)
	})
}

// ExpectedToolSet passes when every named tool was used at least once.
func ExpectedToolSet(names ...string) ProcessVerifier {
	return ProcessFunc("expected_tool_set("+strings.Join(names, ",")+")", func(r *Result) (bool, string) {
		for _, name := range names {
			if countToolCalls(r, name) == 0 {
				return false, name + " was never called"
			}
		}
		return true, ""
	})
}

// ForbiddenToolSet passes when none of the named tools were used.
func ForbiddenToolSet(names ...string) ProcessVerifier {
	return ProcessFunc("forbidden_tool_set("+strings.Join(names, ",")+")", func(r *Result) (bool, string) {
		for _, name := range names {
			if countToolCalls(r, name) > 0 {
				return false, name + " was called"
			}
		}
		return true, ""
	})
}

// ExactToolOrder passes when the tool-call sequence is exactly names.
func ExactToolOrder(names ...string) ProcessVerifier {
	return ProcessFunc("exact_tool_order("+strings.Join(names, "->")+")", func(r *Result) (bool, string) {
		var got []string
		for _, step := range r.Trajectory() {
			if step.Kind == StepToolCall {
				got = append(got, step.ToolName)
			}
		}
		if len(got) != len(names) {
			return false, fmt.Sprintf("got %v want %v", got, names)
		}
		for i := range names {
			if got[i] != names[i] {
				return false, fmt.Sprintf("got %v want %v", got, names)
			}
		}
		return true, ""
	})
}

// ToolArgsEqual passes when a call to name has arguments deep-equal to want.
func ToolArgsEqual(name string, want map[string]any) ProcessVerifier {
	return ProcessFunc("tool_args_equal("+name+")", func(r *Result) (bool, string) {
		for _, step := range r.Trajectory() {
			if step.Kind == StepToolCall && step.ToolName == name && argsEqual(step.Arguments, want) {
				return true, ""
			}
		}
		return false, name + " was not called with the expected arguments"
	})
}

// ToolArgsSubset passes when a call to name includes every key in want.
func ToolArgsSubset(name string, want map[string]any) ProcessVerifier {
	return ProcessFunc("tool_args_subset("+name+")", func(r *Result) (bool, string) {
		for _, step := range r.Trajectory() {
			if step.Kind != StepToolCall || step.ToolName != name {
				continue
			}
			ok := true
			for k, v := range want {
				if !argEqual(step.Arguments[k], v) {
					ok = false
					break
				}
			}
			if ok {
				return true, ""
			}
		}
		return false, name + " missing required argument subset"
	})
}

// RequiredArgsPresent passes when every call to name includes keys.
func RequiredArgsPresent(name string, keys ...string) ProcessVerifier {
	return ProcessFunc("required_args_present("+name+")", func(r *Result) (bool, string) {
		for _, step := range r.Trajectory() {
			if step.Kind != StepToolCall || step.ToolName != name {
				continue
			}
			for _, k := range keys {
				if _, ok := step.Arguments[k]; !ok {
					return false, name + " missing " + k
				}
			}
		}
		return true, ""
	})
}

// NoUndeclaredArgs passes when no call to name includes keys outside allowed.
func NoUndeclaredArgs(name string, allowed ...string) ProcessVerifier {
	allow := map[string]bool{}
	for _, k := range allowed {
		allow[k] = true
	}
	return ProcessFunc("no_undeclared_args("+name+")", func(r *Result) (bool, string) {
		for _, step := range r.Trajectory() {
			if step.Kind != StepToolCall || step.ToolName != name {
				continue
			}
			for k := range step.Arguments {
				if !allow[k] {
					return false, name + " has undeclared argument " + k
				}
			}
		}
		return true, ""
	})
}

// MaxRetriesPerTool passes when name was called at most n times.
func MaxRetriesPerTool(name string, n int) ProcessVerifier {
	return ProcessFunc(fmt.Sprintf("max_retries_per_tool(%s,%d)", name, n), func(r *Result) (bool, string) {
		got := countToolCalls(r, name)
		if got <= n {
			return true, ""
		}
		return false, fmt.Sprintf("%s called %d times, limit %d", name, got, n)
	})
}

// MaxIdenticalCalls passes when no identical name+args pair exceeds n.
func MaxIdenticalCalls(n int) ProcessVerifier {
	return ProcessFunc(fmt.Sprintf("max_identical_calls(%d)", n), func(r *Result) (bool, string) {
		counts := map[string]int{}
		for _, step := range r.Trajectory() {
			if step.Kind != StepToolCall {
				continue
			}
			key := step.ToolName + ":" + fmt.Sprint(step.Arguments)
			counts[key]++
			if counts[key] > n {
				return false, step.ToolName + " repeated too often"
			}
		}
		return true, ""
	})
}

// ArtifactReferenceValid passes when every artifact_path stays under .golum/artifacts.
func ArtifactReferenceValid() ProcessVerifier {
	return ProcessFunc("artifact_reference_valid", func(r *Result) (bool, string) {
		for _, step := range r.Trajectory() {
			if step.ArtifactPath == "" {
				continue
			}
			if strings.Contains(step.ArtifactPath, "..") || strings.HasPrefix(step.ArtifactPath, "/") {
				return false, step.ArtifactPath
			}
			if !strings.HasPrefix(step.ArtifactPath, ".golum/artifacts/") {
				return false, step.ArtifactPath
			}
		}
		return true, ""
	})
}

func argsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range b {
		if !argEqual(a[k], v) {
			return false
		}
	}
	return true
}

func argEqual(a, b any) bool {
	return fmt.Sprint(a) == fmt.Sprint(b)
}

// NoPolicyViolation passes when the run attempted nothing policy refused.
func NoPolicyViolation() ProcessVerifier {
	return ProcessFunc("no_policy_violation", func(r *Result) (bool, string) {
		if len(r.Violations) == 0 {
			return true, ""
		}
		v := r.Violations[0]
		return false, fmt.Sprintf("%s (%s) and %d more", v.Reason, v.ToolName, len(r.Violations)-1)
	})
}

func countToolCalls(r *Result, name string) int {
	n := 0
	for _, step := range r.Trajectory() {
		if step.Kind != StepToolCall {
			continue
		}
		if name == "" || step.ToolName == name {
			n++
		}
	}
	return n
}

// --- adapters ---

// OutcomeFromJudge lifts an existing Judge into the outcome axis. A judge
// returning a graded score is treated as passing only at 1, matching the
// pass convention used elsewhere.
func OutcomeFromJudge(name string, j Judge) OutcomeVerifier {
	return OutcomeFunc(name, func(ctx context.Context, _ execenv.ExecutionEnv, r *Result) (bool, string) {
		score, err := j(ctx, r, r.Input)
		if err != nil {
			return false, fmt.Sprintf("judge error: %v", err)
		}
		return score.Value >= 1, score.Rationale
	})
}

// ProcessFromJudge lifts an existing Judge into the process axis, for the
// transcript-shaped judges such as ToolCalled.
func ProcessFromJudge(name string, j Judge) ProcessVerifier {
	return ProcessFunc(name, func(r *Result) (bool, string) {
		score, err := j(context.Background(), r, r.Input)
		if err != nil {
			return false, fmt.Sprintf("judge error: %v", err)
		}
		return score.Value >= 1, score.Rationale
	})
}

// scoreSubjective runs the subjective judges, which are graded rather than
// binary and never gate the deterministic axes.
func scoreSubjective(ctx context.Context, judges []Judge, r *Result) []Check {
	out := make([]Check, 0, len(judges))
	for i, j := range judges {
		name := fmt.Sprintf("judge[%d]", i)
		score, err := j(ctx, r, r.Input)
		if err != nil {
			// A judge that could not be reached is not evidence the agent
			// failed, but the run still needs a value; record it as a zero
			// with the reason attached rather than dropping the criterion.
			out = append(out, Check{
				Name: name, Kind: KindSubjective, Detail: fmt.Sprintf("judge unavailable: %v", err),
			})
			continue
		}
		out = append(out, Check{
			Name: name, Kind: KindSubjective, Passed: score.Value >= 1,
			Score: score.Value, Detail: score.Rationale,
		})
	}
	return out
}
