package evals

import (
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/sashabaranov/go-openai"
)

func toolCalls(id, name string) []openai.ToolCall {
	return []openai.ToolCall{{ID: id, Function: openai.FunctionCall{Name: name, Arguments: "{}"}}}
}

// cleanResult is a trajectory with no tool errors and no violations.
func cleanResult() *Result {
	return &Result{Entries: []session.Entry{
		{ID: "e_user", Kind: session.EntryUserMessage, Content: "do it"},
		{ID: "e_asst", Kind: session.EntryAssistantMessage, Content: "done",
			Meta: map[string]any{"stop_reason": "stop"}},
	}}
}

func failedRun(r *Result, mutate func(*TaskRun)) *TaskRun {
	run := &TaskRun{
		Task:    Task{ID: "t", Acceptance: AcceptanceCriteria{Outcome: []OutcomeVerifier{FileEquals("a.txt", "x")}}},
		Result:  r,
		Metrics: ComputeMetrics(r),
	}
	run.Outcome = []Check{{Name: "file_equals(a.txt)", Kind: KindOutcome, Passed: false, Detail: "read a.txt: missing"}}
	run.ProcessPassed = true
	run.SafetyPassed = true
	run.ReliabilityPassed = true
	run.PerformancePassed = true
	if mutate != nil {
		mutate(run)
	}
	run.Attribution = AttributeFailure(run)
	return run
}

func TestAttributeFailureNilWhenDeterministicCriteriaHold(t *testing.T) {
	run := &TaskRun{
		Task: Task{ID: "t"}, Result: cleanResult(),
		OutcomePassed: true, ProcessPassed: true,
		SafetyPassed: true, ReliabilityPassed: true, PerformancePassed: true,
	}
	if got := AttributeFailure(run); got != nil {
		t.Fatalf("a passing run should not be attributed, got %+v", got)
	}
}

// A judge scoring low does not make a run attributable: subjective criteria
// inform the report, they do not decide it.
func TestAttributeFailureIgnoresSubjectiveFailure(t *testing.T) {
	run := &TaskRun{
		Task: Task{ID: "t"}, Result: cleanResult(),
		OutcomePassed: true, ProcessPassed: true,
		SafetyPassed: true, ReliabilityPassed: true, PerformancePassed: true,
		Subjective:     []Check{{Name: "judge[0]", Kind: KindSubjective, Passed: false}},
		ResponsePassed: false,
	}
	if got := AttributeFailure(run); got != nil {
		t.Fatalf("expected no attribution, got %+v", got)
	}
}

func TestAttributeFailureFindsFirstToolError(t *testing.T) {
	r := &Result{Entries: []session.Entry{
		{ID: "e_user", Kind: session.EntryUserMessage},
		{ID: "e_a1", Kind: session.EntryAssistantMessage,
			Meta: map[string]any{"tool_calls": toolCalls("c1", "write_file")}},
		{ID: "e_r1", Kind: session.EntryToolResult,
			Meta: map[string]any{"tool_call_id": "c1", "is_error": true}, Content: "disk on fire"},
		{ID: "e_a2", Kind: session.EntryAssistantMessage,
			Meta: map[string]any{"tool_calls": toolCalls("c2", "read_file")}},
		{ID: "e_r2", Kind: session.EntryToolResult,
			Meta: map[string]any{"tool_call_id": "c2", "is_error": true}, Content: "also broken"},
	}}
	run := failedRun(r, nil)
	got := run.Attribution
	if got == nil {
		t.Fatal("expected an attribution")
	}
	if got.Kind != FailureToolError {
		t.Fatalf("kind=%q want %q", got.Kind, FailureToolError)
	}
	// The first failure is what matters; everything after it may just be
	// fallout.
	if got.EntryID != "e_r1" {
		t.Fatalf("attributed to %q want the first failing result e_r1", got.EntryID)
	}
}

func TestAttributeFailurePrefersEarlierPolicyViolation(t *testing.T) {
	r := &Result{Entries: []session.Entry{
		{ID: "e_user", Kind: session.EntryUserMessage},
		{ID: "e_a1", Kind: session.EntryAssistantMessage,
			Meta: map[string]any{"tool_calls": toolCalls("c1", "shell")}},
		{ID: "e_r1", Kind: session.EntryToolResult,
			Meta: map[string]any{"tool_call_id": "c1", "is_error": true}, Content: "rejected"},
		{ID: "e_a2", Kind: session.EntryAssistantMessage,
			Meta: map[string]any{"tool_calls": toolCalls("c2", "write_file")}},
		{ID: "e_r2", Kind: session.EntryToolResult,
			Meta: map[string]any{"tool_call_id": "c2", "is_error": true}, Content: "boom"},
	}}
	r.Violations = []PolicyViolation{{
		ToolName: "shell", ToolCallID: "c1", Reason: ViolationDeniedByPolicy, StepIndex: -1,
	}}
	run := failedRun(r, nil)
	if run.Attribution.Kind != FailurePolicyViolation {
		t.Fatalf("kind=%q want %q", run.Attribution.Kind, FailurePolicyViolation)
	}
	if run.Attribution.ToolName != "shell" {
		t.Fatalf("tool=%q want shell", run.Attribution.ToolName)
	}
}

func TestAttributeFailureReportsGuardrail(t *testing.T) {
	r := cleanResult()
	r.Records = []session.Record{{
		Type: session.RecordOperationFinished, RunID: "run", Outcome: "failed",
		Error: &session.OpError{Code: "model_limit", Message: "model invocation limit exceeded (10)"},
	}}
	run := failedRun(r, nil)
	if run.Attribution.Kind != FailureGuardrail {
		t.Fatalf("kind=%q want %q", run.Attribution.Kind, FailureGuardrail)
	}
	if run.Attribution.Detail == "" {
		t.Fatal("guardrail attribution should carry the code and message")
	}
}

func TestAttributeFailureReportsWrongProcess(t *testing.T) {
	run := failedRun(cleanResult(), func(r *TaskRun) {
		r.ProcessPassed = false
		r.Process = []Check{{
			Name: "tool_used(write_file)", Kind: KindProcess,
			Passed: false, Detail: "write_file was never called",
		}}
	})
	if run.Attribution.Kind != FailureWrongProcess {
		t.Fatalf("kind=%q want %q", run.Attribution.Kind, FailureWrongProcess)
	}
}

func TestAttributeFailureReportsNoProgress(t *testing.T) {
	run := failedRun(cleanResult(), nil)
	if run.Attribution.Kind != FailureNoProgress {
		t.Fatalf("kind=%q want %q", run.Attribution.Kind, FailureNoProgress)
	}
}

// The interesting case: nothing in the trajectory looks wrong, the agent
// worked and declared itself done, but the world does not match the request.
func TestAttributeFailureReportsPrematureCompletion(t *testing.T) {
	r := &Result{Entries: []session.Entry{
		{ID: "e_user", Kind: session.EntryUserMessage},
		{ID: "e_a1", Kind: session.EntryAssistantMessage,
			Meta: map[string]any{"tool_calls": toolCalls("c1", "write_file")}},
		{ID: "e_r1", Kind: session.EntryToolResult,
			Meta: map[string]any{"tool_call_id": "c1", "is_error": false}, Content: "ok"},
		{ID: "e_a2", Kind: session.EntryAssistantMessage, Content: "all set",
			Meta: map[string]any{"stop_reason": "stop"}},
	}}
	run := failedRun(r, nil)
	if run.Attribution.Kind != FailurePrematureCompletion {
		t.Fatalf("kind=%q want %q", run.Attribution.Kind, FailurePrematureCompletion)
	}
	if run.Attribution.Detail == "" {
		t.Fatal("expected the failing outcome check in the detail")
	}
}

// Attribution feeds cross-run comparison, so the same trajectory must always
// produce the same answer.
func TestAttributeFailureIsDeterministic(t *testing.T) {
	r := &Result{Entries: []session.Entry{
		{ID: "e_user", Kind: session.EntryUserMessage},
		{ID: "e_a1", Kind: session.EntryAssistantMessage,
			Meta: map[string]any{"tool_calls": toolCalls("c1", "edit")}},
		{ID: "e_r1", Kind: session.EntryToolResult,
			Meta: map[string]any{"tool_call_id": "c1", "is_error": true}, Content: "nope"},
	}}
	first := failedRun(r, nil).Attribution
	for i := 0; i < 20; i++ {
		got := failedRun(r, nil).Attribution
		if *got != *first {
			t.Fatalf("attribution drifted on run %d: %+v vs %+v", i, got, first)
		}
	}
}
