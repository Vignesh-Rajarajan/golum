package harnesstest

import (
	"errors"
	"sync"

	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/sashabaranov/go-openai"
)

// ErrInjected is the synthetic crash a FaultSession returns at a durable boundary.
var ErrInjected = errors.New("harnesstest: injected crash")

// Boundary names a durable write the crash matrix can fail at.
type Boundary string

const (
	BoundaryOperationStarted       Boundary = "operation_started"
	BoundaryStepAttempt            Boundary = "step_attempt"
	BoundaryAssistantPersisted     Boundary = "assistant_persisted"
	BoundaryToolStarted            Boundary = "tool_started"
	BoundaryToolResultPersisted    Boundary = "tool_result_persisted"
	BoundaryCompactionPersisted    Boundary = "compaction_persisted"
	BoundaryQueueConsumed          Boundary = "queue_consumed"
	BoundaryOperationFinished      Boundary = "operation_finished"
	BoundaryBeforeOperationFinish  Boundary = "before_operation_finished"
	BoundaryDuringToolExecution    Boundary = "during_tool_execution"
	BoundaryDuringQueueConsumption Boundary = "during_queue_consumption"
	BoundaryDuringCompaction       Boundary = "during_compaction"
)

// FaultSession wraps a Session and can fail before or after a named write.
// It is a test-only wrapper; production code never sees it.
type FaultSession struct {
	inner session.Session

	mu       sync.Mutex
	after    map[Boundary]int
	before   map[Boundary]int
	seenA    map[Boundary]int
	seenB    map[Boundary]int
	tripped  bool
	lastFail Boundary
}

// WrapFault returns a FaultSession around inner.
func WrapFault(inner session.Session) *FaultSession {
	return &FaultSession{
		inner:  inner,
		after:  map[Boundary]int{},
		before: map[Boundary]int{},
		seenA:  map[Boundary]int{},
		seenB:  map[Boundary]int{},
	}
}

// Inner returns the wrapped session.
func (f *FaultSession) Inner() session.Session { return f.inner }

// FailAt is FailAfter(boundary, nth). nth is 1-based.
func (f *FaultSession) FailAt(boundary Boundary, nth int) {
	f.FailAfter(boundary, nth)
}

// FailAfter injects ErrInjected after the nth matching durable write succeeds.
func (f *FaultSession) FailAfter(boundary Boundary, nth int) {
	if nth <= 0 {
		nth = 1
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.after[boundary] = nth
}

// FailBefore injects ErrInjected before the nth matching durable write.
func (f *FaultSession) FailBefore(boundary Boundary, nth int) {
	if nth <= 0 {
		nth = 1
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.before[boundary] = nth
}

// Tripped reports whether a crash has already been injected.
func (f *FaultSession) Tripped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tripped
}

// LastFailure is the boundary that tripped, if any.
func (f *FaultSession) LastFailure() Boundary {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastFail
}

// Hit records a boundary crossing and returns ErrInjected when the armed
// before/after count is reached. Tests and TestRig call this for "during"
// crashes that are not themselves session writes.
func (f *FaultSession) Hit(b Boundary, afterWrite bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tripped || b == "" {
		return nil
	}
	counts := f.seenB
	want := f.before
	if afterWrite {
		counts = f.seenA
		want = f.after
	}
	n, armed := want[b]
	if !armed {
		return nil
	}
	counts[b]++
	if counts[b] != n {
		return nil
	}
	f.tripped = true
	f.lastFail = b
	return ErrInjected
}

func boundaryForRecord(r session.Record) Boundary {
	switch r.Type {
	case session.RecordOperationStarted:
		return BoundaryOperationStarted
	case session.RecordStepAttempt:
		return BoundaryStepAttempt
	case session.RecordToolStarted:
		return BoundaryToolStarted
	case session.RecordOperationFinished:
		return BoundaryOperationFinished
	default:
		return ""
	}
}

func boundaryForEntry(e session.Entry) Boundary {
	switch e.Kind {
	case session.EntryAssistantMessage:
		return BoundaryAssistantPersisted
	case session.EntryToolResult:
		return BoundaryToolResultPersisted
	case session.EntryCompaction:
		return BoundaryCompactionPersisted
	default:
		return ""
	}
}

func (f *FaultSession) AppendRecord(r session.Record) (session.Record, error) {
	b := boundaryForRecord(r)
	if r.Type == session.RecordOperationFinished {
		if err := f.Hit(BoundaryBeforeOperationFinish, false); err != nil {
			return session.Record{}, err
		}
	}
	if err := f.Hit(b, false); err != nil {
		return session.Record{}, err
	}
	stored, err := f.inner.AppendRecord(r)
	if err != nil {
		return stored, err
	}
	if err := f.Hit(b, true); err != nil {
		return stored, err
	}
	return stored, nil
}

func (f *FaultSession) AppendProvisioned(p session.ProvisionedEntry) (session.Entry, error) {
	probe := session.Entry{Kind: p.Kind}
	b := boundaryForEntry(probe)
	if err := f.Hit(b, false); err != nil {
		return session.Entry{}, err
	}
	stored, err := f.inner.AppendProvisioned(p)
	if err != nil {
		return stored, err
	}
	if err := f.Hit(b, true); err != nil {
		return stored, err
	}
	return stored, nil
}

func (f *FaultSession) ID() string { return f.inner.ID() }

func (f *FaultSession) FindRecords(q session.RecordQuery) ([]session.Record, error) {
	return f.inner.FindRecords(q)
}

func (f *FaultSession) FindOpenOperations(lane string, limit int) ([]session.Record, error) {
	return f.inner.FindOpenOperations(lane, limit)
}

func (f *FaultSession) AppendUserMessage(content string) (session.Entry, error) {
	return f.inner.AppendUserMessage(content)
}

func (f *FaultSession) AppendAssistantMessage(content string, toolCalls []openai.ToolCall) (session.Entry, error) {
	return f.inner.AppendAssistantMessage(content, toolCalls)
}

func (f *FaultSession) AppendToolResult(toolCallID, content string) (session.Entry, error) {
	return f.inner.AppendToolResult(toolCallID, content)
}

func (f *FaultSession) AppendSystemNotice(content string) (session.Entry, error) {
	return f.inner.AppendSystemNotice(content)
}

func (f *FaultSession) AppendCompaction(summary string) error {
	return f.inner.AppendCompaction(summary)
}

func (f *FaultSession) AppendCompactionAt(summary, cutEntryID string) (session.Entry, error) {
	if err := f.Hit(BoundaryCompactionPersisted, false); err != nil {
		return session.Entry{}, err
	}
	e, err := f.inner.AppendCompactionAt(summary, cutEntryID)
	if err != nil {
		return e, err
	}
	if err := f.Hit(BoundaryCompactionPersisted, true); err != nil {
		return e, err
	}
	return e, nil
}

func (f *FaultSession) AppendTodos(items any) (session.Entry, error) {
	return f.inner.AppendTodos(items)
}

func (f *FaultSession) AppendApprovalAlways(toolName string) (session.Entry, error) {
	return f.inner.AppendApprovalAlways(toolName)
}

func (f *FaultSession) AppendModelChange(model string) (session.Entry, error) {
	return f.inner.AppendModelChange(model)
}

func (f *FaultSession) AppendThinkingLevelChange(level string) (session.Entry, error) {
	return f.inner.AppendThinkingLevelChange(level)
}

func (f *FaultSession) AppendActiveToolsChange(names []string) (session.Entry, error) {
	return f.inner.AppendActiveToolsChange(names)
}

func (f *FaultSession) BuildContext() ([]openai.ChatCompletionMessage, error) {
	return f.inner.BuildContext()
}

func (f *FaultSession) ContextManager() *contextmgr.ContextManager {
	return f.inner.ContextManager()
}

func (f *FaultSession) Entries() []session.Entry { return f.inner.Entries() }

func (f *FaultSession) ContextEntries() []session.Entry { return f.inner.ContextEntries() }

func (f *FaultSession) RebuildContext() error { return f.inner.RebuildContext() }

func (f *FaultSession) Leaf() string { return f.inner.Leaf() }

func (f *FaultSession) GetEntry(id string) (session.Entry, bool) { return f.inner.GetEntry(id) }

func (f *FaultSession) GetPathToRoot(id string) ([]session.Entry, error) {
	return f.inner.GetPathToRoot(id)
}

func (f *FaultSession) GetBranch(id string) ([]session.Entry, error) {
	return f.inner.GetBranch(id)
}

func (f *FaultSession) MoveTo(entryID string) error { return f.inner.MoveTo(entryID) }

func (f *FaultSession) AlwaysAllowed(toolName string) bool {
	return f.inner.AlwaysAllowed(toolName)
}

func (f *FaultSession) SetLabel(label string) error { return f.inner.SetLabel(label) }

func (f *FaultSession) Label() string { return f.inner.Label() }
