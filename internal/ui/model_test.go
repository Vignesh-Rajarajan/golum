package ui

import (
	"fmt"
	"testing"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/Vignesh-Rajarajan/golum/internal/ui/styles"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
)

// newTestModel builds a minimal Model for exercising Update() without a real
// LLM client or terminal. Keeping width at 0 makes syncViewportContent and
// syncSelectableBuffer no-ops (they short-circuit on width<=0).
func newTestModel(messages []Message, streaming bool) Model {
	return Model{
		styles:    styles.DefaultStyles(),
		approvals: newApprovalBroker(),
		messages:  messages,
		streaming: streaming,
		input:     textinput.New(),
		spinner:   spinner.New(),
		viewport:  viewport.New(),
	}
}

func asModel(t *testing.T, tm tea.Model) Model {
	t.Helper()
	m, ok := tm.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", tm)
	}
	return m
}

func TestUpdate_AgentEventTurnDone_ResetsStreamingImmediately(t *testing.T) {
	m := newTestModel([]Message{{Role: RoleAssistant, Content: "hello"}}, true)

	updated, _ := m.Update(AgentEventMsg{Event: harness.AgentEvent{Type: harness.EventTurnDone}})
	got := asModel(t, updated)

	if got.streaming {
		t.Fatal("expected streaming=false immediately after TurnDone, got true")
	}
}

func TestUpdate_AgentEventContentDelta_AppendsToLastAssistantMessage(t *testing.T) {
	m := newTestModel([]Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "Hel"},
	}, true)

	updated, _ := m.Update(AgentEventMsg{Event: harness.AgentEvent{Type: harness.EventContentDelta, Content: "lo"}})
	got := asModel(t, updated)

	if len(got.messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got.messages))
	}
	if got.messages[1].Content != "Hello" {
		t.Fatalf("expected delta appended to last assistant message, got %q", got.messages[1].Content)
	}
	if !got.streaming {
		t.Fatal("expected streaming to remain true mid-delta")
	}
}

func TestUpdate_AgentEventContentDelta_StartsNewAssistantMessageWhenNoneOpen(t *testing.T) {
	m := newTestModel([]Message{{Role: RoleUser, Content: "hi"}}, true)

	updated, _ := m.Update(AgentEventMsg{Event: harness.AgentEvent{Type: harness.EventContentDelta, Content: "Hi there"}})
	got := asModel(t, updated)

	if len(got.messages) != 2 {
		t.Fatalf("expected a new assistant message appended, got %d messages", len(got.messages))
	}
	if got.messages[1].Role != RoleAssistant || got.messages[1].Content != "Hi there" {
		t.Fatalf("expected new assistant message with delta content, got %+v", got.messages[1])
	}
}

func TestUpdate_AgentEventError_SetsStreamingFalseAndAppendsErrorMessage(t *testing.T) {
	m := newTestModel([]Message{{Role: RoleUser, Content: "hi"}}, true)

	updated, _ := m.Update(AgentEventMsg{Event: harness.AgentEvent{Type: harness.EventError, Err: fmt.Errorf("boom")}})
	got := asModel(t, updated)

	if got.streaming {
		t.Fatal("expected streaming=false after an error event")
	}
	if len(got.messages) != 2 || got.messages[1].Role != RoleError {
		t.Fatalf("expected an error message appended, got %+v", got.messages)
	}
}

func TestUpdate_AgentDoneMsg_ClearsEventReader(t *testing.T) {
	m := newTestModel(nil, true)
	ch := make(chan harness.AgentEvent)
	m.eventReader = &agentEventReader{events: ch}

	updated, _ := m.Update(AgentDoneMsg{})
	got := asModel(t, updated)

	if got.streaming {
		t.Fatal("expected streaming=false after AgentDoneMsg")
	}
	if got.eventReader != nil {
		t.Fatal("expected eventReader cleared after AgentDoneMsg")
	}
}

func TestUpdate_ToolCallResult_Renders(t *testing.T) {
	m := newTestModel(nil, true)
	updated, _ := m.Update(AgentEventMsg{Event: harness.AgentEvent{
		Type:       harness.EventToolCallResult,
		ToolResult: &tool.Result{Content: "ok", Display: "read 3 lines", IsError: false},
	}})
	got := asModel(t, updated)
	if len(got.messages) != 1 || got.messages[0].Role != RoleToolResult {
		t.Fatalf("expected tool result message, got %+v", got.messages)
	}
}

func TestApprovalBroker_DecideWithoutPending(t *testing.T) {
	b := newApprovalBroker()
	b.Decide(true) // must not panic
	if b.Waiting() {
		t.Fatal("expected not waiting")
	}
}
