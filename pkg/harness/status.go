package harness

import (
	"fmt"
	"html"
	"strings"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
	"github.com/sashabaranov/go-openai"
)

type agentStatus struct {
	Workspace       string
	Model           string
	ModelCalls      int
	MaxModelCalls   int
	ToolCalls       int
	MaxToolCalls    int
	ConsecutiveErrs int
	Todos           []tool.TodoItem
}

func agentStatusFor(deps LoopDeps, cfg LoopConfig, records []session.Record, runID string) agentStatus {
	s := agentStatus{Model: deps.Model, MaxModelCalls: cfg.MaxModelInvocations, MaxToolCalls: cfg.MaxToolCallsPerTurn}
	if deps.Env != nil {
		s.Workspace = deps.Env.CWD()
	}
	for _, r := range records {
		if r.RunID != runID {
			continue
		}
		switch r.Type {
		case session.RecordStepAttempt:
			if r.Step == "assistant" {
				s.ModelCalls++
			}
		case session.RecordToolStarted:
			s.ToolCalls++
		}
	}
	if deps.Session != nil {
		s.ConsecutiveErrs = consecutiveToolErrors(records, deps.Session.Entries(), runID)
	}
	if deps.Todos != nil {
		s.Todos = deps.Todos.List()
	}
	return s
}

func openAIStatusMessage(s agentStatus) openai.ChatCompletionMessage {
	return openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: formatAgentStatus(s)}
}

func formatAgentStatus(s agentStatus) string {
	var b strings.Builder
	b.WriteString("<agent_status>\n")
	if s.Workspace != "" {
		fmt.Fprintf(&b, "workspace: %s\n", html.EscapeString(s.Workspace))
	}
	if s.Model != "" {
		fmt.Fprintf(&b, "model: %s\n", html.EscapeString(s.Model))
	}
	fmt.Fprintf(&b, "model_calls: %d/%d\ntool_calls: %d/%d\nconsecutive_tool_errors: %d\n",
		s.ModelCalls, s.MaxModelCalls, s.ToolCalls, s.MaxToolCalls, s.ConsecutiveErrs)
	if todos := tool.FormatTodosForDisplay(s.Todos); todos != "" {
		b.WriteString("todos:\n")
		b.WriteString(html.EscapeString(todos))
		b.WriteByte('\n')
	}
	b.WriteString("</agent_status>")
	return b.String()
}
