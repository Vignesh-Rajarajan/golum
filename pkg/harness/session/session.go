package session

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
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
	EntryBranchSummary       EntryKind = "branch_summary"
	EntryLabel               EntryKind = "label"
	EntryCustom              EntryKind = "custom"
	EntryTodos               EntryKind = "todos"
	EntryApprovalAlways      EntryKind = "approval_always"
)

// Entry is one durable session record.
type Entry struct {
	ID       string         `json:"id"`
	ParentID string         `json:"parent_id,omitempty"`
	Seq      int            `json:"seq,omitempty"`
	Kind     EntryKind      `json:"kind"`
	Role     string         `json:"role,omitempty"`
	Content  string         `json:"content,omitempty"`
	ToolCall *llm.ToolCall  `json:"tool_call,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
	Time     time.Time      `json:"time"`
}

// ToolCallID returns the tool_call_id recorded on a tool-result entry, if any.
func (e Entry) ToolCallID() string {
	if e.Meta == nil {
		return ""
	}
	id, _ := e.Meta["tool_call_id"].(string)
	return id
}

// toolCallsFromMeta extracts assistant tool calls from entry metadata.
// Call RehydrateEntry first when the entry came from storage.
func toolCallsFromMeta(meta map[string]any) []openai.ToolCall {
	if meta == nil {
		return nil
	}
	raw, ok := meta["tool_calls"]
	if !ok {
		return nil
	}
	tcs, _ := raw.([]openai.ToolCall)
	return tcs
}

// RehydrateEntry normalizes an Entry decoded from storage so that Meta carries
// concrete types rather than generic JSON. Assistant tool_calls in particular
// must come back as []openai.ToolCall or ReplayEntry silently drops them,
// leaving assistant messages whose tool calls have no matching results.
func RehydrateEntry(e *Entry) {
	if e.Kind != EntryAssistantMessage || e.Meta == nil {
		return
	}
	raw, ok := e.Meta["tool_calls"]
	if !ok {
		return
	}
	if _, already := raw.([]openai.ToolCall); already {
		return
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return
	}
	var tcs []openai.ToolCall
	if json.Unmarshal(b, &tcs) == nil {
		e.Meta["tool_calls"] = tcs
	}
}

// Session is the durability + context surface used by RunAgentLoop.
type Session interface {
	ID() string
	AppendProvisioned(p ProvisionedEntry) (Entry, error)
	AppendRecord(r Record) (Record, error)
	FindRecords(q RecordQuery) ([]Record, error)
	FindOpenOperations(lane string, limit int) ([]Record, error)
	AppendUserMessage(content string) (Entry, error)
	AppendAssistantMessage(content string, toolCalls []openai.ToolCall) (Entry, error)
	AppendToolResult(toolCallID, content string) (Entry, error)
	AppendSystemNotice(content string) (Entry, error)
	AppendCompaction(summary string) error
	// AppendCompactionAt records a summary that folds history up to and
	// including cutEntryID, leaving later entries in the live context.
	AppendCompactionAt(summary, cutEntryID string) (Entry, error)
	AppendTodos(items any) (Entry, error)
	AppendApprovalAlways(toolName string) (Entry, error)
	AppendModelChange(model string) (Entry, error)
	AppendThinkingLevelChange(level string) (Entry, error)
	AppendActiveToolsChange(names []string) (Entry, error)
	BuildContext() ([]openai.ChatCompletionMessage, error)
	ContextManager() *contextmgr.ContextManager
	Entries() []Entry
	// ContextEntries returns the entries that make up the live context, i.e.
	// the log after the most recent compaction boundary is applied.
	ContextEntries() []Entry
	// RebuildContext reconstructs the ContextManager from ContextEntries.
	RebuildContext() error
	// Leaf returns the id of the current head of the session tree.
	Leaf() string
	// GetEntry looks up a single entry by id.
	GetEntry(id string) (Entry, bool)
	// GetPathToRoot returns the ancestor chain ending at id, root first.
	GetPathToRoot(id string) ([]Entry, error)
	// GetBranch returns id and everything descended from it.
	GetBranch(id string) ([]Entry, error)
	// MoveTo repoints the head at entryID and rebuilds context.
	MoveTo(entryID string) error
	AlwaysAllowed(toolName string) bool
	SetLabel(label string) error
	Label() string
}

// InMemorySession wraps ContextManager with Entry recording.
type InMemorySession struct {
	mu sync.RWMutex

	id          string
	label       string
	ctxMgr      *contextmgr.ContextManager
	entries     []Entry
	records     []Record
	parentID    string
	seq         int
	alwaysAllow map[string]bool
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

func (s *InMemorySession) ID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}

func (s *InMemorySession) ContextManager() *contextmgr.ContextManager { return s.ctxMgr }

func (s *InMemorySession) Entries() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

func (s *InMemorySession) Label() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.label
}

func (s *InMemorySession) SetLabel(label string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.label = label
	_, err := s.appendLocked(EntryLabel, "", label, nil, nil)
	return err
}

func (s *InMemorySession) AlwaysAllowed(toolName string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.alwaysAllow[toolName]
}

func (s *InMemorySession) appendLocked(kind EntryKind, role, content string, toolCall *llm.ToolCall, meta map[string]any) (Entry, error) {
	s.seq++
	e := Entry{
		ID:       NewEntryID(),
		ParentID: s.parentID,
		Seq:      s.seq,
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

// AppendProvisioned appends p exactly once. Repeating an identical write is a
// lookup; reusing the id for another payload is corruption.
func (s *InMemorySession) AppendProvisioned(p ProvisionedEntry) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		return Entry{}, fmt.Errorf("provisioned entry id required")
	}
	if existing, ok := s.getEntryLocked(p.ID); ok {
		if !p.Matches(existing) {
			return Entry{}, &ProvisionedEntryMismatchError{ID: p.ID}
		}
		return existing, nil
	}
	s.seq++
	e := Entry{
		ID: p.ID, ParentID: s.parentID, Seq: s.seq, Kind: p.Kind,
		Role: p.Role, Content: p.Content, ToolCall: p.ToolCall, Meta: p.Meta,
		Time: time.Now().UTC(),
	}
	s.appendStoredLocked(e)
	return e, nil
}

func (s *InMemorySession) appendStoredLocked(e Entry) {
	switch e.Kind {
	case EntryUserMessage:
		s.ctxMgr.AddUserMessage(e.Content)
	case EntryAssistantMessage:
		s.ctxMgr.AddAssistantMessage(e.Content, toolCallsFromMeta(e.Meta))
	case EntryToolResult:
		s.ctxMgr.AddToolResult(e.ToolCallID(), e.Content)
	case EntrySystemNotice:
		s.ctxMgr.AddSystemNotice(e.Content)
	case EntryBranchSummary:
		s.ctxMgr.AddSystemNotice(prompt.WrapBranchSummary(e.Content))
	case EntryLabel:
		s.label = e.Content
	case EntryApprovalAlways:
		s.alwaysAllow[e.Content] = true
	}
	s.entries = append(s.entries, e)
	s.parentID = e.ID
	if e.Seq > s.seq {
		s.seq = e.Seq
	}
}

type ProvisionedEntryMismatchError struct{ ID string }

func (e *ProvisionedEntryMismatchError) Error() string {
	return fmt.Sprintf("provisioned_entry_mismatch: entry %q has different payload", e.ID)
}

func (s *InMemorySession) AppendRecord(r Record) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.ID == "" {
		r.ID = NewRecordID()
	}
	if r.Lane == "" {
		r.Lane = "main"
	}
	if err := r.Validate(); err != nil {
		return Record{}, err
	}
	for _, existing := range s.records {
		if existing.ID == r.ID {
			return existing, nil
		}
	}
	s.seq++
	r.Seq = int64(s.seq)
	if r.Time.IsZero() {
		r.Time = time.Now().UTC()
	}
	s.records = append(s.records, r)
	return r, nil
}

func (s *InMemorySession) FindRecords(q RecordQuery) ([]Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Record
	for _, r := range s.records {
		if q.Lane != "" && r.Lane != q.Lane || q.RunID != "" && r.RunID != q.RunID ||
			q.Type != "" && r.Type != q.Type || r.Seq <= q.After {
			continue
		}
		out = append(out, r)
		if q.Limit > 0 && len(out) == q.Limit {
			break
		}
	}
	return out, nil
}

func (s *InMemorySession) FindOpenOperations(lane string, limit int) ([]Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	finished := map[string]bool{}
	for _, r := range s.records {
		if r.Type == RecordOperationFinished {
			finished[r.RunID] = true
		}
	}
	var out []Record
	for i := len(s.records) - 1; i >= 0; i-- {
		r := s.records[i]
		if r.Type != RecordOperationStarted || lane != "" && r.Lane != lane || finished[r.RunID] {
			continue
		}
		out = append(out, r)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

func (s *InMemorySession) AppendUserMessage(content string) (Entry, error) {
	return s.AppendProvisioned(provision(EntryUserMessage, openai.ChatMessageRoleUser, content, nil, nil))
}

func (s *InMemorySession) AppendAssistantMessage(content string, toolCalls []openai.ToolCall) (Entry, error) {
	meta := map[string]any{}
	if len(toolCalls) > 0 {
		meta["tool_calls"] = toolCalls
	}
	return s.AppendProvisioned(provision(EntryAssistantMessage, openai.ChatMessageRoleAssistant, content, nil, meta))
}

func (s *InMemorySession) AppendToolResult(toolCallID, content string) (Entry, error) {
	return s.AppendProvisioned(provision(EntryToolResult, openai.ChatMessageRoleTool, content, nil, map[string]any{
		"tool_call_id": toolCallID,
	}))
}

func (s *InMemorySession) AppendSystemNotice(content string) (Entry, error) {
	return s.AppendProvisioned(provision(EntrySystemNotice, openai.ChatMessageRoleSystem, content, nil, nil))
}

// AppendCompaction folds the entire history into a summary. Prefer
// AppendCompactionAt, which keeps recent turns.
func (s *InMemorySession) AppendCompaction(summary string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctxMgr.ReplaceWithSummary(summary)
	_, err := s.appendLocked(EntryCompaction, "", summary, nil, nil)
	return err
}

// AppendCompactionAt records a compaction boundary without discarding entries.
// The log keeps everything; only the derived context shrinks. The caller is
// responsible for having already applied the equivalent change to the
// ContextManager (see contextmgr.CompactPrefix) — this records the boundary so
// the same context can be rebuilt on reload.
func (s *InMemorySession) AppendCompactionAt(summary, cutEntryID string) (Entry, error) {
	return s.AppendProvisioned(provision(EntryCompaction, "", summary, nil, map[string]any{
		MetaCutEntryID: cutEntryID,
	}))
}

func (s *InMemorySession) AppendTodos(items any) (Entry, error) {
	return s.AppendProvisioned(provision(EntryTodos, "", "", nil, map[string]any{"items": items}))
}

func (s *InMemorySession) AppendApprovalAlways(toolName string) (Entry, error) {
	return s.AppendProvisioned(provision(EntryApprovalAlways, "", toolName, nil, map[string]any{"tool": toolName}))
}

func (s *InMemorySession) AppendModelChange(model string) (Entry, error) {
	return s.AppendProvisioned(provision(EntryModelChange, "", model, nil, map[string]any{"model": model}))
}

func (s *InMemorySession) AppendThinkingLevelChange(level string) (Entry, error) {
	return s.AppendProvisioned(provision(EntryThinkingLevelChange, "", level, nil, map[string]any{"level": level}))
}

func (s *InMemorySession) AppendActiveToolsChange(names []string) (Entry, error) {
	return s.AppendProvisioned(provision(EntryActiveToolsChange, "", "", nil, map[string]any{"tools": names}))
}

func (s *InMemorySession) BuildContext() ([]openai.ChatCompletionMessage, error) {
	return s.ctxMgr.ChatCompletionMessages(), nil
}

// LoadEntry appends a stored entry to the log WITHOUT applying it to the
// ContextManager.
//
// Loading must not replay linearly: a compaction entry is appended at the end
// of the log but represents a boundary near its start, so replaying in order
// would let it wipe the very tail it was meant to preserve. Callers load the
// whole log with LoadEntry and then call RebuildContext, which applies the
// derived context in the right order.
func (s *InMemorySession) LoadEntry(e Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadEntryLocked(e)
}

func (s *InMemorySession) loadEntryLocked(e Entry) {
	RehydrateEntry(&e)
	s.entries = append(s.entries, e)
	s.parentID = e.ID
	if e.Kind == EntryLabel {
		s.label = e.Content
	}
	if e.Kind == EntryApprovalAlways {
		s.alwaysAllow[e.Content] = true
	}
	if e.Seq > s.seq {
		s.seq = e.Seq
	} else {
		s.seq++
	}
}

// ReplayEntry reapplies a persisted entry onto ctxMgr / session state without re-appending.
func (s *InMemorySession) ReplayEntry(e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	RehydrateEntry(&e)
	switch e.Kind {
	case EntryUserMessage:
		s.ctxMgr.AddUserMessage(e.Content)
	case EntryAssistantMessage:
		s.ctxMgr.AddAssistantMessage(e.Content, toolCallsFromMeta(e.Meta))
	case EntryToolResult:
		s.ctxMgr.AddToolResult(e.ToolCallID(), e.Content)
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
	// Keep the counter ahead of replayed entries so newly appended entries sort
	// after everything already on disk.
	if e.Seq > s.seq {
		s.seq = e.Seq
	} else {
		s.seq++
	}
	return nil
}
