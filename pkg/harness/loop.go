package harness

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/hooks"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm/capability"
	"github.com/Vignesh-Rajarajan/golum/pkg/memory"
	"github.com/Vignesh-Rajarajan/golum/pkg/observability"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
	"github.com/sashabaranov/go-openai"
)

// AgentEventType classifies events emitted by RunAgentLoop.
type AgentEventType string

const (
	EventContentDelta             AgentEventType = "content_delta"
	EventThinkingDelta            AgentEventType = "thinking_delta"
	EventContentDone              AgentEventType = "content_done"
	EventToolCallStart            AgentEventType = "tool_call_start"
	EventToolCallAwaitingApproval AgentEventType = "tool_call_awaiting_approval"
	EventToolCallResult           AgentEventType = "tool_call_result"
	EventTodosChanged             AgentEventType = "todos_changed"
	EventQueueConsumed            AgentEventType = "queue_consumed"
	EventCompactionStart          AgentEventType = "compaction_start"
	EventCompactionDone           AgentEventType = "compaction_done"
	EventError                    AgentEventType = "error"
	EventTurnDone                 AgentEventType = "turn_done"
)

// AgentEvent is one event from the agent loop to the UI.
type AgentEvent struct {
	Type       AgentEventType
	Content    string
	ToolCall   *llm.ToolCall
	ToolResult *tool.Result
	Todos      []tool.TodoItem
	Compaction *CompactionResult
	Meta       map[string]string
	Err        error
	Cancelled  bool
}

// LoopConfig configures guardrails for one user turn.
type LoopConfig struct {
	MaxModelInvocations  int
	MaxToolCallsPerTurn  int
	MaxWallClock         time.Duration
	MaxConsecutiveErrors int
	LoopDetectionWindow  int
	// ForceTool, when set, is copied onto the operation intent so a run cannot
	// complete without a successful invocation of that tool.
	ForceTool          string
	ForceToolAttempts  int
	MaxToolResultBytes int
	ToolExecTimeout    time.Duration
	StreamTimeout      time.Duration
}

// DefaultLoopConfig returns sensible defaults.
func DefaultLoopConfig() LoopConfig {
	return LoopConfig{
		MaxModelInvocations:  50,
		MaxToolCallsPerTurn:  20,
		MaxWallClock:         30 * time.Minute,
		MaxConsecutiveErrors: 5,
		LoopDetectionWindow:  3,
		ForceToolAttempts:    2,
		MaxToolResultBytes:   100_000,
		ToolExecTimeout:      120 * time.Second,
		StreamTimeout:        10 * time.Minute,
	}
}

// ApprovalBroker gates mutating tool calls.
type ApprovalBroker interface {
	Request(ctx context.Context, call llm.ToolCall) (approved bool, err error)
}

// AutoApprove always approves (for tests / non-interactive).
type AutoApprove struct{}

func (AutoApprove) Request(context.Context, llm.ToolCall) (bool, error) { return true, nil }

// LoopDeps bundles the collaborators RunAgentLoop needs. Grouping them keeps
// the loop callable (and testable) without a ten-argument signature.
type LoopDeps struct {
	Client      *llm.Client
	Session     session.Session
	Registry    *tool.Registry
	Env         execenv.ExecutionEnv
	Approvals   ApprovalBroker
	Todos       *tool.TodoStore
	Compactor   *Compactor
	Hooks       *hooks.HooksManager
	Model       string
	ActiveTools []string
	// Memory, when set, receives an episodic digest at the end of each turn.
	Memory *memory.Store
	// Episodic accumulates file changes and failures within a turn.
	Episodic *EpisodicTracker
	// Directors intercept inference and candidate completion. Nil uses DefaultDirectors.
	Directors []Director
	// Caps is the model/provider compatibility table. Nil uses capability.Default.
	Caps *capability.Registry
}

// RunAgentLoop drives model ↔ tool rounds until the turn completes or a guardrail trips.
func RunAgentLoop(
	ctx context.Context,
	deps LoopDeps,
	cfg LoopConfig,
	emit func(AgentEvent),
) error {
	if cfg.MaxModelInvocations <= 0 {
		cfg.MaxModelInvocations = DefaultLoopConfig().MaxModelInvocations
	}
	if cfg.MaxToolCallsPerTurn <= 0 {
		cfg.MaxToolCallsPerTurn = DefaultLoopConfig().MaxToolCallsPerTurn
	}
	if cfg.LoopDetectionWindow <= 0 {
		cfg.LoopDetectionWindow = DefaultLoopConfig().LoopDetectionWindow
	}
	if cfg.ForceToolAttempts <= 0 {
		cfg.ForceToolAttempts = DefaultLoopConfig().ForceToolAttempts
	}
	if cfg.MaxToolResultBytes <= 0 {
		cfg.MaxToolResultBytes = DefaultLoopConfig().MaxToolResultBytes
	}
	if cfg.ToolExecTimeout <= 0 {
		cfg.ToolExecTimeout = DefaultLoopConfig().ToolExecTimeout
	}
	if deps.Approvals == nil {
		deps.Approvals = AutoApprove{}
	}
	if emit == nil {
		emit = func(AgentEvent) {}
	}

	return observability.Wrap(observability.DefaultSink, "RunAgentLoop", func() error {
		runCtx := ctx
		cancel := func() {}
		if cfg.MaxWallClock > 0 {
			runCtx, cancel = context.WithTimeout(ctx, cfg.MaxWallClock)
		}
		defer cancel()
		open, err := deps.Session.FindOpenOperations("main", 2)
		if err != nil {
			return err
		}
		if len(open) > 1 {
			return &CorruptionError{Reason: CorruptionMultipleOpenOperations, Detail: "main"}
		}
		if len(open) == 0 {
			runID := session.NewRecordID()
			if _, err := deps.Session.AppendRecord(session.Record{
				ID: stableID("r_start_", runID), Lane: "main",
				Type: session.RecordOperationStarted, RunID: runID,
				SourceLeafID: deps.Session.Leaf(),
				Intent: &session.OperationIntent{
					Kind: "run", ForceTool: cfg.ForceTool, ForceToolAttempts: cfg.ForceToolAttempts,
				},
			}); err != nil {
				return err
			}
		}
		return NewDriver(deps, cfg, emit).RunToCompletion(runCtx)
	})
}

func dispatchToolCall(
	ctx context.Context,
	tc *llm.ToolCall,
	deps LoopDeps,
	cfg LoopConfig,
	emit func(AgentEvent),
) (content string, result tool.Result, todosChanged bool) {
	startedAt := time.Now()
	defer func() { result.Duration = time.Since(startedAt) }()
	registry := deps.Registry
	env := deps.Env
	approvals := deps.Approvals
	sess := deps.Session
	todos := deps.Todos
	if approvals == nil {
		approvals = AutoApprove{}
	}
	if tc.ArgsErr != nil {
		msg := fmt.Sprintf("Invalid tool arguments: %v", tc.ArgsErr)
		return msg, tool.Result{Content: msg, IsError: true, Display: "invalid args"}, false
	}

	t, ok := registry.Get(tc.Name)
	if !ok {
		msg := fmt.Sprintf("Unknown tool: %s", tc.Name)
		return msg, tool.Result{Content: msg, IsError: true, Display: "unknown tool"}, false
	}

	args := tc.Arguments
	if args == nil {
		args = map[string]interface{}{}
	}

	if tool.RequiresApproval(tc.Name) && !sess.AlwaysAllowed(tc.Name) {
		emit(AgentEvent{Type: EventToolCallAwaitingApproval, ToolCall: tc})
		approved, err := approvals.Request(ctx, *tc)
		if err != nil {
			msg := fmt.Sprintf("Tool call cancelled: %v", err)
			return msg, tool.Result{Content: msg, IsError: true, Display: "cancelled"}, false
		}
		if !approved {
			msg := "Tool call rejected by user."
			return msg, tool.Result{Content: msg, IsError: true, Display: "rejected"}, false
		}
	}

	execCtx, cancel := context.WithTimeout(ctx, cfg.ToolExecTimeout)
	defer cancel()

	res, err := t.Execute(execCtx, args, env)
	if err != nil {
		msg := fmt.Sprintf("Tool execution failed: %v", err)
		return msg, tool.Result{Content: msg, IsError: true, Display: "exec error"}, false
	}
	res.OutputBytes = len(res.Content)
	content = res.Content
	todosChanged = tc.Name == "todos"
	if todosChanged && todos != nil {
		_, _ = sess.AppendTodos(todos.List())
	}
	return content, res, todosChanged
}

func truncateResult(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	const marker = "\n\n...[tool result truncated]...\n\n"
	if max <= len(marker) {
		return safePrefix(s, max)
	}
	remaining := max - len(marker)
	head := remaining / 2
	return safePrefix(s, head) + marker + safeSuffix(s, remaining-head)
}

func safePrefix(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.ValidString(s[:n]) {
		n--
	}
	return s[:n]
}

func safeSuffix(s string, n int) string {
	if n >= len(s) {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.ValidString(s[start:]) {
		start++
	}
	return s[start:]
}

func toOpenAIToolCalls(calls []*llm.ToolCall) []openai.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]openai.ToolCall, len(calls))
	for i, tc := range calls {
		out[i] = tc.ToOpenAI()
	}
	return out
}

func toolCallHash(tc *llm.ToolCall) string {
	args := tc.Arguments
	if args == nil {
		args = map[string]interface{}{}
	}
	canon, _ := canonicalJSON(args)
	sum := sha256.Sum256([]byte(tc.Name + "\n" + canon))
	return fmt.Sprintf("%x", sum[:8])
}

func canonicalJSON(v any) (string, error) {
	// Sort object keys recursively via encoding/json of a normalized map
	normalized, err := normalizeJSON(v)
	if err != nil {
		b, _ := json.Marshal(v)
		return string(b), nil
	}
	b, err := json.Marshal(normalized)
	return string(b), err
}

func normalizeJSON(v any) (any, error) {
	switch t := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]interface{}, len(t))
		for _, k := range keys {
			nv, err := normalizeJSON(t[k])
			if err != nil {
				return nil, err
			}
			out[k] = nv
		}
		return out, nil
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, el := range t {
			nv, err := normalizeJSON(el)
			if err != nil {
				return nil, err
			}
			out[i] = nv
		}
		return out, nil
	default:
		return t, nil
	}
}

func countRecent(hashes []string, h string) int {
	n := 0
	for _, x := range hashes {
		if x == h {
			n++
		}
	}
	return n
}
