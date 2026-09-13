package harness

import (
	"context"
	"sync"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/harnesstest"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/sashabaranov/go-openai"
)

func TestContractOperationStartIsDurableBeforeInference(t *testing.T) {
	r := NewTestRig(t)
	r.Model.Script(harnesstest.Turn{Content: "hi"})
	r.Prompt("hello")
	if _, err := r.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	open, err := r.Session.FindOpenOperations("main", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 {
		t.Fatalf("expected a durable operation start, got %d", len(open))
	}
	if r.Model.RequestCount() != 0 {
		t.Fatal("inference ran before operation_started was durable")
	}
}

func TestContractAssistantAttemptHasOneTerminalResult(t *testing.T) {
	r := NewTestRig(t)
	r.Model.Script(harnesstest.Turn{Content: "done"})
	r.Prompt("hello")
	if err := r.RunToCompletion(context.Background()); err != nil {
		t.Fatal(err)
	}
	attempts, results := 0, 0
	for _, rec := range r.Records() {
		if rec.Type == session.RecordStepAttempt && rec.Step == "assistant" {
			attempts++
			if _, ok := r.Session.GetEntry(rec.ResultEntryID); ok {
				results++
			}
		}
	}
	if attempts != 1 || results != 1 {
		t.Fatalf("attempts=%d results=%d", attempts, results)
	}
	r.AssertInvariants()
}

func TestContractToolCallHasStartAndResult(t *testing.T) {
	r := NewTestRig(t)
	r.Model.Script(
		harnesstest.Turn{ToolCalls: []harnesstest.ToolCall{{
			ID: "c1", Name: "read_file", Args: `{"path":"missing.txt"}`,
		}}},
		harnesstest.Turn{Content: "done"},
	)
	r.Prompt("read")
	if err := r.RunToCompletion(context.Background()); err != nil {
		t.Fatal(err)
	}
	starts, results := 0, 0
	for _, rec := range r.Records() {
		if rec.Type == session.RecordToolStarted {
			starts++
			if _, ok := r.Session.GetEntry(rec.ResultEntryID); ok {
				results++
			}
		}
	}
	if starts != 1 || results != 1 {
		t.Fatalf("starts=%d results=%d", starts, results)
	}
	r.AssertInvariants()
}

func TestContractDuplicateFinishedIsIdempotent(t *testing.T) {
	sess := driverSession()
	_, _ = sess.AppendRecord(session.Record{
		ID: "r_start", Type: session.RecordOperationStarted, RunID: "run",
		Intent: &session.OperationIntent{Kind: "run"},
	})
	first, err := sess.AppendRecord(session.Record{
		ID: "r_finish", Type: session.RecordOperationFinished, RunID: "run", Outcome: "completed",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := sess.AppendRecord(session.Record{
		ID: "r_finish", Type: session.RecordOperationFinished, RunID: "run", Outcome: "completed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Seq != second.Seq {
		t.Fatalf("duplicate finish must be a lookup, seq %d vs %d", first.Seq, second.Seq)
	}
	recs, _ := sess.FindRecords(session.RecordQuery{})
	finished := 0
	for _, rec := range recs {
		if rec.Type == session.RecordOperationFinished {
			finished++
		}
	}
	if finished != 1 {
		t.Fatalf("finished count=%d", finished)
	}
}

func TestContractOrphanToolResultRejected(t *testing.T) {
	err := ValidateRecordLog(RecordLogSlice{
		Entries: []session.Entry{{ID: "r", Kind: session.EntryToolResult, Meta: map[string]any{"tool_call_id": "missing"}}},
		Records: []session.Record{started(1, "run")},
	})
	var corruption *CorruptionError
	if err == nil || !asCorruption(err, &corruption) || corruption.Reason != CorruptionOrphanToolResult {
		t.Fatalf("got %v", err)
	}
}

func TestContractUnmatchedToolCallsRejectedWhenFinished(t *testing.T) {
	assistant := session.Entry{
		ID: "a", Seq: 2, Kind: session.EntryAssistantMessage,
		Meta: map[string]any{"tool_calls": []openai.ToolCall{{
			ID: "call", Function: openai.FunctionCall{Name: "read_file"},
		}}},
	}
	step := rec(2, session.RecordStepAttempt)
	step.Step, step.Attempt, step.ResultEntryID = "assistant", 1, "a"
	finish := rec(3, session.RecordOperationFinished)
	finish.Outcome = "completed"
	err := ValidateRecordLog(RecordLogSlice{
		Entries: []session.Entry{assistant},
		Records: []session.Record{started(1, "run"), step, finish},
	})
	var corruption *CorruptionError
	if err == nil || !asCorruption(err, &corruption) || corruption.Reason != CorruptionUnmatchedToolCalls {
		t.Fatalf("got %v", err)
	}
}

func TestContractMultipleOpenOperationsFailClosed(t *testing.T) {
	err := ValidateRecordLog(RecordLogSlice{
		Records: []session.Record{started(1, "one"), started(2, "two")},
	})
	var corruption *CorruptionError
	if err == nil || !asCorruption(err, &corruption) || corruption.Reason != CorruptionMultipleOpenOperations {
		t.Fatalf("got %v", err)
	}
}

func TestContractRecordSeqIsMonotonic(t *testing.T) {
	a := started(1, "run")
	b := rec(1, session.RecordAbortRequested)
	err := ValidateRecordLog(RecordLogSlice{Records: []session.Record{a, b}})
	var corruption *CorruptionError
	if err == nil || !asCorruption(err, &corruption) || corruption.Reason != CorruptionNonMonotonicSeq {
		t.Fatalf("got %v", err)
	}
}

func TestContractRecordIDsUniqueUnderConcurrency(t *testing.T) {
	sess := driverSession()
	var wg sync.WaitGroup
	errc := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := sess.AppendRecord(session.Record{
				Type: session.RecordUsage, RunID: "run", Cause: "test",
			})
			errc <- err
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	recs, _ := sess.FindRecords(session.RecordQuery{})
	seen := map[string]bool{}
	for _, rec := range recs {
		if seen[rec.ID] {
			t.Fatalf("duplicate id %s", rec.ID)
		}
		seen[rec.ID] = true
	}
}

func TestContractInvalidRecordTypeRejected(t *testing.T) {
	if err := (session.Record{ID: "x", Type: "nope"}).Validate(); err == nil {
		t.Fatal("expected invalid type to fail")
	}
}

func TestContractInvalidLaneRejected(t *testing.T) {
	r := started(1, "run")
	r.Lane = "sideways"
	err := ValidateRecordLog(RecordLogSlice{Records: []session.Record{r}})
	var corruption *CorruptionError
	if err == nil || !asCorruption(err, &corruption) || corruption.Reason != CorruptionInvalidLane {
		t.Fatalf("got %v", err)
	}
}

func TestContractReductionIsDeterministic(t *testing.T) {
	step := rec(2, session.RecordStepAttempt)
	step.Step, step.Attempt, step.ResultEntryID = "assistant", 1, "e"
	in := ReductionInput{Lane: "main", Records: []session.Record{started(1, "run"), step}}
	first, err := ReduceLaneState(in)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		got, err := ReduceLaneState(in)
		if err != nil {
			t.Fatal(err)
		}
		if (got.State.Operation == nil) != (first.State.Operation == nil) {
			t.Fatal("reduction drifted")
		}
		if got.State.Operation.Step.ResultEntryID != first.State.Operation.Step.ResultEntryID {
			t.Fatal("step drifted")
		}
	}
}

func TestContractReloadMatchesLiveContext(t *testing.T) {
	r := NewTestRig(t)
	r.Model.Script(harnesstest.Turn{Content: "hello"})
	r.Prompt("hi")
	if err := r.RunToCompletion(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _ := r.Session.BuildContext()
	r.Restart()
	after, _ := r.Session.BuildContext()
	if len(before) != len(after) {
		t.Fatalf("context size %d vs %d", len(before), len(after))
	}
	for i := range before {
		if before[i].Role != after[i].Role || before[i].Content != after[i].Content {
			t.Fatalf("message %d drifted", i)
		}
	}
}

func TestContractStoppedProcessIsNotCompleted(t *testing.T) {
	r := NewTestRig(t)
	r.Model.Script(harnesstest.Turn{Content: "never"})
	r.Prompt("hi")
	if _, err := r.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	outcome, _ := r.Outcome()
	if outcome == "completed" {
		t.Fatal("a stopped process must not appear completed")
	}
	open, err := r.Session.FindOpenOperations("main", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 {
		t.Fatalf("expected one open operation, got %d", len(open))
	}
}

func asCorruption(err error, into **CorruptionError) bool {
	if err == nil {
		return false
	}
	c, ok := err.(*CorruptionError)
	if !ok {
		return false
	}
	*into = c
	return true
}
