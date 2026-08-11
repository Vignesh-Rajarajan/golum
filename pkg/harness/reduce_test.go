package harness

import (
	"errors"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/sashabaranov/go-openai"
)

func rec(seq int64, typ session.RecordType) session.Record {
	return session.Record{ID: session.NewRecordID(), Lane: "main", Seq: seq, Type: typ, RunID: "run"}
}

func started(seq int64, run string) session.Record {
	r := rec(seq, session.RecordOperationStarted)
	r.RunID = run
	r.Intent = &session.OperationIntent{Kind: "run"}
	return r
}

func TestValidateRecordLogCorruptionReasons(t *testing.T) {
	target := session.ProvisionedEntry{
		ID: "queued", Kind: session.EntryUserMessage, Role: "user", Content: "queued",
	}
	assistant := session.Entry{
		ID: "assistant", Seq: 2, Kind: session.EntryAssistantMessage,
		Meta: map[string]any{"tool_calls": []openai.ToolCall{{
			ID: "call", Type: "function",
			Function: openai.FunctionCall{Name: "read_file", Arguments: `{}`},
		}}},
	}
	validTool := rec(3, session.RecordToolStarted)
	validTool.AssistantEntryID, validTool.ToolIndex = assistant.ID, 0
	validTool.ToolCallID, validTool.ToolName, validTool.ResultEntryID = "call", "read_file", "result"

	tests := []struct {
		name, reason string
		entries      []session.Entry
		records      []session.Record
	}{
		{"multiple open", CorruptionMultipleOpenOperations, nil,
			[]session.Record{started(1, "one"), started(2, "two")}},
		{"unknown operation", CorruptionUnknownOperation, nil,
			[]session.Record{func() session.Record { r := rec(1, session.RecordAbortRequested); r.RunID = "missing"; return r }()}},
		{"record after finish", CorruptionRecordAfterFinish, nil,
			[]session.Record{started(1, "run"), func() session.Record { r := rec(2, session.RecordOperationFinished); r.Outcome = "completed"; return r }(), rec(3, session.RecordAbortRequested)}},
		{"non consecutive attempt", CorruptionNonConsecutiveAttempt, nil,
			[]session.Record{started(1, "run"), func() session.Record {
				r := rec(2, session.RecordStepAttempt)
				r.Step, r.Attempt, r.ResultEntryID = "assistant", 2, "e"
				return r
			}()}},
		{"invalid compaction reason", CorruptionInvalidCompactionReason, nil,
			[]session.Record{started(1, "run"), func() session.Record {
				r := rec(2, session.RecordStepAttempt)
				r.Step, r.Attempt, r.ResultEntryID, r.CompactionReason = "compaction", 1, "e", "other"
				return r
			}()}},
		{"queue after abort", CorruptionQueueAfterAbort, nil,
			[]session.Record{started(1, "run"), rec(2, session.RecordAbortRequested), func() session.Record {
				r := rec(3, session.RecordQueueEnqueued)
				r.Queue, r.Target = "steer", &target
				return r
			}()}},
		{"invalid queue cancellation", CorruptionInvalidQueueCancellation, nil,
			[]session.Record{started(1, "run"), func() session.Record {
				r := rec(2, session.RecordQueueCancelled)
				r.Queue, r.EntryID = "steer", "missing"
				return r
			}()}},
		{"inconsistent step", CorruptionInconsistentStep, nil,
			[]session.Record{started(1, "run"),
				func() session.Record {
					r := rec(2, session.RecordStepAttempt)
					r.Step, r.Attempt, r.ResultEntryID = "assistant", 1, "same"
					return r
				}(),
				func() session.Record {
					r := rec(3, session.RecordStepAttempt)
					r.Step, r.Attempt, r.ResultEntryID, r.CompactionReason = "compaction", 1, "same", "manual"
					return r
				}()}},
		{"tool call mismatch", CorruptionToolCallMismatch, []session.Entry{assistant},
			[]session.Record{started(1, "run"), func() session.Record { r := validTool; r.ToolName = "shell"; return r }()}},
		{"duplicate tool invocation", CorruptionDuplicateToolInvocation, []session.Entry{assistant},
			[]session.Record{started(1, "run"), validTool, func() session.Record { r := validTool; r.ID = session.NewRecordID(); r.Seq = 4; return r }()}},
		{"provisioned mismatch", CorruptionProvisionedEntryMismatch,
			[]session.Entry{{ID: target.ID, Kind: target.Kind, Role: target.Role, Content: "different"}},
			[]session.Record{started(1, "run"), func() session.Record {
				r := rec(2, session.RecordQueueEnqueued)
				r.Queue, r.Target = "steer", &target
				return r
			}()}},
		{"invalid deferred handle", CorruptionInvalidDeferredHandle, nil,
			[]session.Record{started(1, "run"), rec(2, session.RecordWriteDeferred)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRecordLog(RecordLogSlice{Entries: tt.entries, Records: tt.records})
			var corruption *CorruptionError
			if !errors.As(err, &corruption) {
				t.Fatalf("expected corruption error, got %v", err)
			}
			if corruption.Reason != tt.reason {
				t.Fatalf("reason=%q want %q", corruption.Reason, tt.reason)
			}
		})
	}
}

func TestReduceLaneStateDerivesInflightStepByProvisionedID(t *testing.T) {
	step := rec(2, session.RecordStepAttempt)
	step.Step, step.Attempt, step.ResultEntryID = "assistant", 1, "answer"
	input := ReductionInput{Lane: "main", Records: []session.Record{started(1, "run"), step}}
	got, err := ReduceLaneState(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Operation == nil || got.State.Operation.Step == nil {
		t.Fatal("missing in-flight step")
	}

	input.Entries = []session.Entry{{ID: "answer", Seq: 3, Kind: session.EntryAssistantMessage}}
	got, err = ReduceLaneState(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Operation.Step != nil {
		t.Fatal("materialized result still treated as in-flight")
	}
}
