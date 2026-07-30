package harness

import (
	"context"
	"fmt"
	"sync"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/hooks"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/memory"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
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
	compactor     *Compactor
	memory        *memory.Store
	episodic      *EpisodicTracker
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
	Memory    *memory.Store
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
		compactor: NewCompactor(hc.Client),
		memory:    hc.Memory,
		episodic:  NewEpisodicTracker(),
		cfg:       loop,
		promptCfg: hc.PromptCfg,
		phase:     PhaseIdle,
	}, nil
}

// Session returns the active session.
func (h *AgentHarness) Session() session.Session { return h.session }

// SetSession swaps in a different session (resume or fork). Refused while a
// turn is in flight, since the loop holds a reference to the current one.
func (h *AgentHarness) SetSession(s session.Session) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.phase != PhaseIdle {
		return fmt.Errorf("harness busy (%s)", h.phase)
	}
	h.session = s
	return nil
}

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

		_ = RunAgentLoop(turnCtx, LoopDeps{
			Client:    h.client,
			Session:   h.session,
			Registry:  h.registry,
			Env:       h.env,
			Approvals: approvals,
			Todos:     h.todos,
			Compactor: h.compactor,
			Memory:    h.memory,
			Episodic:  h.episodic,
		}, h.cfg, emit)
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

// Compact runs tiered compaction now, regardless of threshold or cooldown.
// This is the manual path; the loop compacts automatically via the Compactor.
func (h *AgentHarness) Compact(ctx context.Context, emit func(AgentEvent)) (CompactionResult, error) {
	h.mu.Lock()
	if h.phase != PhaseIdle {
		phase := h.phase
		h.mu.Unlock()
		return CompactionResult{}, fmt.Errorf("harness busy (%s)", phase)
	}
	h.phase = PhaseCompacting
	h.mu.Unlock()
	defer h.setPhase(PhaseIdle)

	return h.compactor.Compact(ctx, h.session, emit)
}

// NeedsCompression reports whether the session context is near the limit,
// using the larger of reported usage and the local estimate.
func (h *AgentHarness) NeedsCompression() bool {
	cm := h.session.ContextManager()
	if cm == nil {
		return false
	}
	return cm.ShouldCompact()
}

// ContextUsageRatio reports how full the context window is (0..1).
func (h *AgentHarness) ContextUsageRatio() float64 {
	cm := h.session.ContextManager()
	if cm == nil {
		return 0
	}
	return cm.ContextUsageRatio()
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
