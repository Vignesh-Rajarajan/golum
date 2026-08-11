package harness

import (
	"sort"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/sashabaranov/go-openai"
)

type RecordLogSlice struct {
	Entries []session.Entry
	Records []session.Record
}

type ReductionInput struct {
	Lane    string
	LeafID  string
	Entries []session.Entry
	Records []session.Record
}

type ReductionResult struct{ State LaneState }

type LaneState struct {
	Lane           string
	LeafID         string
	Operation      *OperationState
	PendingNextRun []session.ProvisionedEntry
}

type OperationState struct {
	ID, Kind               string
	Intent                 session.OperationIntent
	Aborting               bool
	Step                   *StepState
	ToolBatch              *ToolBatchState
	MissingInitialMessages []session.ProvisionedEntry
	PendingSteer           []session.ProvisionedEntry
	PendingFollowUp        []session.ProvisionedEntry
	PendingWrites          []session.ProvisionedEntry
	OverflowRecoveryUsed   bool
	NewestOwn              *NewestOwn
	TerminalFailure        *session.OpError
	Targets                struct{ Result, Summary bool }
}

type StepState struct {
	Step, ResultEntryID, CompactionReason string
	Attempt                               int
	Record                                session.Record
}

type ToolBatchState struct {
	AssistantEntryID string
	Calls            []ToolCallState
	Truncated        bool
	Unresolved       bool
}

type ToolCallState struct {
	Index   int
	Call    openAIToolCall
	Started *session.Record
	Result  *session.Entry
}

// openAIToolCall keeps reducer state independent of provider request structs.
type openAIToolCall struct {
	ID, Name  string
	Arguments string
}

type NewestOwn struct {
	Entry session.Entry
	Seq   int64
}

func ReduceLaneState(in ReductionInput) (ReductionResult, error) {
	if in.Lane == "" {
		in.Lane = "main"
	}
	if err := ValidateRecordLog(RecordLogSlice{Entries: in.Entries, Records: in.Records}); err != nil {
		return ReductionResult{}, err
	}
	state := LaneState{Lane: in.Lane, LeafID: in.LeafID}
	entries := make(map[string]session.Entry, len(in.Entries))
	for _, e := range in.Entries {
		entries[e.ID] = e
	}
	records := append([]session.Record(nil), in.Records...)
	sort.SliceStable(records, func(i, j int) bool { return records[i].Seq < records[j].Seq })

	finished := map[string]bool{}
	var start *session.Record
	for i := range records {
		r := &records[i]
		if r.Lane != in.Lane {
			continue
		}
		if r.Type == session.RecordOperationFinished {
			finished[r.RunID] = true
		}
		if r.Type == session.RecordOperationStarted {
			start = r
		}
	}
	if start != nil && !finished[start.RunID] {
		op := &OperationState{ID: start.RunID, Kind: start.Intent.Kind, Intent: *start.Intent}
		state.Operation = op
		for _, p := range start.Intent.InitialMessages {
			if _, ok := entries[p.ID]; !ok {
				op.MissingInitialMessages = append(op.MissingInitialMessages, p)
			}
		}
		reduceOperation(op, start.Seq, records, entries, in.Entries)
	}
	state.PendingNextRun = reduceQueue("nextRun", "", records, entries)
	captured := map[string]bool{}
	for _, r := range records {
		if r.Type == session.RecordOperationStarted && r.Intent != nil {
			for _, p := range r.Intent.InitialMessages {
				captured[p.ID] = true
			}
		}
	}
	pending := state.PendingNextRun[:0]
	for _, p := range state.PendingNextRun {
		if !captured[p.ID] {
			pending = append(pending, p)
		}
	}
	state.PendingNextRun = pending
	return ReductionResult{State: state}, nil
}

func reduceOperation(op *OperationState, startSeq int64, records []session.Record, entries map[string]session.Entry, entryList []session.Entry) {
	var latestStep *session.Record
	for i := range records {
		r := &records[i]
		if r.RunID != op.ID || r.Seq < startSeq {
			continue
		}
		switch r.Type {
		case session.RecordAbortRequested:
			op.Aborting = true
		case session.RecordStepAttempt:
			latestStep = r
			if r.CompactionReason == "overflow" {
				op.OverflowRecoveryUsed = true
			}
		case session.RecordWriteDeferred:
			if r.Target != nil {
				if _, ok := entries[r.Target.ID]; !ok {
					op.PendingWrites = append(op.PendingWrites, *r.Target)
				}
			}
		}
	}
	if latestStep != nil {
		if _, ok := entries[latestStep.ResultEntryID]; !ok {
			op.Step = &StepState{
				Step: latestStep.Step, Attempt: latestStep.Attempt,
				ResultEntryID:    latestStep.ResultEntryID,
				CompactionReason: latestStep.CompactionReason, Record: *latestStep,
			}
		}
	}
	op.PendingSteer = reduceQueue("steer", op.ID, records, entries)
	op.PendingFollowUp = reduceQueue("followUp", op.ID, records, entries)
	op.ToolBatch, op.NewestOwn = deriveToolBatch(op.ID, records, entryList, entries)
	if op.NewestOwn != nil && op.NewestOwn.Entry.Meta != nil {
		if stop, _ := op.NewestOwn.Entry.Meta["stop_reason"].(string); stop == "error" {
			message, _ := op.NewestOwn.Entry.Meta["error"].(string)
			op.TerminalFailure = &session.OpError{Code: "model_error", Message: message}
		}
	}
	if op.Intent.ResultEntryID != "" {
		_, op.Targets.Result = entries[op.Intent.ResultEntryID]
	}
	if op.Intent.SummaryEntryID != "" {
		_, op.Targets.Summary = entries[op.Intent.SummaryEntryID]
	}
}

func reduceQueue(queue, runID string, records []session.Record, entries map[string]session.Entry) []session.ProvisionedEntry {
	cancelled := map[string]bool{}
	var out []session.ProvisionedEntry
	for _, r := range records {
		if r.Queue != queue || runID != "" && r.RunID != runID {
			continue
		}
		if r.Type == session.RecordQueueCancelled {
			cancelled[r.EntryID] = true
			continue
		}
		if r.Type == session.RecordQueueEnqueued && r.Target != nil {
			if !cancelled[r.Target.ID] {
				if _, materialized := entries[r.Target.ID]; !materialized {
					out = append(out, *r.Target)
				}
			}
		}
	}
	filtered := out[:0]
	for _, p := range out {
		if !cancelled[p.ID] {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

func deriveToolBatch(runID string, records []session.Record, entryList []session.Entry, entries map[string]session.Entry) (*ToolBatchState, *NewestOwn) {
	attempted := map[string]bool{}
	for _, r := range records {
		if r.RunID == runID && r.Type == session.RecordStepAttempt {
			attempted[r.ResultEntryID] = true
		}
	}
	var assistant *session.Entry
	for i := len(entryList) - 1; i >= 0; i-- {
		e := &entryList[i]
		if e.Kind == session.EntryAssistantMessage && attempted[e.ID] {
			assistant = e
			break
		}
	}
	if assistant == nil {
		return nil, nil
	}
	newest := &NewestOwn{Entry: *assistant, Seq: int64(assistant.Seq)}
	if len(toolCalls(assistant)) == 0 {
		return nil, newest
	}
	batch := &ToolBatchState{AssistantEntryID: assistant.ID}
	if assistant.Meta != nil {
		stop, _ := assistant.Meta["stop_reason"].(string)
		batch.Truncated = stop == "length"
	}
	for i, tc := range toolCalls(assistant) {
		item := ToolCallState{Index: i, Call: openAIToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments}}
		for j := range records {
			r := &records[j]
			if r.RunID == runID && r.Type == session.RecordToolStarted &&
				r.AssistantEntryID == assistant.ID && r.ToolIndex == i {
				item.Started = r
				if e, ok := entries[r.ResultEntryID]; ok {
					copy := e
					item.Result = &copy
				}
			}
		}
		if item.Result == nil {
			for j := range entryList {
				e := &entryList[j]
				if int64(e.Seq) > int64(assistant.Seq) && e.Kind == session.EntryToolResult && e.ToolCallID() == tc.ID {
					copy := *e
					item.Result = &copy
				}
			}
		}
		if item.Result == nil {
			batch.Unresolved = true
		}
		batch.Calls = append(batch.Calls, item)
	}
	return batch, newest
}

func toolCalls(e *session.Entry) []openai.ToolCall {
	if e.Meta == nil {
		return nil
	}
	raw := e.Meta["tool_calls"]
	if calls, ok := raw.([]openai.ToolCall); ok {
		return calls
	}
	return nil
}
