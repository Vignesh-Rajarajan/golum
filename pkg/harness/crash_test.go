package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/harnesstest"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
)

func crashScript() []harnesstest.Turn {
	return []harnesstest.Turn{
		{ToolCalls: []harnesstest.ToolCall{{
			ID: "c1", Name: "write_file", Args: `{"path":"note.txt","content":"EVAL_OK"}`,
		}}},
		{Content: "done"},
	}
}

func TestCrashMatrixRestartsCleanly(t *testing.T) {
	cases := []struct {
		name     string
		boundary harnesstest.Boundary
		before   bool
	}{
		{"after_operation_start", harnesstest.BoundaryOperationStarted, false},
		{"after_step_attempt", harnesstest.BoundaryStepAttempt, false},
		{"after_assistant_persisted", harnesstest.BoundaryAssistantPersisted, false},
		{"after_tool_started", harnesstest.BoundaryToolStarted, false},
		{"during_tool_execution", harnesstest.BoundaryDuringToolExecution, true},
		{"after_tool_result", harnesstest.BoundaryToolResultPersisted, false},
		{"before_operation_finish", harnesstest.BoundaryBeforeOperationFinish, true},
		{"after_operation_finish", harnesstest.BoundaryOperationFinished, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			baseline := NewTestRig(t)
			baseline.Model.Script(crashScript()...)
			baseline.Prompt("write note.txt")
			if err := baseline.RunToCompletion(context.Background()); err != nil {
				t.Fatalf("baseline: %v", err)
			}
			baseline.AssertInvariants()
			wantOutcome, _ := baseline.Outcome()

			r := NewTestRig(t)
			r.Model.Script(crashScript()...)
			r.Prompt("write note.txt")
			if tc.before {
				r.Fault.FailBefore(tc.boundary, 1)
			} else {
				r.Fault.FailAfter(tc.boundary, 1)
			}

			err := r.RunToCompletion(context.Background())
			if !errors.Is(err, harnesstest.ErrInjected) && tc.boundary != harnesstest.BoundaryOperationFinished {
				// OperationFinished is last; the write may succeed and then
				// fail after persist, which still returns ErrInjected.
				if err != nil && !errors.Is(err, harnesstest.ErrInjected) {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			if dups := harnesstest.DuplicateMessages(r.Session.Entries()); len(dups) > 0 {
				t.Fatalf("duplicates before restart: %v", dups)
			}

			r.Restart()
			r.Model.Script(crashScript()...)
			if err := r.RunToCompletion(context.Background()); err != nil {
				t.Fatalf("restart: %v", err)
			}
			r.AssertInvariants()
			if dups := harnesstest.DuplicateMessages(r.Session.Entries()); len(dups) > 0 {
				t.Fatalf("duplicates after restart: %v", dups)
			}
			got, _ := r.Outcome()
			if got != wantOutcome {
				t.Fatalf("outcome=%s want %s\n%s", got, wantOutcome, FormatRecords(r.Records()))
			}
			if _, err := os.Stat(filepath.Join(r.Root, "note.txt")); err != nil && wantOutcome == "completed" {
				t.Fatal("expected write_file to land")
			}
		})
	}
}

func TestCrashUnsafeToolIsNotReplayed(t *testing.T) {
	reg, todos := tool.DefaultRegistry(nil)
	r := NewTestRig(t, WithRigRegistry(reg, todos))
	r.Model.Script(
		harnesstest.Turn{ToolCalls: []harnesstest.ToolCall{{
			ID: "c1", Name: "write_file", Args: `{"path":"note.txt","content":"EVAL_OK"}`,
		}}},
		harnesstest.Turn{Content: "done"},
	)
	r.Prompt("write")
	r.Fault.FailAfter(harnesstest.BoundaryToolStarted, 1)
	_ = r.RunToCompletion(context.Background())
	r.Restart()
	r.Model.Script(harnesstest.Turn{Content: "recovered"})
	if err := r.RunToCompletion(context.Background()); err != nil {
		t.Fatal(err)
	}
	var interrupted bool
	for _, e := range r.Session.Entries() {
		if e.Kind == session.EntryToolResult && e.Content == "interrupted; not replayed" {
			interrupted = true
		}
	}
	if !interrupted {
		t.Fatal("unsafe tool must not replay after restart")
	}
}

func TestCrashCompactionPersistedRestarts(t *testing.T) {
	r := NewTestRig(t)
	r.Model.Script(
		harnesstest.Turn{Status: 400, ErrorBody: `{"error":{"message":"This model's maximum context length is 8192 tokens","code":"context_length_exceeded"}}`},
		harnesstest.Turn{NonStream: true, Content: "## ORIGINAL GOAL\nDo the thing."},
		harnesstest.Turn{Content: "recovered"},
	)
	r.Deps.Compactor = NewCompactor(r.Model.Client())
	r.rebuildDriver()
	for i := 0; i < 4; i++ {
		_, _ = r.Session.AppendUserMessage("context")
		_, _ = r.Session.AppendAssistantMessage("reply", nil)
	}
	r.Prompt("continue")
	r.Fault.FailAfter(harnesstest.BoundaryCompactionPersisted, 1)
	_ = r.RunToCompletion(context.Background())
	r.Restart()
	r.Deps.Compactor = NewCompactor(r.Model.Client())
	r.rebuildDriver()
	r.Model.Script(harnesstest.Turn{Content: "recovered"})
	if err := r.RunToCompletion(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.AssertInvariants()
}
