package harness

import (
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
)

func TestStatusIsEphemeralAndEscapesTodos(t *testing.T) {
	s := formatAgentStatus(agentStatus{
		Workspace:     "/tmp/ws",
		Model:         "gpt-4o",
		ModelCalls:    2,
		MaxModelCalls: 10,
		ToolCalls:     1,
		MaxToolCalls:  20,
		Todos:         []tool.TodoItem{{ID: "1", Content: "<script>alert(1)</script>"}},
	})
	if !strings.Contains(s, "<agent_status>") {
		t.Fatal(s)
	}
	if !strings.Contains(s, "model_calls: 2/10") {
		t.Fatal(s)
	}
	if strings.Contains(s, "<script>") {
		t.Fatal("todo content was not escaped")
	}
	if !strings.Contains(s, "&lt;script&gt;") {
		t.Fatal("expected escaped todo content")
	}
}
