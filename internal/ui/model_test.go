package ui

import (
	"testing"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/Vignesh-Rajarajan/golum/internal/ui/styles"
	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
)

// newTestModel builds a minimal Model for exercising Update() without a real
// LLM client or terminal. Keeping width at 0 makes syncViewportContent and
// syncSelectableBuffer no-ops (they short-circuit on width<=0), so the
// rendering pipeline (styles, glamour, viewport sizing) is never touched.
// input/spinner/viewport still need real bubbles constructors (not zero
// values) since Update() unconditionally drives them (e.g. input.Focus()).
func newTestModel(messages []Message, streaming bool) Model {
	return Model{
		styles:    styles.DefaultStyles(),
		ctxMgr:    contextmgr.NewContextManager(nil, prompt.PromptConfig{}, nil, nil),
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

func TestUpdate_StreamMsgContentDone_ResetsStreamingImmediately(t *testing.T) {
	// Regression: streaming must flip false as soon as ContentDone arrives,
	// not only later when the channel closes and StreamDoneMsg fires -
	// otherwise the UI shows "Generating response..." for a beat after the
	// answer is already complete.
	m := newTestModel([]Message{{Role: RoleAssistant, Content: "hello"}}, true)

	updated, _ := m.Update(StreamMsg{Event: llm.StreamEvent{Type: llm.EventTypeContentDone}})
	got := asModel(t, updated)

	if got.streaming {
		t.Fatal("expected streaming=false immediately after ContentDone, got true")
	}
}

func TestUpdate_StreamMsgContentDelta_AppendsToLastAssistantMessage(t *testing.T) {
	m := newTestModel([]Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "Hel"},
	}, true)

	updated, _ := m.Update(StreamMsg{Event: llm.StreamEvent{Type: llm.EventTypeContentDelta, Content: "lo"}})
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

func TestUpdate_StreamMsgContentDelta_StartsNewAssistantMessageWhenNoneOpen(t *testing.T) {
	m := newTestModel([]Message{{Role: RoleUser, Content: "hi"}}, true)

	updated, _ := m.Update(StreamMsg{Event: llm.StreamEvent{Type: llm.EventTypeContentDelta, Content: "Hi there"}})
	got := asModel(t, updated)

	if len(got.messages) != 2 {
		t.Fatalf("expected a new assistant message appended, got %d messages", len(got.messages))
	}
	if got.messages[1].Role != RoleAssistant || got.messages[1].Content != "Hi there" {
		t.Fatalf("expected new assistant message with delta content, got %+v", got.messages[1])
	}
}

func TestUpdate_StreamMsgContentDone_SanitizesAndRecordsAssistantMessage(t *testing.T) {
	m := newTestModel([]Message{
		{Role: RoleAssistant, Content: "answer<tool_call><function=todos></function></tool_call>"},
	}, true)

	updated, _ := m.Update(StreamMsg{Event: llm.StreamEvent{Type: llm.EventTypeContentDone}})
	got := asModel(t, updated)

	if got.messages[0].Content != "answer" {
		t.Fatalf("expected pseudo tool markup stripped, got %q", got.messages[0].Content)
	}
	if got.ctxMgr.MessageCount() != 1 {
		t.Fatalf("expected sanitized content recorded in context manager, got %d messages", got.ctxMgr.MessageCount())
	}
}

func TestUpdate_StreamMsgContentDone_Cancelled_DropsPartialAssistantMessage(t *testing.T) {
	m := newTestModel([]Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "partial..."},
	}, true)

	updated, _ := m.Update(StreamMsg{Event: llm.StreamEvent{Type: llm.EventTypeContentDone, Cancelled: true}})
	got := asModel(t, updated)

	if got.streaming {
		t.Fatal("expected streaming=false after a cancelled stream")
	}
	if len(got.messages) != 1 {
		t.Fatalf("expected partial assistant message dropped, got %d messages", len(got.messages))
	}
	if got.messages[0].Role != RoleUser {
		t.Fatalf("expected only the user message to remain, got role %v", got.messages[0].Role)
	}
}

func TestUpdate_StreamMsgError_SetsStreamingFalseAndAppendsErrorMessage(t *testing.T) {
	m := newTestModel([]Message{{Role: RoleUser, Content: "hi"}}, true)

	updated, _ := m.Update(StreamMsg{Event: llm.StreamEvent{Type: llm.EventTypeError, Error: errBoom}})
	got := asModel(t, updated)

	if got.streaming {
		t.Fatal("expected streaming=false after an error event")
	}
	if len(got.messages) != 2 || got.messages[1].Role != RoleError {
		t.Fatalf("expected an error message appended, got %+v", got.messages)
	}
}

func TestUpdate_StreamDoneMsg_ClearsStreamReaderAndCancel(t *testing.T) {
	m := newTestModel(nil, true)
	m.streamReader = &streamReader{events: make(chan llm.StreamEvent)}
	cancelled := false
	m.streamCancel = func() { cancelled = true }

	updated, _ := m.Update(StreamDoneMsg{})
	got := asModel(t, updated)

	if got.streaming {
		t.Fatal("expected streaming=false after StreamDoneMsg")
	}
	if got.streamReader != nil {
		t.Fatal("expected streamReader cleared after StreamDoneMsg")
	}
	if !cancelled {
		t.Fatal("expected releaseStreamCancel to invoke the cancel func")
	}
}

var errBoom = &testError{"boom"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
