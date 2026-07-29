package session

import (
	"fmt"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/sashabaranov/go-openai"
)

// EntryKind classifies a session entry.
type EntryKind string

const (
	EntryUserMessage         EntryKind = "user_message"
	EntryAssistantMessage    EntryKind = "assistant_message"
	EntryToolResult          EntryKind = "tool_result"
	EntrySystemNotice        EntryKind = "system_notice"
	EntryThinkingLevelChange EntryKind = "thinking_level_change"
	EntryModelChange         EntryKind = "model_change"
	EntryActiveToolsChange   EntryKind = "active_tools_change"
	EntryCompaction          EntryKind = "compaction"
	EntryLabel               EntryKind = "label"
	EntryCustom              EntryKind = "custom"
	EntryTodos               EntryKind = "todos"
	EntryApprovalAlways      EntryKind = "approval_always"
)

// Entry is one durable session record.
type Entry struct {
	ID       string         `json:"id"`
	ParentID string         `json:"parent_id,omitempty"`
	Kind     EntryKind      `json:"kind"`
	Role     string         `json:"role,omitempty"`
	Content  string         `json:"content,omitempty"`
	ToolCall *llm.ToolCall  `json:"tool_call,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
	Time     time.Time      `json:"time"`
}

// Session is the durability + context surface used by RunAgentLoop.
type Session interface {
	ID() string
	AppendUserMessage(content string) (Entry, error)
	AppendAssistantMessage(content string, toolCalls []openai.ToolCall) (Entry, error)
	AppendToolResult(toolCallID, content string) (Entry, error)
	AppendSystemNotice(content string) (Entry, error)
	AppendCompaction(summary string) error
	AppendTodos(items any) (Entry, error)
	AppendApprovalAlways(toolName string) (Entry, error)
	BuildContext() ([]openai.ChatCompletionMessage, error)
	ContextManager() *contextmgr.ContextManager
	Entries() []Entry
	AlwaysAllowed(toolName string) bool
	SetLabel(label string) error
	Label() string
}

// InMemorySession wraps ContextManager with Entry recording.
type InMemorySession struct {
	id             string
	label          string
	ctxMgr         *contextmgr.ContextManager
	entries        []Entry
	parentID       string
	seq            int
	alwaysAllow    map[string]bool
}

// NewInMemorySession creates a session wrapping ctxMgr.
func NewInMemorySession(id string, ctxMgr *contextmgr.ContextManager) *InMemorySession {
	if id == "" {
		id = fmt.Sprintf("sess_%d", time.Now().UnixNano())
	}
	return &InMemorySession{
		id:          id,
		ctxMgr:      ctxMgr,
		alwaysAllow: make(map[string]bool),
	}
}

func (s *InMemorySession) ID() string { return s.id }

func (s *InMemorySession) ContextManager() *contextmgr.ContextManager { return s.ctxMgr }

func (s *InMemorySession) Entries() []Entry {
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

func (s *InMemorySession) Label() string { return s.label }

func (s *InMemorySession) SetLabel(label string) error {
	s.label = label
	_, err := s.append(EntryLabel, "", label, nil, nil)
	return err
}

func (s *InMemorySession) AlwaysAllowed(toolName string) bool {
	return s.alwaysAllow[toolName]
}

func (s *InMemorySession) nextID() string {
	s.seq++
	return fmt.Sprintf("%s_%d", s.id, s.seq)
}

func (s *InMemorySession) append(kind EntryKind, role, content string, toolCall *llm.ToolCall, meta map[string]any) (Entry, error) {
	e := Entry{
		ID:       s.nextID(),
		ParentID: s.parentID,
		Kind:     kind,
		Role:     role,
		Content:  content,
		ToolCall: toolCall,
		Meta:     meta,
		Time:     time.Now().UTC(),
	}
	s.entries = append(s.entries, e)
	s.parentID = e.ID
	return e, nil
}

func (s *InMemorySession) AppendUserMessage(content string) (Entry, error) {
	s.ctxMgr.AddUserMessage(content)
	return s.append(EntryUserMessage, openai.ChatMessageRoleUser, content, nil, nil)
}

func (s *InMemorySession) AppendAssistantMessage(content string, toolCalls []openai.ToolCall) (Entry, error) {
	s.ctxMgr.AddAssistantMessage(content, toolCalls)
	meta := map[string]any{}
	if len(toolCalls) > 0 {
		meta["tool_calls"] = toolCalls
	}
	return s.append(EntryAssistantMessage, openai.ChatMessageRoleAssistant, content, nil, meta)
}

func (s *InMemorySession) AppendToolResult(toolCallID, content string) (Entry, error) {
	s.ctxMgr.AddToolResult(toolCallID, content)
	return s.append(EntryToolResult, openai.ChatMessageRoleTool, content, nil, map[string]any{
		"tool_call_id": toolCallID,
	})
}

func (s *InMemorySession) AppendSystemNotice(content string) (Entry, error) {
	s.ctxMgr.AddSystemNotice(content)
	return s.append(EntrySystemNotice, openai.ChatMessageRoleSystem, content, nil, nil)
}

func (s *InMemorySession) AppendCompaction(summary string) error {
	s.ctxMgr.ReplaceWithSummary(summary)
	_, err := s.append(EntryCompaction, "", summary, nil, nil)
	return err
}

func (s *InMemorySession) AppendTodos(items any) (Entry, error) {
	return s.append(EntryTodos, "", "", nil, map[string]any{"items": items})
}

func (s *InMemorySession) AppendApprovalAlways(toolName string) (Entry, error) {
	s.alwaysAllow[toolName] = true
	return s.append(EntryApprovalAlways, "", toolName, nil, map[string]any{"tool": toolName})
}

func (s *InMemorySession) BuildContext() ([]openai.ChatCompletionMessage, error) {
	return s.ctxMgr.ChatCompletionMessages(), nil
}

// ReplayEntry reapplies a persisted entry onto ctxMgr / session state without re-appending.
func (s *InMemorySession) ReplayEntry(e Entry) error {
	switch e.Kind {
	case EntryUserMessage:
		s.ctxMgr.AddUserMessage(e.Content)
	case EntryAssistantMessage:
		var tcs []openai.ToolCall
		if e.Meta != nil {
			if raw, ok := e.Meta["tool_calls"]; ok {
				// Meta may hold []openai.ToolCall or JSON-decoded []any
				switch v := raw.(type) {
				case []openai.ToolCall:
					tcs = v
				}
			}
		}
		s.ctxMgr.AddAssistantMessage(e.Content, tcs)
	case EntryToolResult:
		id, _ := e.Meta["tool_call_id"].(string)
		s.ctxMgr.AddToolResult(id, e.Content)
	case EntrySystemNotice:
		s.ctxMgr.AddSystemNotice(e.Content)
	case EntryCompaction:
		s.ctxMgr.ReplaceWithSummary(e.Content)
	case EntryLabel:
		s.label = e.Content
	case EntryApprovalAlways:
		s.alwaysAllow[e.Content] = true
	case EntryTodos:
		// caller rehydrates TodoStore separately
	}
	s.entries = append(s.entries, e)
	s.parentID = e.ID
	if n := len(e.ID); n > 0 {
		// keep seq ahead of replayed IDs best-effort
		s.seq++
	}
	return nil
}
