package harness

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
)

const (
	CorruptionMultipleOpenOperations   = "multiple_open_operations"
	CorruptionUnknownOperation         = "unknown_operation"
	CorruptionRecordAfterFinish        = "record_after_finish"
	CorruptionNonConsecutiveAttempt    = "non_consecutive_attempt"
	CorruptionInvalidCompactionReason  = "invalid_compaction_reason"
	CorruptionQueueAfterAbort          = "queue_after_abort"
	CorruptionInvalidQueueCancellation = "invalid_queue_cancellation"
	CorruptionInconsistentStep         = "inconsistent_step"
	CorruptionToolCallMismatch         = "tool_call_mismatch"
	CorruptionDuplicateToolInvocation  = "duplicate_tool_invocation"
	CorruptionProvisionedEntryMismatch = "provisioned_entry_mismatch"
	CorruptionInvalidDeferredHandle    = "invalid_deferred_handle"
)

type CorruptionError struct {
	Reason   string
	RecordID string
	Detail   string
}

func (e *CorruptionError) Error() string {
	if e.Detail == "" {
		return "corrupt record log: " + e.Reason
	}
	return fmt.Sprintf("corrupt record log: %s: %s", e.Reason, e.Detail)
}

func corrupt(reason string, r session.Record, detail string) error {
	return &CorruptionError{Reason: reason, RecordID: r.ID, Detail: detail}
}

func ValidateRecordLog(in RecordLogSlice) error {
	records := append([]session.Record(nil), in.Records...)
	sort.SliceStable(records, func(i, j int) bool { return records[i].Seq < records[j].Seq })
	entries := map[string]session.Entry{}
	for _, e := range in.Entries {
		entries[e.ID] = e
	}
	started, finished, aborted := map[string]session.Record{}, map[string]session.Record{}, map[string]bool{}
	attempts := map[string]int{}
	attemptStep := map[string]string{}
	enqueued := map[string]session.Record{}
	cancelled := map[string]bool{}
	invocations := map[string]session.Record{}
	targets := map[string]session.ProvisionedEntry{}

	for _, r := range records {
		if r.Type == session.RecordOperationStarted {
			started[r.RunID] = r
			continue
		}
		if r.RunID != "" {
			if _, ok := started[r.RunID]; !ok {
				return corrupt(CorruptionUnknownOperation, r, r.RunID)
			}
			if f, ok := finished[r.RunID]; ok && r.Seq > f.Seq {
				return corrupt(CorruptionRecordAfterFinish, r, r.RunID)
			}
		}
		switch r.Type {
		case session.RecordOperationFinished:
			finished[r.RunID] = r
		case session.RecordAbortRequested:
			aborted[r.RunID] = true
		case session.RecordStepAttempt:
			key := r.RunID + "\x00" + r.Step
			if want := attempts[key] + 1; r.Attempt != want {
				return corrupt(CorruptionNonConsecutiveAttempt, r, fmt.Sprintf("got %d want %d", r.Attempt, want))
			}
			attempts[key] = r.Attempt
			if r.Step == "compaction" && r.CompactionReason != "manual" &&
				r.CompactionReason != "threshold" && r.CompactionReason != "overflow" {
				return corrupt(CorruptionInvalidCompactionReason, r, r.CompactionReason)
			}
			if prior, ok := attemptStep[r.ResultEntryID]; ok && prior != r.Step {
				return corrupt(CorruptionInconsistentStep, r, r.ResultEntryID)
			}
			attemptStep[r.ResultEntryID] = r.Step
		case session.RecordQueueEnqueued:
			if (r.Queue == "steer" || r.Queue == "followUp") && aborted[r.RunID] {
				return corrupt(CorruptionQueueAfterAbort, r, r.Queue)
			}
			if r.Target != nil {
				enqueued[r.Queue+"\x00"+r.Target.ID] = r
				if prior, ok := targets[r.Target.ID]; ok && !reflect.DeepEqual(prior, *r.Target) {
					return corrupt(CorruptionProvisionedEntryMismatch, r, r.Target.ID)
				}
				targets[r.Target.ID] = *r.Target
			}
		case session.RecordQueueCancelled:
			key := r.Queue + "\x00" + r.EntryID
			enq, ok := enqueued[key]
			if !ok || cancelled[key] || enq.Target == nil {
				return corrupt(CorruptionInvalidQueueCancellation, r, r.EntryID)
			}
			if _, materialized := entries[r.EntryID]; materialized {
				return corrupt(CorruptionInvalidQueueCancellation, r, r.EntryID)
			}
			cancelled[key] = true
		case session.RecordToolStarted:
			key := fmt.Sprintf("%s\x00%s\x00%d", r.RunID, r.AssistantEntryID, r.ToolIndex)
			if _, exists := invocations[key]; exists {
				return corrupt(CorruptionDuplicateToolInvocation, r, key)
			}
			invocations[key] = r
			e, ok := entries[r.AssistantEntryID]
			calls := toolCalls(&e)
			if !ok || r.ToolIndex < 0 || r.ToolIndex >= len(calls) ||
				calls[r.ToolIndex].ID != r.ToolCallID ||
				calls[r.ToolIndex].Function.Name != r.ToolName {
				return corrupt(CorruptionToolCallMismatch, r, r.ToolCallID)
			}
		case session.RecordWriteDeferred:
			if r.Target == nil || r.Target.ID == "" {
				return corrupt(CorruptionInvalidDeferredHandle, r, "missing target")
			}
		}
	}

	openByLane := map[string]int{}
	for runID, start := range started {
		if _, ok := finished[runID]; !ok {
			openByLane[start.Lane]++
			if openByLane[start.Lane] > 1 {
				return corrupt(CorruptionMultipleOpenOperations, start, start.Lane)
			}
		}
	}
	for id, p := range targets {
		if e, ok := entries[id]; ok && !p.Matches(e) {
			return corrupt(CorruptionProvisionedEntryMismatch, session.Record{}, id)
		}
	}
	return nil
}
