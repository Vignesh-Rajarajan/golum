package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/harnesstest"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/sashabaranov/go-openai"
)

func TestCompactionOverflowRetriesOnce(t *testing.T) {
	r := NewTestRig(t)
	r.Deps.Compactor = NewCompactor(r.Model.Client())
	r.rebuildDriver()
	r.Model.Script(
		harnesstest.Turn{Status: 400, ErrorBody: `{"error":{"message":"This model's maximum context length is 8192 tokens","code":"context_length_exceeded"}}`},
		harnesstest.Turn{NonStream: true, Content: "## ORIGINAL GOAL\nDo the thing.\n## COMPLETED ACTIONS\n- step"},
		harnesstest.Turn{Content: "recovered"},
	)
	for i := 0; i < 4; i++ {
		_, _ = r.Session.AppendUserMessage(strings.Repeat("context ", 40))
		_, _ = r.Session.AppendAssistantMessage(strings.Repeat("reply ", 40), nil)
	}
	r.Prompt("continue")
	if err := r.RunToCompletion(context.Background()); err != nil {
		t.Fatal(err)
	}
	overflows := 0
	for _, rec := range r.Records() {
		if rec.Type == session.RecordUsage && rec.Cause == "overflow" {
			overflows++
		}
	}
	if overflows != 1 {
		t.Fatalf("overflow retries=%d want 1", overflows)
	}
	r.AssertInvariants()
}

func TestCompactionDoesNotSplitToolBatch(t *testing.T) {
	call := openai.ToolCall{ID: "c1", Type: openai.ToolTypeFunction,
		Function: openai.FunctionCall{Name: "read_file", Arguments: `{}`}}
	entries := []session.Entry{
		{ID: "e1", Kind: session.EntryUserMessage},
		{ID: "e2", Kind: session.EntryAssistantMessage, Meta: map[string]any{"tool_calls": []openai.ToolCall{call}}},
		{ID: "e3", Kind: session.EntryToolResult, Meta: map[string]any{"tool_call_id": "c1"}},
		{ID: "e4", Kind: session.EntryUserMessage},
	}
	for _, cut := range session.FindValidEntryCutPoints(entries) {
		if cut == 2 {
			t.Fatal("cut split an assistant/tool batch")
		}
	}
}

func TestFailedCompactionLeavesSessionIntact(t *testing.T) {
	r := NewTestRig(t)
	r.Deps.Compactor = NewCompactor(r.Model.Client())
	r.rebuildDriver()
	r.Model.Script(harnesstest.Turn{Status: 500, ErrorBody: `{"error":{"message":"summarizer down"}}`})
	for i := 0; i < 4; i++ {
		_, _ = r.Session.AppendUserMessage("x")
		_, _ = r.Session.AppendAssistantMessage("y", nil)
	}
	before := len(r.Session.Entries())
	_, err := r.Deps.Compactor.Compact(context.Background(), r.Session, nil)
	if err == nil {
		t.Fatal("expected compaction failure")
	}
	if len(r.Session.Entries()) != before {
		t.Fatal("failed compaction mutated the journal")
	}
}
