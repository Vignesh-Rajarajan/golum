package harness

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/hooks"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
	"github.com/sashabaranov/go-openai"
)

// Phase is the harness lifecycle state.
type Phase string

const (
	PhaseIdle             Phase = "idle"
	PhaseStreaming        Phase = "streaming"
	PhaseAwaitingApproval Phase = "awaiting_approval"
	PhaseCompacting       Phase = "compacting"
)

// AgentHarness owns the agent loop, session, tools, and cancellation for the TUI.
type AgentHarness struct {
	model         string
	thinkingLevel string
	client        *llm.Client
	registry      *tool.Registry
	env           execenv.ExecutionEnv
	session       session.Session
	approvals     ApprovalBroker
	todos         *tool.TodoStore
	hooks         *hooks.HooksManager
	cfg           LoopConfig
	promptCfg     prompt.PromptConfig

	mu           sync.Mutex
	phase        Phase
	streamCancel context.CancelFunc
}

// HarnessConfig configures a new AgentHarness.
type HarnessConfig struct {
	Config    *config.Config
	Client    *llm.Client
	Env       execenv.ExecutionEnv
	Registry  *tool.Registry
	Todos     *tool.TodoStore
	Session   session.Session
	Approvals ApprovalBroker
	Hooks     *hooks.HooksManager
	Loop      LoopConfig
	PromptCfg prompt.PromptConfig
}

// NewAgentHarness constructs a harness. Session may be nil (creates in-memory).
func NewAgentHarness(hc HarnessConfig) (*AgentHarness, error) {
	if hc.Env == nil {
		return nil, fmt.Errorf("execution env required")
	}
	if hc.Registry == nil || hc.Todos == nil {
		reg, todos := tool.DefaultRegistry(hc.Todos)
		hc.Registry = reg
		hc.Todos = todos
	}
	if hc.Client == nil {
		if hc.Config == nil {
			return nil, fmt.Errorf("config or client required")
		}
		hc.Client = llm.NewClient(hc.Config)
	}
	if hc.Session == nil {
		tools := hc.Registry.AsLLMTools()
		ctxMgr := contextmgr.NewContextManager(hc.Config, hc.PromptCfg, nil, tools)
		hc.Session = session.NewInMemorySession("", ctxMgr)
	}
	if hc.Approvals == nil {
		hc.Approvals = AutoApprove{}
	}
	if hc.Hooks == nil {
		hc.Hooks = hooks.New()
	}
	loop := hc.Loop
	if loop.MaxModelInvocations == 0 {
		loop = DefaultLoopConfig()
	}
	if hc.Config != nil {
		loop.StreamTimeout = hc.Config.StreamTimeoutOrDefault()
	}
	model := ""
	if hc.Config != nil {
		model = hc.Config.Model
	}
	return &AgentHarness{
		model:     model,
		client:    hc.Client,
		registry:  hc.Registry,
		env:       hc.Env,
		session:   hc.Session,
		approvals: hc.Approvals,
		todos:     hc.Todos,
		hooks:     hc.Hooks,
		cfg:       loop,
		promptCfg: hc.PromptCfg,
		phase:     PhaseIdle,
	}, nil
}

// Session returns the active session.
func (h *AgentHarness) Session() session.Session { return h.session }

// Registry returns the tool registry.
func (h *AgentHarness) Registry() *tool.Registry { return h.registry }

// Todos returns the todo store.
func (h *AgentHarness) Todos() *tool.TodoStore { return h.todos }

// Env returns the execution environment.
func (h *AgentHarness) Env() execenv.ExecutionEnv { return h.env }

// Phase returns the current phase.
func (h *AgentHarness) Phase() Phase {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.phase
}

// Hooks returns the hooks manager.
func (h *AgentHarness) Hooks() *hooks.HooksManager { return h.hooks }

// SetApprovals replaces the approval broker (e.g. UI broker after construction).
func (h *AgentHarness) SetApprovals(a ApprovalBroker) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.approvals = a
}

// Prompt starts an agent turn for the user text. Returns an event channel.
func (h *AgentHarness) Prompt(ctx context.Context, text string) (<-chan AgentEvent, error) {
	h.mu.Lock()
	if h.phase != PhaseIdle {
		h.mu.Unlock()
		return nil, fmt.Errorf("harness busy (%s)", h.phase)
	}
	h.phase = PhaseStreaming
	turnCtx, cancel := context.WithCancel(ctx)
	h.streamCancel = cancel
	approvals := h.approvals
	hm := h.hooks
	h.mu.Unlock()

	if hm != nil {
		_ = hm.Emit(ctx, hooks.BeforeAgentStart, text)
	}

	if _, err := h.session.AppendUserMessage(text); err != nil {
		h.setPhase(PhaseIdle)
		cancel()
		return nil, err
	}

	ch := make(chan AgentEvent, 64)
	go func() {
		defer close(ch)
		defer h.setPhase(PhaseIdle)
		defer func() {
			h.mu.Lock()
			h.streamCancel = nil
			h.mu.Unlock()
		}()

		emit := func(ev AgentEvent) {
			if ev.Type == EventToolCallAwaitingApproval {
				h.setPhase(PhaseAwaitingApproval)
			} else if ev.Type == EventToolCallResult || ev.Type == EventToolCallStart {
				h.setPhase(PhaseStreaming)
			}
			select {
			case ch <- ev:
			case <-turnCtx.Done():
			}
		}

		_ = RunAgentLoop(turnCtx, h.client, h.session, h.registry, h.env, approvals, h.todos, h.cfg, emit)
	}()
	return ch, nil
}

// Abort cancels the in-flight turn.
func (h *AgentHarness) Abort() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.streamCancel != nil {
		h.streamCancel()
	}
}

// Compact compresses context using the compression prompt when needed or forced.
func (h *AgentHarness) Compact(ctx context.Context) error {
	h.mu.Lock()
	if h.phase != PhaseIdle {
		h.mu.Unlock()
		return fmt.Errorf("harness busy (%s)", h.phase)
	}
	h.phase = PhaseCompacting
	h.mu.Unlock()
	defer h.setPhase(PhaseIdle)

	cm := h.session.ContextManager()
	if cm == nil {
		return fmt.Errorf("no context manager")
	}
	cm.PruneToolOutputs()

	messages := cm.ChatCompletionMessages()
	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: prompt.GetCompressionPrompt(),
	})

	opts := llm.ChatCompletionOptions{
		Stream:     false,
		MaxRetries: 2,
		Timeout:    h.cfg.StreamTimeout,
	}
	events := h.client.ChatCompletion(ctx, messages, opts)
	var summary strings.Builder
	for ev := range events {
		if ev.Type == llm.EventTypeContentDelta {
			summary.WriteString(ev.Content)
		}
		if ev.Type == llm.EventTypeError {
			return ev.Error
		}
	}
	s := summary.String()
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("empty compression summary")
	}
	return h.session.AppendCompaction(s)
}

// NeedsCompression reports whether the session context is near the limit.
func (h *AgentHarness) NeedsCompression() bool {
	cm := h.session.ContextManager()
	if cm == nil {
		return false
	}
	return cm.NeedsCompression()
}

// NewSession replaces the in-memory session with a fresh one (keeps tools/env).
func (h *AgentHarness) NewSession(cfg *config.Config, promptCfg prompt.PromptConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	tools := h.registry.AsLLMTools()
	ctxMgr := contextmgr.NewContextManager(cfg, promptCfg, nil, tools)
	h.session = session.NewInMemorySession("", ctxMgr)
	h.todos = tool.NewTodoStore()
	h.registry.Register(tool.NewTodosTool(h.todos))
}

func (h *AgentHarness) setPhase(p Phase) {
	h.mu.Lock()
	h.phase = p
	h.mu.Unlock()
}
