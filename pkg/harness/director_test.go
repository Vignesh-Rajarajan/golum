package harness

import (
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm/capability"
)

func TestForceToolDirectorRetriesThenFails(t *testing.T) {
	dir := ForceToolDirector{}
	dc := DirectorContext{
		RunID:    "run",
		Intent:   session.OperationIntent{ForceTool: "write_file", ForceToolAttempts: 2},
		Attempts: 1,
		Caps:     capability.Caps{ForcedToolChoice: capability.Supported},
	}
	if got, _ := dir.AfterAssistant(dc); got != Continue {
		t.Fatalf("first miss should continue, got %v", got)
	}
	hint := dir.BeforeInference(dc)
	if hint.System == "" || hint.ToolChoice.Name != "" {
		t.Fatalf("first attempt should be soft-only, got %+v", hint)
	}
	dc.Attempts = 2
	hint = dir.BeforeInference(dc)
	if hint.ToolChoice.Name != "write_file" {
		t.Fatalf("second attempt should force the tool, got %+v", hint)
	}
	if got, msg := dir.AfterAssistant(dc); got != Fail || msg == "" {
		t.Fatalf("second miss should fail, got %v %q", got, msg)
	}
}

func TestForceToolDirectorPassesOnSuccessfulResult(t *testing.T) {
	dir := ForceToolDirector{}
	dc := DirectorContext{
		RunID:  "run",
		Intent: session.OperationIntent{ForceTool: "write_file"},
		Records: []session.Record{{
			Type: session.RecordToolStarted, RunID: "run", ToolName: "write_file", ResultEntryID: "r1",
		}},
		Entries: []session.Entry{{
			ID: "r1", Kind: session.EntryToolResult, Meta: map[string]any{"is_error": false},
		}},
		Attempts: 1,
	}
	if got, _ := dir.AfterAssistant(dc); got != Pass {
		t.Fatalf("got %v want Pass", got)
	}
}

func TestForceToolDirectorDoesNotNativeForceWhenUnsupported(t *testing.T) {
	dir := ForceToolDirector{}
	hint := dir.BeforeInference(DirectorContext{
		Intent:   session.OperationIntent{ForceTool: "write_file"},
		Attempts: 2,
		Caps:     capability.Caps{ForcedToolChoice: capability.Unknown},
	})
	if hint.ToolChoice.Name != "" {
		t.Fatalf("unknown capability must not set tool_choice, got %+v", hint)
	}
}
