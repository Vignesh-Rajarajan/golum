package harness

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/hooks"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/memory"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/Vignesh-Rajarajan/golum/pkg/skill"
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
	events        *HarnessEventBus
	skills        []skill.Skill

	mu           sync.Mutex
	abortMu      sync.Mutex
	phase        Phase
	streamCancel context.CancelFunc
	streamDone   chan struct{}
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
	Skills    []skill.Skill
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
	if hc.Skills == nil {
		hc.Skills, _ = skill.LoadSkills(context.Background(), hc.Env)
	}
	if hc.PromptCfg.SkillsSection == "" {
		hc.PromptCfg.SkillsSection = skill.FormatSkillsSection(hc.Skills)
	}
	if hc.Session == nil {
		tools := hc.Registry.AsLLMTools()
		ctxMgr := contextmgr.NewContextManager(hc.Config, hc.PromptCfg, nil, tools)
		hc.Session = session.NewInMemorySession("", ctxMgr)
	}
	if hc.Approvals == nil {
		hc.Approvals = AutoApprove{}
	}
	tool.LoadMCPInto(context.Background(), hc.Registry, filepath.Join(hc.Env.CWD(), ".golum/mcp.json"))
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
		events:    NewHarnessEventBus(),
		skills:    append([]skill.Skill(nil), hc.Skills...),
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

// Skills returns the project skills loaded when the harness was constructed.
func (h *AgentHarness) Skills() []skill.Skill {
	return append([]skill.Skill(nil), h.skills...)
}

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
	if text == "" {
		return nil, &InvalidMessageError{Lane: "main", Reason: "empty prompt"}
	}
	h.mu.Lock()
	if h.phase != PhaseIdle {
		h.mu.Unlock()
		return nil, &BusyError{Lane: "main", OperationKind: "run"}
	}
	h.phase = PhaseStreaming
	turnCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	h.streamCancel = cancel
	h.streamDone = done
	approvals := h.approvals
	hm := h.hooks
	h.mu.Unlock()

	open, err := h.session.FindOpenOperations("main", 2)
	if err != nil {
		cancel()
		h.finishStream(done)
		return nil, err
	}
	if len(open) > 0 {
		cancel()
		h.finishStream(done)
		return nil, &BusyError{Lane: "main", OperationID: open[0].RunID, OperationKind: open[0].Intent.Kind}
	}
	if hm != nil {
		runEvent := &hooks.RunEvent{Prompt: text}
		if err := hm.Emit(ctx, hooks.BeforeRun, runEvent); err != nil {
			cancel()
			h.finishStream(done)
			return nil, err
		}
		text = runEvent.Prompt
	}
	p := session.ProvisionedEntry{
		ID: session.NewEntryID(), Kind: session.EntryUserMessage,
		Role: "user", Content: text,
	}
	initial := []session.ProvisionedEntry{}
	if records, findErr := h.session.FindRecords(session.RecordQuery{Lane: "main"}); findErr == nil {
		if reduced, reduceErr := ReduceLaneState(ReductionInput{
			Lane: "main", LeafID: h.session.Leaf(), Entries: h.session.Entries(), Records: records,
		}); reduceErr == nil {
			initial = append(initial, reduced.State.PendingNextRun...)
		}
	}
	initial = append(initial, p)
	runID := session.NewRecordID()
	if _, err := h.session.AppendRecord(session.Record{
		ID: stableID("r_start_", runID), Lane: "main",
		Type: session.RecordOperationStarted, RunID: runID,
		SourceLeafID: h.session.Leaf(),
		Intent: &session.OperationIntent{
			Kind: "run", OriginalPrompt: []session.ProvisionedEntry{p},
			InitialMessages:   initial,
			ForceTool:         h.cfg.ForceTool,
			ForceToolAttempts: h.cfg.ForceToolAttempts,
		},
	}); err != nil {
		cancel()
		h.finishStream(done)
		return nil, err
	}
	h.events.Publish(HarnessEvent{Type: HarnessRunStart, RunID: runID})

	if hm != nil {
		// Keep the legacy notification for existing integrations while the
		// transform-capable BeforeRun hook is the preferred API.
		_ = hm.Emit(ctx, hooks.BeforeAgentStart, text)
	}

	ch := make(chan AgentEvent, 64)
	go func() {
		defer close(ch)
		defer h.finishStream(done)

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

		effective := h.EffectiveConfig()
		runErr := RunAgentLoop(turnCtx, LoopDeps{
			Client:      h.client,
			Session:     h.session,
			Registry:    h.registry,
			Env:         h.env,
			Approvals:   approvals,
			Todos:       h.todos,
			Compactor:   h.compactor,
			Hooks:       h.hooks,
			Model:       effective.Model,
			ActiveTools: effective.ActiveTools,
			Memory:      h.memory,
			Episodic:    h.episodic,
		}, h.cfg, emit)
		outcome := "completed"
		if runErr != nil {
			outcome = "failed"
			if turnCtx.Err() != nil {
				outcome = "aborted"
			}
		}
		h.events.Publish(HarnessEvent{Type: HarnessRunEnd, RunID: runID, Outcome: outcome})
		if hm != nil {
			_ = hm.Emit(turnCtx, hooks.BeforeRunEnd, &hooks.RunEndEvent{Err: runErr})
		}
	}()
	return ch, nil
}

// Resume continues the single suspended operation, if any.
func (h *AgentHarness) Resume(ctx context.Context) (ResumeOutcome, error) {
	h.mu.Lock()
	if h.phase != PhaseIdle {
		h.mu.Unlock()
		return ResumeOutcome{}, &BusyError{Lane: "main", OperationKind: "run"}
	}
	h.phase = PhaseStreaming
	resumeCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	h.streamCancel = cancel
	h.streamDone = done
	h.mu.Unlock()
	defer h.finishStream(done)
	defer cancel()
	open, err := h.session.FindOpenOperations("main", 2)
	if err != nil {
		return ResumeOutcome{}, err
	}
	if len(open) == 0 {
		return ResumeOutcome{}, &NothingToResumeError{Lane: "main"}
	}
	if len(open) > 1 {
		return ResumeOutcome{}, &CorruptionError{Reason: CorruptionMultipleOpenOperations, Detail: "main"}
	}
	if h.hooks != nil {
		if err := h.hooks.Emit(resumeCtx, hooks.BeforeResume, &hooks.RunEvent{}); err != nil {
			return ResumeOutcome{}, err
		}
	}
	h.events.Publish(HarnessEvent{Type: HarnessRunStart, RunID: open[0].RunID})
	effective := h.EffectiveConfig()
	err = RunAgentLoop(resumeCtx, LoopDeps{
		Client: h.client, Session: h.session, Registry: h.registry, Env: h.env,
		Approvals: h.approvals, Todos: h.todos, Compactor: h.compactor,
		Hooks: h.hooks, Model: effective.Model, ActiveTools: effective.ActiveTools,
		Memory: h.memory, Episodic: h.episodic,
	}, h.cfg, nil)
	out := ResumeOutcome{RunOutcome: h.currentOutcome()}
	if err != nil {
		out.Kind = "failed"
	}
	h.events.Publish(HarnessEvent{Type: HarnessRunEnd, RunID: open[0].RunID, Outcome: out.Kind})
	if h.hooks != nil {
		_ = h.hooks.Emit(resumeCtx, hooks.BeforeRunEnd, &hooks.RunEndEvent{Err: err})
	}
	return out, err
}

func (h *AgentHarness) Events() *HarnessEventBus { return h.events }

func (h *AgentHarness) currentOutcome() RunOutcome {
	entries := h.session.Entries()
	out := RunOutcome{Kind: "completed", LeafID: h.session.Leaf()}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == session.EntryAssistantMessage {
			out.FinalEntryID, out.FinalMessage = entries[i].ID, entries[i].Content
			break
		}
	}
	return out
}

// Abort cancels the in-flight turn.
func (h *AgentHarness) Abort() {
	_, _ = h.AbortContext(context.Background())
}

// AbortContext durably aborts the operation and returns drained transient queues.
func (h *AgentHarness) AbortContext(ctx context.Context) (AbortResult, error) {
	h.abortMu.Lock()
	defer h.abortMu.Unlock()
	h.mu.Lock()
	cancel := h.streamCancel
	done := h.streamDone
	h.mu.Unlock()
	open, err := h.session.FindOpenOperations("main", 2)
	if err != nil {
		return AbortResult{}, err
	}
	if len(open) == 0 {
		return AbortResult{}, &NoActiveOperationError{Lane: "main"}
	}
	if len(open) > 1 {
		return AbortResult{}, &CorruptionError{Reason: CorruptionMultipleOpenOperations, Detail: "main"}
	}
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return AbortResult{}, ctx.Err()
		}
	}
	// The operation may have completed naturally while cancellation was
	// propagating. In that case there is nothing left to finalize.
	open, err = h.session.FindOpenOperations("main", 2)
	if err != nil {
		return AbortResult{}, err
	}
	if len(open) == 0 {
		return AbortResult{}, nil
	}
	if len(open) > 1 {
		return AbortResult{}, &CorruptionError{Reason: CorruptionMultipleOpenOperations, Detail: "main"}
	}
	runID := open[0].RunID
	if _, err := h.session.AppendRecord(session.Record{
		Lane: "main", Type: session.RecordAbortRequested, RunID: runID,
	}); err != nil {
		return AbortResult{}, err
	}
	records, err := h.session.FindRecords(session.RecordQuery{Lane: "main"})
	if err != nil {
		return AbortResult{}, err
	}
	reduced, err := ReduceLaneState(ReductionInput{
		Lane: "main", LeafID: h.session.Leaf(), Entries: h.session.Entries(), Records: records,
	})
	if err != nil {
		return AbortResult{}, err
	}
	result := AbortResult{}
	if reduced.State.Operation != nil {
		result.Steer = reduced.State.Operation.PendingSteer
		result.FollowUp = reduced.State.Operation.PendingFollowUp
		for _, item := range append(append([]session.ProvisionedEntry{}, result.Steer...), result.FollowUp...) {
			queue := "steer"
			for _, follow := range result.FollowUp {
				if follow.ID == item.ID {
					queue = "followUp"
				}
			}
			if _, err := h.session.AppendRecord(session.Record{
				Lane: "main", Type: session.RecordQueueCancelled, RunID: runID,
				Queue: queue, EntryID: item.ID,
			}); err != nil {
				return result, err
			}
		}
	}
	if _, err := h.session.AppendRecord(session.Record{
		Lane: "main", Type: session.RecordOperationFinished, RunID: runID, Outcome: "aborted",
	}); err != nil {
		return result, err
	}
	return result, nil
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
	if h.hooks != nil {
		if err := h.hooks.Emit(ctx, hooks.BeforeCompaction, &hooks.CompactionEvent{}); err != nil {
			return CompactionResult{}, err
		}
	}
	runID, resultID := session.NewRecordID(), session.NewEntryID()
	if _, err := h.session.AppendRecord(session.Record{
		Lane: "main", Type: session.RecordOperationStarted, RunID: runID,
		SourceLeafID: h.session.Leaf(),
		Intent:       &session.OperationIntent{Kind: "compaction", ResultEntryID: resultID},
	}); err != nil {
		return CompactionResult{}, err
	}
	if _, err := h.session.AppendRecord(session.Record{
		Lane: "main", Type: session.RecordStepAttempt, RunID: runID,
		Step: "compaction", Attempt: 1, ResultEntryID: resultID, CompactionReason: "manual",
	}); err != nil {
		return CompactionResult{}, err
	}
	res, err := h.compactor.compact(ctx, h.session, emit, resultID)
	outcome := "completed"
	var opErr *session.OpError
	if err != nil {
		outcome = "failed"
		opErr = &session.OpError{Code: "compaction", Message: err.Error()}
	}
	_, finishErr := h.session.AppendRecord(session.Record{
		Lane: "main", Type: session.RecordOperationFinished, RunID: runID,
		Outcome: outcome, Error: opErr,
	})
	if err == nil {
		err = finishErr
	}
	return res, err
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

func (h *AgentHarness) finishStream(done chan struct{}) {
	h.mu.Lock()
	if h.streamDone == done {
		h.streamCancel = nil
		h.streamDone = nil
		h.phase = PhaseIdle
	}
	h.mu.Unlock()
	close(done)
}
