package evals

import (
	"encoding/json"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/sashabaranov/go-openai"
)

// Trajectory step kinds.
const (
	StepUser         = "user"
	StepAssistant    = "assistant"
	StepToolCall     = "tool_call"
	StepToolResult   = "tool_result"
	StepSystemNotice = "system_notice"
)

// TrajectoryStep is one position in the run, flattened so a verifier or an
// attribution rule can talk about "the step where it went wrong" by index.
type TrajectoryStep struct {
	Index      int            `json:"index"`
	Kind       string         `json:"kind"`
	EntryID    string         `json:"entry_id,omitempty"`
	ToolName   string         `json:"tool_name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Arguments  map[string]any `json:"arguments,omitempty"`
	Content    string         `json:"content,omitempty"`
	IsError    bool           `json:"is_error,omitempty"`
	StopReason string         `json:"stop_reason,omitempty"`
	DurationMs int64          `json:"duration_ms,omitempty"`
	// batchStart is the index of the assistant step that owns this one, used
	// to keep a tool batch intact when cutting a prefix.
	batchStart int
}

// Trajectory flattens the run into ordered steps, joining entries with the
// orchestration records that carry durations and stop reasons. The result is
// memoized: verifiers call it repeatedly.
func (r *Result) Trajectory() []TrajectoryStep {
	if r == nil {
		return nil
	}
	r.trajOnce.Do(func() { r.traj = buildTrajectory(r.Entries, r.Records) })
	return r.traj
}

func buildTrajectory(entries []session.Entry, records []session.Record) []TrajectoryStep {
	startedByResult := map[string]session.Record{}
	attemptByResult := map[string]session.Record{}
	for _, rec := range records {
		switch rec.Type {
		case session.RecordToolStarted:
			startedByResult[rec.ResultEntryID] = rec
		case session.RecordStepAttempt:
			attemptByResult[rec.ResultEntryID] = rec
		}
	}

	var steps []TrajectoryStep
	// owner maps a tool call id to the index of the assistant step that
	// issued it, so its result can be tied back to the same batch.
	owner := map[string]int{}
	// add appends s at the next index. A batchStart of -1 means "own index";
	// it cannot default to zero, since zero is a valid owning index.
	add := func(s TrajectoryStep, batchStart int) int {
		s.Index = len(steps)
		s.batchStart = batchStart
		if batchStart < 0 {
			s.batchStart = s.Index
		}
		steps = append(steps, s)
		return s.Index
	}

	for _, e := range entries {
		switch e.Kind {
		case session.EntryUserMessage:
			add(TrajectoryStep{Kind: StepUser, EntryID: e.ID, Content: e.Content}, -1)
		case session.EntrySystemNotice:
			add(TrajectoryStep{Kind: StepSystemNotice, EntryID: e.ID, Content: e.Content}, -1)
		case session.EntryAssistantMessage:
			stop, _ := e.Meta["stop_reason"].(string)
			step := TrajectoryStep{
				Kind: StepAssistant, EntryID: e.ID, Content: e.Content, StopReason: stop,
			}
			if attempt, ok := attemptByResult[e.ID]; ok {
				step.DurationMs = elapsedMs(attempt.Time, e.Time)
			}
			at := add(step, -1)
			for _, tc := range assistantToolCalls(e.Meta) {
				owner[tc.ID] = at
				add(TrajectoryStep{
					Kind: StepToolCall, EntryID: e.ID, ToolName: tc.Function.Name,
					ToolCallID: tc.ID, Arguments: decodeArgs(tc.Function.Arguments),
				}, at)
			}
		case session.EntryToolResult:
			isErr, _ := e.Meta["is_error"].(bool)
			step := TrajectoryStep{
				Kind: StepToolResult, EntryID: e.ID, Content: e.Content,
				ToolCallID: e.ToolCallID(), IsError: isErr,
			}
			if started, ok := startedByResult[e.ID]; ok {
				step.ToolName = started.ToolName
				step.Arguments = started.EffectiveArgs
				step.DurationMs = elapsedMs(started.Time, e.Time)
			}
			batch := -1
			if at, ok := owner[step.ToolCallID]; ok {
				batch = at
			}
			add(step, batch)
		}
	}
	return steps
}

func elapsedMs(start, end time.Time) int64 {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}

// assistantToolCalls reads tool calls off an assistant entry, tolerating both
// the live in-memory shape and the generic JSON shape entries take after a
// storage round trip.
func assistantToolCalls(meta map[string]any) []openai.ToolCall {
	if meta == nil {
		return nil
	}
	raw, ok := meta["tool_calls"]
	if !ok {
		return nil
	}
	if tcs, ok := raw.([]openai.ToolCall); ok {
		return tcs
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var tcs []openai.ToolCall
	if json.Unmarshal(b, &tcs) != nil {
		return nil
	}
	return tcs
}

func decodeArgs(raw string) map[string]any {
	if raw == "" {
		return nil
	}
	var out map[string]any
	if json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}

// SafeCutIndex normalizes a prefix length to a coherent boundary. An assistant
// message that requested tools cannot be separated from the results of those
// tools: the provider rejects a context where a tool call has no matching
// result. A cut landing inside a batch therefore moves back to just before the
// assistant message that opened it.
func SafeCutIndex(steps []TrajectoryStep, keep int) int {
	if keep <= 0 {
		return 0
	}
	if keep >= len(steps) {
		return len(steps)
	}
	// steps[keep] is the first dropped step. If it belongs to a batch that
	// started earlier, the whole batch has to go.
	if start := steps[keep].batchStart; start < keep {
		return start
	}
	return keep
}

// EntriesForPrefix returns the session entries backing the first keep steps,
// deduplicated because one assistant entry can span several steps. Callers
// should pass a length already normalized by SafeCutIndex.
func EntriesForPrefix(r *Result, keep int) []session.Entry {
	if r == nil {
		return nil
	}
	steps := r.Trajectory()
	if keep > len(steps) {
		keep = len(steps)
	}
	wanted := make(map[string]bool, keep)
	for _, s := range steps[:keep] {
		if s.EntryID != "" {
			wanted[s.EntryID] = true
		}
	}
	out := make([]session.Entry, 0, len(wanted))
	for _, e := range r.Entries {
		if wanted[e.ID] {
			out = append(out, e)
		}
	}
	return out
}

// LastToolCallID returns the tool call id of the final tool result in the
// first keep steps, which identifies the workspace snapshot matching that
// prefix. Empty means "no tool ran yet", i.e. the initial snapshot.
func LastToolCallID(steps []TrajectoryStep, keep int) string {
	if keep > len(steps) {
		keep = len(steps)
	}
	for i := keep - 1; i >= 0; i-- {
		if steps[i].Kind == StepToolResult && steps[i].ToolCallID != "" {
			return steps[i].ToolCallID
		}
	}
	return ""
}
