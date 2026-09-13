package harness

import (
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
)

func TestFormatAgentStatusIsCompactAndEscaped(t *testing.T) {
	got := formatAgentStatus(agentStatus{
		Workspace:       "/tmp/work",
		Model:           "provider/model",
		ModelCalls:      2,
		MaxModelCalls:   5,
		ToolCalls:       1,
		MaxToolCalls:    3,
		ConsecutiveErrs: 1,
		Todos:           []tool.TodoItem{{Content: "check <output>", Status: "pending"}},
	})
	for _, want := range []string{"model_calls: 2/5", "tool_calls: 1/3", "consecutive_tool_errors: 1", "check &lt;output&gt;"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<output>") {
		t.Fatalf("status leaked unescaped todo content: %s", got)
	}
}

func TestAgentStatusForCountsOnlyThisRun(t *testing.T) {
	sess := driverSession()
	_, err := sess.AppendRecord(session.Record{
		Type: session.RecordStepAttempt, RunID: "other", Step: "assistant",
		Attempt: 1, ResultEntryID: "a0",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = sess.AppendRecord(session.Record{
		Type: session.RecordStepAttempt, RunID: "run", Step: "assistant",
		Attempt: 1, ResultEntryID: "a1",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = sess.AppendRecord(session.Record{
		Type: session.RecordToolStarted, RunID: "run", AssistantEntryID: "a1",
		ToolCallID: "c1", ToolName: "read_file", ResultEntryID: "r1",
	})
	if err != nil {
		t.Fatal(err)
	}
	records, err := sess.FindRecords(session.RecordQuery{Lane: "main"})
	if err != nil {
		t.Fatal(err)
	}
	got := agentStatusFor(LoopDeps{Session: sess, Model: "m"}, LoopConfig{
		MaxModelInvocations: 9, MaxToolCallsPerTurn: 4,
	}, records, "run")
	if got.ModelCalls != 1 || got.ToolCalls != 1 {
		t.Fatalf("got model_calls=%d tool_calls=%d, want 1 and 1", got.ModelCalls, got.ToolCalls)
	}
	if got.Model != "m" || got.MaxModelCalls != 9 || got.MaxToolCalls != 4 {
		t.Fatalf("status = %+v", got)
	}
}
