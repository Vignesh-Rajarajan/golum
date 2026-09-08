package evals

import (
	"testing"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/sashabaranov/go-openai"
)

// synthetic builds a Result whose entries and records look like a real run:
// user prompt, assistant message requesting two tools, both tool results, then
// a final assistant message.
func synthetic() *Result {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := []openai.ToolCall{
		{ID: "call_a", Function: openai.FunctionCall{Name: "write_file", Arguments: `{"path":"a.txt"}`}},
		{ID: "call_b", Function: openai.FunctionCall{Name: "read_file", Arguments: `{"path":"a.txt"}`}},
	}
	entries := []session.Entry{
		{ID: "e_user", Kind: session.EntryUserMessage, Content: "do it", Time: base},
		{ID: "e_asst1", Kind: session.EntryAssistantMessage, Content: "working",
			Meta: map[string]any{"tool_calls": calls, "stop_reason": "tool_calls"},
			Time: base.Add(2 * time.Second)},
		{ID: "e_res_a", Kind: session.EntryToolResult, Content: "ok",
			Meta: map[string]any{"tool_call_id": "call_a", "is_error": false},
			Time: base.Add(3 * time.Second)},
		{ID: "e_res_b", Kind: session.EntryToolResult, Content: "boom",
			Meta: map[string]any{"tool_call_id": "call_b", "is_error": true},
			Time: base.Add(4 * time.Second)},
		{ID: "e_asst2", Kind: session.EntryAssistantMessage, Content: "done",
			Meta: map[string]any{"stop_reason": "stop"}, Time: base.Add(5 * time.Second)},
	}
	records := []session.Record{
		{Type: session.RecordStepAttempt, Step: "assistant", Attempt: 1,
			ResultEntryID: "e_asst1", Time: base.Add(1 * time.Second)},
		{Type: session.RecordToolStarted, ToolName: "write_file", ToolCallID: "call_a",
			ResultEntryID: "e_res_a", EffectiveArgs: map[string]any{"path": "a.txt"},
			Time: base.Add(2 * time.Second)},
		{Type: session.RecordToolStarted, ToolName: "read_file", ToolCallID: "call_b",
			ResultEntryID: "e_res_b", EffectiveArgs: map[string]any{"path": "a.txt"},
			Time: base.Add(3 * time.Second)},
		{Type: session.RecordStepAttempt, Step: "assistant", Attempt: 2,
			ResultEntryID: "e_asst2", Time: base.Add(4 * time.Second)},
		{Type: session.RecordOperationFinished, RunID: "run", Outcome: "completed"},
	}
	return &Result{Entries: entries, Records: records}
}

func TestTrajectoryShape(t *testing.T) {
	steps := synthetic().Trajectory()

	wantKinds := []string{
		StepUser, StepAssistant, StepToolCall, StepToolCall,
		StepToolResult, StepToolResult, StepAssistant,
	}
	if len(steps) != len(wantKinds) {
		t.Fatalf("got %d steps, want %d: %+v", len(steps), len(wantKinds), steps)
	}
	for i, want := range wantKinds {
		if steps[i].Kind != want {
			t.Fatalf("step %d kind=%q want %q", i, steps[i].Kind, want)
		}
		if steps[i].Index != i {
			t.Fatalf("step %d reports index %d", i, steps[i].Index)
		}
	}
	if steps[2].ToolName != "write_file" {
		t.Fatalf("tool call name=%q want write_file", steps[2].ToolName)
	}
	if got := steps[2].Arguments["path"]; got != "a.txt" {
		t.Fatalf("tool call args path=%v want a.txt", got)
	}
	if !steps[5].IsError {
		t.Fatal("the failing tool result should be marked as an error")
	}
	if steps[5].ToolName != "read_file" {
		t.Fatalf("tool result name=%q want read_file (joined from the record)", steps[5].ToolName)
	}
	if steps[6].StopReason != "stop" {
		t.Fatalf("final stop reason=%q want stop", steps[6].StopReason)
	}
	// Durations come from joining the step-attempt record to its result entry.
	if steps[1].DurationMs != 1000 {
		t.Fatalf("assistant duration=%dms want 1000", steps[1].DurationMs)
	}
	if steps[4].DurationMs != 1000 {
		t.Fatalf("tool duration=%dms want 1000", steps[4].DurationMs)
	}
}

func TestSafeCutIndexKeepsToolBatchesIntact(t *testing.T) {
	steps := synthetic().Trajectory()

	// Indices 1..5 are the assistant message and its batch. Cutting anywhere
	// inside must fall back to 1, just before the assistant message.
	for keep := 2; keep <= 5; keep++ {
		if got := SafeCutIndex(steps, keep); got != 1 {
			t.Fatalf("SafeCutIndex(%d)=%d want 1 (batch starts at 1)", keep, got)
		}
	}
	// Boundaries outside a batch are already safe.
	for _, keep := range []int{0, 1, 6, 7} {
		if got := SafeCutIndex(steps, keep); got != keep {
			t.Fatalf("SafeCutIndex(%d)=%d want %d", keep, got, keep)
		}
	}
	if got := SafeCutIndex(steps, 99); got != len(steps) {
		t.Fatalf("SafeCutIndex past the end=%d want %d", got, len(steps))
	}
	if got := SafeCutIndex(steps, -3); got != 0 {
		t.Fatalf("SafeCutIndex(-3)=%d want 0", got)
	}
}

// A cut is only useful if every kept tool call still has its result, which is
// what the provider requires of the rebuilt context.
func TestSafeCutIndexLeavesNoDanglingToolCalls(t *testing.T) {
	r := synthetic()
	steps := r.Trajectory()
	for keep := 0; keep <= len(steps); keep++ {
		cut := SafeCutIndex(steps, keep)
		called := map[string]bool{}
		resolved := map[string]bool{}
		for _, s := range steps[:cut] {
			switch s.Kind {
			case StepToolCall:
				called[s.ToolCallID] = true
			case StepToolResult:
				resolved[s.ToolCallID] = true
			}
		}
		for id := range called {
			if !resolved[id] {
				t.Fatalf("keep=%d cut=%d left tool call %q without a result", keep, cut, id)
			}
		}
	}
}

func TestEntriesForPrefixDeduplicatesAssistantEntries(t *testing.T) {
	r := synthetic()
	steps := r.Trajectory()

	// Keeping through the whole batch spans one assistant entry and two
	// results, but only three entries total.
	got := EntriesForPrefix(r, SafeCutIndex(steps, 6))
	want := []string{"e_user", "e_asst1", "e_res_a", "e_res_b"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("entry %d = %q want %q", i, got[i].ID, id)
		}
	}
}

func TestLastToolCallID(t *testing.T) {
	steps := synthetic().Trajectory()
	if got := LastToolCallID(steps, 1); got != "" {
		t.Fatalf("before any tool ran, got %q want empty", got)
	}
	if got := LastToolCallID(steps, 5); got != "call_a" {
		t.Fatalf("got %q want call_a", got)
	}
	if got := LastToolCallID(steps, len(steps)); got != "call_b" {
		t.Fatalf("got %q want call_b", got)
	}
}

// Entries that came back through JSON hold tool calls as generic maps rather
// than typed values; dropping them would silently produce a trajectory with no
// tool steps at all.
func TestTrajectoryReadsToolCallsAfterJSONRoundTrip(t *testing.T) {
	r := &Result{Entries: []session.Entry{{
		ID: "e1", Kind: session.EntryAssistantMessage,
		Meta: map[string]any{"tool_calls": []any{map[string]any{
			"id":       "call_x",
			"function": map[string]any{"name": "glob", "arguments": `{"pattern":"*.go"}`},
		}}},
	}}}
	steps := r.Trajectory()
	if len(steps) != 2 || steps[1].Kind != StepToolCall {
		t.Fatalf("expected an assistant step plus a tool call, got %+v", steps)
	}
	if steps[1].ToolName != "glob" || steps[1].ToolCallID != "call_x" {
		t.Fatalf("tool call = %+v", steps[1])
	}
}
