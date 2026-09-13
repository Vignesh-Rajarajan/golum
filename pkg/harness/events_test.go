package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/harnesstest"
)

func TestEventOrderingInvariants(t *testing.T) {
	r := NewTestRig(t)
	r.Model.Script(
		harnesstest.Turn{ToolCalls: []harnesstest.ToolCall{{
			ID: "c1", Name: "list_dir", Args: `{"path":"."}`,
		}}},
		harnesstest.Turn{Content: "done"},
	)
	r.Prompt("list")
	if err := r.RunToCompletion(context.Background()); err != nil {
		t.Fatal(err)
	}
	var starts, results, dones int
	sawStartBeforeResult := false
	started := false
	for _, ev := range r.Events {
		switch ev.Type {
		case EventToolCallStart:
			starts++
			started = true
		case EventToolCallResult:
			results++
			if started {
				sawStartBeforeResult = true
			}
		case EventTurnDone:
			dones++
		case EventError:
			if ev.Err == nil {
				t.Fatal("error event missing err")
			}
		}
		if ev.ToolResult != nil && strings.Contains(ev.ToolResult.Content, "SECRET=") {
			t.Fatal("secret in event metadata")
		}
	}
	if !sawStartBeforeResult {
		t.Fatal("tool-start must precede tool-result")
	}
	if dones != 1 {
		t.Fatalf("turn done emitted %d times", dones)
	}
}
