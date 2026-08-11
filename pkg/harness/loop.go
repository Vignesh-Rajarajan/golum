package harness

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/applog"
	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/hooks"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/memory"
	"github.com/Vignesh-Rajarajan/golum/pkg/observability"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/Vignesh-Rajarajan/golum/pkg/sanitize"
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
	MaxToolResultBytes   int
	ToolExecTimeout      time.Duration
	StreamTimeout        time.Duration
}

// DefaultLoopConfig returns sensible defaults.
func DefaultLoopConfig() LoopConfig {
	return LoopConfig{
		MaxModelInvocations:  50,
		MaxToolCallsPerTurn:  20,
		MaxWallClock:         30 * time.Minute,
		MaxConsecutiveErrors: 5,
		LoopDetectionWindow:  3,
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
				Intent:       &session.OperationIntent{Kind: "run"},
			}); err != nil {
				return err
			}
		}
		return NewDriver(deps, cfg, emit).RunToCompletion(runCtx)
	})
}

func runAgentLoopInner(
	ctx context.Context,
	deps LoopDeps,
	cfg LoopConfig,
	emit func(AgentEvent),
	deadline time.Time,
) error {
	client := deps.Client
	sess := deps.Session
	todos := deps.Todos

	llmTools := deps.Registry.AsLLMTools()
	var recentHashes []string
	toolCallsThisTurn := 0
	consecutiveErrors := 0
	overflowRetried := false

	for invocation := 0; invocation < cfg.MaxModelInvocations; invocation++ {
		if err := ctx.Err(); err != nil {
			emit(AgentEvent{Type: EventTurnDone, Cancelled: true})
			return err
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			emit(AgentEvent{Type: EventError, Err: fmt.Errorf("wall-clock limit exceeded (%s)", cfg.MaxWallClock)})
			emit(AgentEvent{Type: EventTurnDone})
			return fmt.Errorf("wall-clock limit exceeded")
		}

		// Compact before building the request, not after a failure: the estimate
		// is available now, whereas reported usage only arrives with a response.
		if deps.Compactor != nil {
			if _, cErr := deps.Compactor.MaybeCompact(ctx, sess, emit); cErr != nil {
				// A failed compaction is not fatal — the request may still fit.
				applog.Printf("loop: auto-compaction failed: %v", cErr)
			}
		}

		messages, err := sess.BuildContext()
		if err != nil {
			emit(AgentEvent{Type: EventError, Err: err})
			emit(AgentEvent{Type: EventTurnDone})
			return err
		}

		opts := llm.ChatCompletionOptions{
			Stream:     true,
			Tools:      llmTools,
			MaxRetries: 3,
			Timeout:    cfg.StreamTimeout,
		}
		events := client.ChatCompletion(ctx, messages, opts)

		var content strings.Builder
		var toolCalls []*llm.ToolCall
		var usageMeta map[string]string
		var streamErr error
		cancelled := false

		for ev := range events {
			switch ev.Type {
			case llm.EventTypeContentDelta:
				content.WriteString(ev.Content)
				emit(AgentEvent{Type: EventContentDelta, Content: ev.Content})
			case llm.EventTypeThinkingDelta:
				emit(AgentEvent{Type: EventThinkingDelta, Content: ev.Content})
			case llm.EventTypeToolCall:
				if ev.Tool != nil {
					toolCalls = append(toolCalls, ev.Tool)
				}
			case llm.EventTypeContentDone:
				usageMeta = ev.Meta
				cancelled = ev.Cancelled
				emit(AgentEvent{Type: EventContentDone, Meta: ev.Meta, Cancelled: ev.Cancelled})
			case llm.EventTypeError:
				streamErr = ev.Error
			}
		}

		// A context-overflow rejection is recoverable exactly once: compact and
		// retry the same turn rather than losing the user's request.
		if streamErr != nil {
			if llm.IsContextOverflowError(streamErr) && !overflowRetried && deps.Compactor != nil {
				overflowRetried = true
				applog.Printf("loop: context overflow; compacting and retrying once")
				if _, cErr := deps.Compactor.Compact(ctx, sess, emit); cErr == nil {
					continue
				}
			}
			emit(AgentEvent{Type: EventError, Err: streamErr})
			emit(AgentEvent{Type: EventTurnDone})
			return streamErr
		}

		if cancelled {
			// Synthesize results for any collected tool calls so context stays balanced
			if len(toolCalls) > 0 {
				cleaned := sanitize.StripPseudoToolMarkup(content.String())
				openaiCalls := toOpenAIToolCalls(toolCalls)
				_, _ = sess.AppendAssistantMessage(cleaned, openaiCalls)
				for _, tc := range toolCalls {
					_, _ = sess.AppendToolResult(tc.ID, "Tool call cancelled.")
				}
			}
			emit(AgentEvent{Type: EventTurnDone, Cancelled: true})
			return context.Canceled
		}

		cleaned := sanitize.StripPseudoToolMarkup(content.String())
		openaiCalls := toOpenAIToolCalls(toolCalls)
		if _, err := sess.AppendAssistantMessage(cleaned, openaiCalls); err != nil {
			emit(AgentEvent{Type: EventError, Err: err})
			emit(AgentEvent{Type: EventTurnDone})
			return err
		}

		if cm := sess.ContextManager(); cm != nil && usageMeta != nil {
			u := contextmgr.TokenUsageFromMeta(usageMeta)
			if u.TotalTokens > 0 || u.PromptTokens > 0 || u.CompletionTokens > 0 {
				cm.SetLatestUsage(u)
				cm.AddUsage(u)
			}
		}

		if len(toolCalls) == 0 {
			recordEpisode(ctx, deps)
			emit(AgentEvent{Type: EventTurnDone})
			return nil
		}

		for i, tc := range toolCalls {
			if err := ctx.Err(); err != nil {
				// Cancel remaining tool calls with synthetic results
				for _, rem := range toolCalls[i:] {
					_, _ = sess.AppendToolResult(rem.ID, "Tool call cancelled.")
				}
				emit(AgentEvent{Type: EventTurnDone, Cancelled: true})
				return err
			}

			toolCallsThisTurn++
			if toolCallsThisTurn > cfg.MaxToolCallsPerTurn {
				msg := fmt.Sprintf("tool call limit exceeded (%d per turn)", cfg.MaxToolCallsPerTurn)
				_, _ = sess.AppendToolResult(tc.ID, msg)
				for _, rem := range toolCalls[i+1:] {
					_, _ = sess.AppendToolResult(rem.ID, msg)
				}
				emit(AgentEvent{Type: EventError, Err: fmt.Errorf("%s", msg)})
				emit(AgentEvent{Type: EventTurnDone})
				return fmt.Errorf("%s", msg)
			}

			emit(AgentEvent{Type: EventToolCallStart, ToolCall: tc})

			resultContent, result, todosChanged := dispatchToolCall(ctx, tc, deps, cfg, emit)
			if _, err := sess.AppendToolResult(tc.ID, resultContent); err != nil {
				emit(AgentEvent{Type: EventError, Err: err})
				emit(AgentEvent{Type: EventTurnDone})
				return err
			}
			deps.Episodic.Observe(tc, result.IsError, resultContent)
			emit(AgentEvent{Type: EventToolCallResult, ToolCall: tc, ToolResult: &result})
			if todosChanged && todos != nil {
				emit(AgentEvent{Type: EventTodosChanged, Todos: todos.List()})
			}

			if result.IsError {
				consecutiveErrors++
				if cfg.MaxConsecutiveErrors > 0 && consecutiveErrors >= cfg.MaxConsecutiveErrors {
					err := fmt.Errorf("too many consecutive tool errors (%d)", consecutiveErrors)
					emit(AgentEvent{Type: EventError, Err: err})
					emit(AgentEvent{Type: EventTurnDone})
					return err
				}
			} else {
				consecutiveErrors = 0
			}

			h := toolCallHash(tc)
			recentHashes = append(recentHashes, h)
			if len(recentHashes) > cfg.LoopDetectionWindow*2 {
				recentHashes = recentHashes[len(recentHashes)-cfg.LoopDetectionWindow*2:]
			}
			if countRecent(recentHashes, h) >= cfg.LoopDetectionWindow {
				notice := prompt.CreateLoopBreakerPrompt(fmt.Sprintf("repeated tool call %s", tc.Name))
				_, _ = sess.AppendSystemNotice(notice)
			}
		}
	}

	err := fmt.Errorf("model invocation limit exceeded (%d)", cfg.MaxModelInvocations)
	emit(AgentEvent{Type: EventError, Err: err})
	emit(AgentEvent{Type: EventTurnDone})
	return err
}

func dispatchToolCall(
	ctx context.Context,
	tc *llm.ToolCall,
	deps LoopDeps,
	cfg LoopConfig,
	emit func(AgentEvent),
) (content string, result tool.Result, todosChanged bool) {
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
	content = truncateResult(res.Content, cfg.MaxToolResultBytes)
	res.Content = content
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
	half := (max - 40) / 2
	if half < 1 {
		return s[:max]
	}
	return s[:half] + "\n\n...[tool result truncated]...\n\n" + s[len(s)-half:]
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
