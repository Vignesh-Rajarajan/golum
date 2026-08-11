package hooks

import (
	"context"
	"sync"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
	"github.com/sashabaranov/go-openai"
)

// HookKind identifies a lifecycle point.
type HookKind string

const (
	BeforeAgentStart HookKind = "before_agent_start"
	BeforeToolExec   HookKind = "before_tool_exec"
	AfterToolExec    HookKind = "after_tool_exec"
	AfterAgentTurn   HookKind = "after_agent_turn"
	BeforeRun        HookKind = "before_run"
	BeforeResume     HookKind = "before_resume"
	BeforeRunEnd     HookKind = "before_run_end"
	TransformContext HookKind = "transform_context"
	BeforeRequest    HookKind = "before_request"
	BeforePayload    HookKind = "before_payload"
	AfterResponse    HookKind = "after_response"
	BeforeTool       HookKind = "before_tool"
	AfterTool        HookKind = "after_tool"
	BeforeCompaction HookKind = "before_compaction"
	BeforeNavigation HookKind = "before_navigation"
)

type BeforeToolEvent struct {
	ToolName string
	Args     map[string]any
	Skip     bool
	Result   *tool.Result
}

type AfterToolEvent struct {
	ToolName string
	Args     map[string]any
	Result   *tool.Result
}

type TransformContextEvent struct {
	Messages *[]openai.ChatCompletionMessage
}

type BeforeRequestEvent struct {
	Messages *[]openai.ChatCompletionMessage
	Options  *llm.ChatCompletionOptions
}

type PayloadEvent struct{ Payload map[string]any }
type ResponseEvent struct{ Response any }
type RunEvent struct{ Prompt string }
type RunEndEvent struct{ Err error }
type CompactionEvent struct{ Instructions string }
type NavigationEvent struct{ TargetID string }

// HookEvent is delivered to subscribers.
type HookEvent struct {
	Kind    HookKind
	Payload any
	Time    time.Time
}

// HookHandler handles a hook event.
type HookHandler func(ctx context.Context, ev HookEvent) error

// HooksManager fans out lifecycle events to independent subscribers.
type HooksManager struct {
	mu       sync.RWMutex
	handlers map[HookKind][]HookHandler
}

// New creates an empty HooksManager.
func New() *HooksManager {
	return &HooksManager{handlers: make(map[HookKind][]HookHandler)}
}

// On registers a handler for a kind.
func (m *HooksManager) On(kind HookKind, h HookHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[kind] = append(m.handlers[kind], h)
}

// Emit delivers an event to all handlers for its kind. First error is returned.
func (m *HooksManager) Emit(ctx context.Context, kind HookKind, payload any) error {
	m.mu.RLock()
	hs := append([]HookHandler(nil), m.handlers[kind]...)
	m.mu.RUnlock()
	ev := HookEvent{Kind: kind, Payload: payload, Time: time.Now().UTC()}
	for _, h := range hs {
		if err := h(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}

func (m *HooksManager) OnBeforeTool(fn func(context.Context, *BeforeToolEvent) error) {
	m.On(BeforeTool, func(ctx context.Context, ev HookEvent) error {
		return fn(ctx, ev.Payload.(*BeforeToolEvent))
	})
}

func (m *HooksManager) OnAfterTool(fn func(context.Context, *AfterToolEvent) error) {
	m.On(AfterTool, func(ctx context.Context, ev HookEvent) error {
		return fn(ctx, ev.Payload.(*AfterToolEvent))
	})
}

func (m *HooksManager) OnTransformContext(fn func(context.Context, *[]openai.ChatCompletionMessage) error) {
	m.On(TransformContext, func(ctx context.Context, ev HookEvent) error {
		return fn(ctx, ev.Payload.(*TransformContextEvent).Messages)
	})
}

func (m *HooksManager) OnBeforeRequest(fn func(context.Context, *BeforeRequestEvent) error) {
	m.On(BeforeRequest, func(ctx context.Context, ev HookEvent) error {
		return fn(ctx, ev.Payload.(*BeforeRequestEvent))
	})
}

func (m *HooksManager) OnBeforeRun(fn func(context.Context, *RunEvent) error) {
	m.On(BeforeRun, func(ctx context.Context, ev HookEvent) error { return fn(ctx, ev.Payload.(*RunEvent)) })
}

func (m *HooksManager) OnBeforeResume(fn func(context.Context, *RunEvent) error) {
	m.On(BeforeResume, func(ctx context.Context, ev HookEvent) error { return fn(ctx, ev.Payload.(*RunEvent)) })
}

func (m *HooksManager) OnBeforeRunEnd(fn func(context.Context, *RunEndEvent) error) {
	m.On(BeforeRunEnd, func(ctx context.Context, ev HookEvent) error { return fn(ctx, ev.Payload.(*RunEndEvent)) })
}

func (m *HooksManager) OnBeforePayload(fn func(context.Context, *PayloadEvent) error) {
	m.On(BeforePayload, func(ctx context.Context, ev HookEvent) error {
		return fn(ctx, ev.Payload.(*PayloadEvent))
	})
}

func (m *HooksManager) OnAfterResponse(fn func(context.Context, *ResponseEvent) error) {
	m.On(AfterResponse, func(ctx context.Context, ev HookEvent) error {
		return fn(ctx, ev.Payload.(*ResponseEvent))
	})
}

func (m *HooksManager) OnBeforeCompaction(fn func(context.Context, *CompactionEvent) error) {
	m.On(BeforeCompaction, func(ctx context.Context, ev HookEvent) error {
		return fn(ctx, ev.Payload.(*CompactionEvent))
	})
}

func (m *HooksManager) OnBeforeNavigation(fn func(context.Context, *NavigationEvent) error) {
	m.On(BeforeNavigation, func(ctx context.Context, ev HookEvent) error {
		return fn(ctx, ev.Payload.(*NavigationEvent))
	})
}
