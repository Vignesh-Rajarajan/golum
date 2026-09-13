package evals

import (
	"context"
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
)

// tempEnv returns a real confined execution environment over a temp dir, so
// outcome verifiers are exercised against the same boundary the agent uses
// rather than a stub that cannot refuse anything.
func tempEnv(t *testing.T, files map[string]string) execenv.ExecutionEnv {
	t.Helper()
	env, err := execenv.NewOsExecutionEnv(t.TempDir())
	if err != nil {
		t.Fatalf("execenv: %v", err)
	}
	for path, content := range files {
		if err := env.WriteFile(context.Background(), path, content); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
	}
	return env
}

func TestFileEquals(t *testing.T) {
	env := tempEnv(t, map[string]string{"note.txt": "EVAL_OK\n"})
	ctx := context.Background()
	r := &Result{}

	if got := FileEquals("note.txt", "EVAL_OK").Verify(ctx, env, r); !got.Passed {
		t.Fatalf("expected a pass, got %+v", got)
	}
	got := FileEquals("note.txt", "SOMETHING_ELSE").Verify(ctx, env, r)
	if got.Passed {
		t.Fatal("expected a failure on differing content")
	}
	if !strings.Contains(got.Detail, "EVAL_OK") {
		t.Fatalf("detail should show what was found, got %q", got.Detail)
	}
	if got.Kind != KindOutcome {
		t.Fatalf("kind=%q want %q", got.Kind, KindOutcome)
	}

	// A missing file is a failure, not an error that aborts the eval.
	missing := FileEquals("nope.txt", "x").Verify(ctx, env, r)
	if missing.Passed {
		t.Fatal("a missing file should fail the check")
	}
}

func TestFileContainsAndAbsent(t *testing.T) {
	env := tempEnv(t, map[string]string{"a.txt": "hello world"})
	ctx := context.Background()
	r := &Result{}

	if got := FileContains("a.txt", "world").Verify(ctx, env, r); !got.Passed {
		t.Fatalf("expected a pass, got %+v", got)
	}
	if got := FileContains("a.txt", "goodbye").Verify(ctx, env, r); got.Passed {
		t.Fatal("expected a failure")
	}
	if got := FileAbsent("b.txt").Verify(ctx, env, r); !got.Passed {
		t.Fatalf("b.txt does not exist, so the check should pass: %+v", got)
	}
	if got := FileAbsent("a.txt").Verify(ctx, env, r); got.Passed {
		t.Fatal("a.txt exists, so the absence check should fail")
	}
}

func TestCommandSucceeds(t *testing.T) {
	env := tempEnv(t, nil)
	ctx := context.Background()
	r := &Result{}

	if got := CommandSucceeds("true").Verify(ctx, env, r); !got.Passed {
		t.Fatalf("expected a pass, got %+v", got)
	}
	got := CommandSucceeds("exit 3").Verify(ctx, env, r)
	if got.Passed {
		t.Fatal("a nonzero exit should fail the check")
	}
	if !strings.Contains(got.Detail, "exit 3") {
		t.Fatalf("detail should report the exit code, got %q", got.Detail)
	}
}

func TestFinalAnswerVerifiers(t *testing.T) {
	env := tempEnv(t, nil)
	ctx := context.Background()
	r := &Result{Output: "  Paris  "}

	if got := FinalAnswerEquals("Paris").Verify(ctx, env, r); !got.Passed {
		t.Fatalf("surrounding whitespace should not matter: %+v", got)
	}
	if got := FinalAnswerEquals("London").Verify(ctx, env, r); got.Passed {
		t.Fatal("expected a failure")
	}
	if got := FinalAnswerContains("ari").Verify(ctx, env, r); !got.Passed {
		t.Fatalf("expected a pass, got %+v", got)
	}
}

// processResult builds a run that wrote a file and then read it back.
func processResult() *Result {
	return &Result{Entries: []session.Entry{
		{ID: "e_user", Kind: session.EntryUserMessage},
		{ID: "e_a1", Kind: session.EntryAssistantMessage,
			Meta: map[string]any{"tool_calls": toolCalls("c1", "write_file")}},
		{ID: "e_r1", Kind: session.EntryToolResult,
			Meta: map[string]any{"tool_call_id": "c1", "is_error": false}, Content: "ok"},
		{ID: "e_a2", Kind: session.EntryAssistantMessage,
			Meta: map[string]any{"tool_calls": toolCalls("c2", "read_file")}},
		{ID: "e_r2", Kind: session.EntryToolResult,
			Meta: map[string]any{"tool_call_id": "c2", "is_error": false}, Content: "ok"},
	}}
}

func TestProcessVerifiers(t *testing.T) {
	ctx := context.Background()
	r := processResult()

	if got := ToolUsed("write_file").Verify(ctx, r); !got.Passed {
		t.Fatalf("write_file was called: %+v", got)
	}
	if got := ToolUsed("shell").Verify(ctx, r); got.Passed {
		t.Fatal("shell was never called")
	}
	if got := ToolNotUsed("shell").Verify(ctx, r); !got.Passed {
		t.Fatalf("expected a pass, got %+v", got)
	}
	if got := ToolNotUsed("write_file").Verify(ctx, r); got.Passed {
		t.Fatal("write_file was called, so ToolNotUsed should fail")
	}
	if got := MaxToolCalls(2).Verify(ctx, r); !got.Passed {
		t.Fatalf("two calls is within a limit of two: %+v", got)
	}
	if got := MaxToolCalls(1).Verify(ctx, r); got.Passed {
		t.Fatal("two calls should exceed a limit of one")
	}
	if got := NoToolErrors().Verify(ctx, r); !got.Passed {
		t.Fatalf("no tool failed: %+v", got)
	}
	if got := ToolResultsWithinBytes(2).Verify(ctx, r); !got.Passed {
		t.Fatalf("short tool results should be bounded: %+v", got)
	}
	if got := NoPolicyViolation().Verify(ctx, r); !got.Passed {
		t.Fatalf("no policy was violated: %+v", got)
	}
	if got := ToolCallOrder("write_file", "read_file").Verify(ctx, r); !got.Passed {
		t.Fatalf("the order matches: %+v", got)
	}
	// The reverse order never occurred.
	reversed := ToolCallOrder("read_file", "write_file").Verify(ctx, r)
	if reversed.Passed {
		t.Fatal("expected the reversed order to fail")
	}
	if !strings.Contains(reversed.Detail, "write_file") {
		t.Fatalf("detail should name the tool it was still waiting for, got %q", reversed.Detail)
	}
}

func TestToolResultsWithinBytesDetectsUnboundedResult(t *testing.T) {
	r := &Result{Entries: []session.Entry{
		{ID: "a", Kind: session.EntryAssistantMessage, Meta: map[string]any{"tool_calls": toolCalls("c", "shell")}},
		{ID: "r", Kind: session.EntryToolResult, Content: "too long", Meta: map[string]any{
			"tool_call_id": "c", "output_bytes": 8, "truncated": false,
		}},
	}}
	got := ToolResultsWithinBytes(4).Verify(context.Background(), r)
	if got.Passed || !strings.Contains(got.Detail, "without truncation") {
		t.Fatalf("expected unbounded result failure, got %+v", got)
	}
}

func TestToolResultsWithinBytesAllowsTruncatedPreview(t *testing.T) {
	r := &Result{Entries: []session.Entry{
		{ID: "a", Kind: session.EntryAssistantMessage, Meta: map[string]any{"tool_calls": toolCalls("c", "read_file")}},
		{ID: "r", Kind: session.EntryToolResult, Content: "head...tail", Meta: map[string]any{
			"tool_call_id": "c", "output_bytes": 80_000, "truncated": true,
		}},
	}}
	got := ToolResultsWithinBytes(32).Verify(context.Background(), r)
	if !got.Passed {
		t.Fatalf("truncated preview within the bound should pass: %+v", got)
	}
}

func TestToolTruncationsHaveArtifact(t *testing.T) {
	missing := &Result{Entries: []session.Entry{
		{ID: "a", Kind: session.EntryAssistantMessage, Meta: map[string]any{"tool_calls": toolCalls("c", "read_file")}},
		{ID: "r", Kind: session.EntryToolResult, Content: "preview", Meta: map[string]any{
			"tool_call_id": "c", "truncated": true,
		}},
	}}
	if got := ToolTruncationsHaveArtifact().Verify(context.Background(), missing); got.Passed {
		t.Fatal("expected missing artifact to fail")
	}
	ok := &Result{Entries: []session.Entry{
		{ID: "a", Kind: session.EntryAssistantMessage, Meta: map[string]any{"tool_calls": toolCalls("c", "read_file")}},
		{ID: "r", Kind: session.EntryToolResult, Content: "preview", Meta: map[string]any{
			"tool_call_id": "c", "truncated": true, "artifact_path": ".golum/artifacts/c.txt",
		}},
	}}
	if got := ToolTruncationsHaveArtifact().Verify(context.Background(), ok); !got.Passed {
		t.Fatalf("expected a pass: %+v", got)
	}
}

func TestSafetyFailureCannotBeLiftedByOutcomeOrJudge(t *testing.T) {
	task := Task{
		ID: "safe", Objective: "do it",
		Acceptance: AcceptanceCriteria{
			Outcome:    []OutcomeVerifier{FinalAnswerEquals("ok")},
			Safety:     []SafetyVerifier{SafetyFunc("must_fail", func(*Result) (bool, string) { return false, "leaked" })},
			Subjective: []Judge{Equals("ok")},
		},
	}
	result := &Result{Output: "ok"}
	run := Evaluate(context.Background(), task, result, nil)
	if !run.OutcomePassed {
		t.Fatal("outcome should pass")
	}
	if run.SafetyPassed || run.Passed() {
		t.Fatal("a fluent answer must not lift a safety failure")
	}
	if run.Attribution == nil || run.Attribution.Kind != FailureSafety {
		t.Fatalf("attribution=%+v", run.Attribution)
	}
}

func TestEvaluateFailsWhenToolResultExceedsBound(t *testing.T) {
	task := Task{
		ID: "bound", Objective: "read it",
		Acceptance: AcceptanceCriteria{
			Process: []ProcessVerifier{ToolUsed("read_file"), ToolResultsWithinBytes(4)},
		},
	}
	result := &Result{Entries: []session.Entry{
		{ID: "a", Kind: session.EntryAssistantMessage, Meta: map[string]any{"tool_calls": toolCalls("c", "read_file")}},
		{ID: "r", Kind: session.EntryToolResult, Content: "too long", Meta: map[string]any{
			"tool_call_id": "c", "output_bytes": 8, "truncated": false,
		}},
	}}
	run := Evaluate(context.Background(), task, result, nil)
	if run.ProcessPassed {
		t.Fatal("expected process failure for an unbounded tool result")
	}
}

// Order is relative, not adjacent: unrelated calls in between are fine.
func TestToolCallOrderAllowsInterleaving(t *testing.T) {
	r := &Result{Entries: []session.Entry{
		{ID: "e_a1", Kind: session.EntryAssistantMessage,
			Meta: map[string]any{"tool_calls": toolCalls("c1", "write_file")}},
		{ID: "e_r1", Kind: session.EntryToolResult, Meta: map[string]any{"tool_call_id": "c1"}},
		{ID: "e_a2", Kind: session.EntryAssistantMessage,
			Meta: map[string]any{"tool_calls": toolCalls("c2", "list_dir")}},
		{ID: "e_r2", Kind: session.EntryToolResult, Meta: map[string]any{"tool_call_id": "c2"}},
		{ID: "e_a3", Kind: session.EntryAssistantMessage,
			Meta: map[string]any{"tool_calls": toolCalls("c3", "read_file")}},
		{ID: "e_r3", Kind: session.EntryToolResult, Meta: map[string]any{"tool_call_id": "c3"}},
	}}
	if got := ToolCallOrder("write_file", "read_file").Verify(context.Background(), r); !got.Passed {
		t.Fatalf("an unrelated call in between should not break the order: %+v", got)
	}
}

func TestProcessVerifiersDetectFailures(t *testing.T) {
	r := processResult()
	r.Entries[4].Meta["is_error"] = true
	r.Violations = []PolicyViolation{{ToolName: "shell", Reason: ViolationDeniedByPolicy, StepIndex: -1}}

	if got := NoToolErrors().Verify(context.Background(), r); got.Passed {
		t.Fatal("a failing tool result should fail the check")
	}
	if got := NoPolicyViolation().Verify(context.Background(), r); got.Passed {
		t.Fatal("a recorded violation should fail the check")
	}
}

func TestNewProcessVerifiers(t *testing.T) {
	r := processResult()
	if got := ExpectedToolSet("write_file", "read_file").Verify(context.Background(), r); !got.Passed {
		t.Fatalf("%+v", got)
	}
	if got := ForbiddenToolSet("shell").Verify(context.Background(), r); !got.Passed {
		t.Fatalf("%+v", got)
	}
	if got := ExactToolOrder("write_file", "read_file").Verify(context.Background(), r); !got.Passed {
		t.Fatalf("%+v", got)
	}
	if got := ToolSucceeded("write_file").Verify(context.Background(), r); !got.Passed {
		t.Fatalf("%+v", got)
	}
	if got := ArtifactReferenceValid().Verify(context.Background(), r); !got.Passed {
		t.Fatalf("%+v", got)
	}
}

func TestVerifiersHandleNilResult(t *testing.T) {
	env := tempEnv(t, nil)
	if got := FileEquals("a", "b").Verify(context.Background(), env, nil); got.Passed {
		t.Fatal("a nil result must not pass")
	}
	if got := ToolUsed("x").Verify(context.Background(), nil); got.Passed {
		t.Fatal("a nil result must not pass")
	}
}

// A judge lifted into a deterministic axis must pass only at a full score:
// half credit is not a pass.
func TestOutcomeFromJudgeRequiresFullScore(t *testing.T) {
	env := tempEnv(t, nil)
	ctx := context.Background()
	graded := func(v float64) Judge {
		return func(context.Context, *Result, string) (Score, error) {
			return Score{Value: v, Rationale: "graded"}, nil
		}
	}
	if got := OutcomeFromJudge("j", graded(1)).Verify(ctx, env, &Result{}); !got.Passed {
		t.Fatal("a score of 1 should pass")
	}
	if got := OutcomeFromJudge("j", graded(0.9)).Verify(ctx, env, &Result{}); got.Passed {
		t.Fatal("partial credit should not pass a deterministic axis")
	}
}

// A judge that cannot be reached still has to produce a check, or the run
// would silently lose an acceptance criterion.
func TestScoreSubjectiveRecordsJudgeFailure(t *testing.T) {
	broken := func(context.Context, *Result, string) (Score, error) {
		return Score{}, context.DeadlineExceeded
	}
	checks := scoreSubjective(context.Background(), []Judge{broken}, &Result{})
	if len(checks) != 1 {
		t.Fatalf("got %d checks, want 1", len(checks))
	}
	if checks[0].Passed {
		t.Fatal("an unavailable judge must not pass")
	}
	if !strings.Contains(checks[0].Detail, "judge unavailable") {
		t.Fatalf("detail should say the judge was unavailable, got %q", checks[0].Detail)
	}
}
