package evals

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/harnesstest"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/hooks"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/mcp"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/Vignesh-Rajarajan/golum/pkg/skill"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"
)

// Step is one action in a multi-step eval run.
type Step struct {
	Kind    string // "prompt" | "reload"
	Content string
}

// Prompt returns a prompt step.
func Prompt(content string) Step { return Step{Kind: "prompt", Content: content} }

// Reload is a skills-reload step (re-load .golum/skills and rebuild the harness session).
var Reload = Step{Kind: "reload"}

// TranscriptEvent is a simplified view of harness events for judges and assertions.
type TranscriptEvent struct {
	Kind       string // "message" | "tool_call" | "tool_result"
	Role       string
	Content    string
	ToolCallID string
	Name       string
	Arguments  map[string]any
	IsError    bool
}

// Usage summarizes token and tool usage for one run.
type Usage struct {
	Model                                  string
	InputTokens, OutputTokens, TotalTokens int
	ToolCalls                              int
}

// Result is the outcome of Harness.Run.
type Result struct {
	Output  string
	Events  []TranscriptEvent
	Entries []session.Entry
	// Records are the session's orchestration records (step attempts, tool
	// starts, usage, operation outcomes). They carry model-request counts,
	// stop reasons, and guardrail codes that the simplified Events cannot.
	Records []session.Record
	Usage   Usage
	Elapsed time.Duration
	// TimeToFirstToken is measured from the start of the first prompt step to
	// the first content, thinking, or tool-call event. Zero when nothing
	// streamed back.
	TimeToFirstToken time.Duration
	RunID            string
	Workspace        string
	// SnapshotDir holds per-tool-call workspace snapshots when
	// Options.SnapshotWorkspace is set; empty otherwise.
	SnapshotDir string
	Harness     string
	Input       string
	// Violations are approval denials and sandbox refusals observed during the
	// run.
	Violations []PolicyViolation
	// NativeToolCount is how many tool schemas were advertised to the model.
	NativeToolCount int
	DatasetVersion  string
	TaskHash        string
	Seed            string
	BuildID         string
	InitialManifest []string
	FinalManifest   []string

	trajOnce sync.Once
	traj     []TrajectoryStep
}

// Options configures an eval harness adapter.
type Options struct {
	Name                  string
	Model                 string // overrides GOLUM_EVAL_MODEL / OPENAI_MODEL
	ActiveTools           []string
	Skills                []skill.Skill
	TransformSystemPrompt func(defaultPrompt string) string
	Loop                  harness.LoopConfig // zero fields → eval defaults
	// Approvals gates mutating tool calls. Nil approves everything.
	Approvals ApprovalPolicy
	// SnapshotWorkspace copies the workspace tree after every tool result so
	// ReplayFrom can restore filesystem state at a trajectory cut. Off by
	// default: it is only needed for prefix replay.
	SnapshotWorkspace bool
	// MCPBackends are extra operations reachable only through invoke.
	MCPBackends []mcp.Backend
	// BaseURL and APIKey override the process environment when set.
	BaseURL string
	APIKey  string
	// Script, when set, starts a local scripted model for this run.
	Script []harnesstest.Turn
}

// Harness adapts golum's AgentHarness for behavioral evals.
type Harness struct {
	Name string
	opts Options
}

// New constructs an eval Harness. Name defaults to "default" when empty.
func New(opts Options) *Harness {
	if opts.Name == "" {
		opts.Name = "default"
	}
	return &Harness{Name: opts.Name, opts: opts}
}

// Run executes steps against a fresh isolated AgentHarness in t.TempDir().
func (h *Harness) Run(ctx context.Context, t *testing.T, steps ...Step) (*Result, error) {
	t.Helper()
	if len(steps) == 0 {
		return nil, fmt.Errorf("evals: no steps")
	}

	runID := uuid.NewString()
	workspace := t.TempDir()

	env, err := execenv.NewOsExecutionEnv(workspace)
	if err != nil {
		return nil, err
	}
	if err := writeSkills(ctx, env, h.opts.Skills); err != nil {
		return nil, err
	}

	return h.run(ctx, t, runID, workspace, env, nil, steps)
}

// run drives steps against an already-provisioned workspace. seed, when
// non-nil, is a pre-existing session whose entries are replayed before the
// first step (the prefix-replay path).
func (h *Harness) run(
	ctx context.Context,
	t *testing.T,
	runID, workspace string,
	env execenv.ExecutionEnv,
	seed []session.Entry,
	steps []Step,
) (*Result, error) {
	t.Helper()
	start := time.Now()

	model := h.opts.Model
	if model == "" && len(h.opts.Script) > 0 {
		model = "gpt-4o"
	}
	if model == "" {
		var err error
		model, err = ResolveModel(h.opts.Model, os.Getenv)
		if err != nil {
			return nil, err
		}
	}
	cfg := buildConfig(model)
	if h.opts.BaseURL != "" {
		cfg.BaseURL = h.opts.BaseURL
	}
	if h.opts.APIKey != "" {
		cfg.OpenAIAPIKey = h.opts.APIKey
	}
	var client *llm.Client
	if len(h.opts.Script) > 0 {
		mock := harnesstest.NewScriptedModel()
		defer mock.Close()
		mock.Script(h.opts.Script...)
		client = mock.Client()
		cfg.BaseURL = mock.BaseURL()
		cfg.OpenAIAPIKey = "test-key"
		cfg.Model = model
		if cfg.Model == "" {
			cfg.Model = "gpt-4o"
		}
	} else {
		client = llm.NewClient(cfg)
	}
	reg, todos := buildRegistry(h.opts.ActiveTools)
	if cat := tool.CatalogOf(reg); cat != nil {
		for _, b := range h.opts.MCPBackends {
			cat.Add(b)
		}
	}
	loop := mergeEvalLoop(h.opts.Loop)
	approvals := NewRecordingApprovals(h.opts.Approvals)

	snapshots := ""
	var err error
	if h.opts.SnapshotWorkspace {
		if snapshots, err = prepareSnapshotDir(runID); err != nil {
			return nil, err
		}
		if err := SnapshotWorkspaceAt(workspace, snapshots, initialSnapshot); err != nil {
			return nil, err
		}
	}

	ah, sess, err := h.buildAgent(ctx, cfg, client, env, reg, todos, loop, approvals, nil)
	if err != nil {
		return nil, err
	}
	if len(seed) > 0 {
		if sess, err = replaySeed(sess, seed); err != nil {
			return nil, err
		}
		if ah, sess, err = h.buildAgent(ctx, cfg, client, env, reg, todos, loop, approvals, sess); err != nil {
			return nil, err
		}
	}

	result := &Result{
		RunID:           runID,
		Workspace:       workspace,
		SnapshotDir:     snapshots,
		Harness:         h.Name,
		Usage:           Usage{Model: model},
		NativeToolCount: len(reg.AsLLMTools()),
	}
	var accumulated contextmgr.TokenUsage
	var records []session.Record
	var inputs []string
	var output strings.Builder
	var events []TranscriptEvent
	toolCalls := 0

	// finish populates the fields every exit path needs, so an error return
	// still yields a partial Result worth scoring and attributing.
	finish := func() {
		result.Events = events
		result.Entries = sess.Entries()
		result.Records = appendRecords(records, sess)
		result.Violations = approvals.Violations(result.Entries)
		result.Elapsed = time.Since(start)
	}

	for _, step := range steps {
		switch step.Kind {
		case "prompt":
			inputs = append(inputs, step.Content)
			out, err := drainPrompt(ctx, ah, step.Content, promptHooks{
				workspace:   workspace,
				snapshotDir: snapshots,
			})
			events = append(events, out.Events...)
			toolCalls += out.ToolCalls
			if result.TimeToFirstToken == 0 && !out.FirstTokenAt.IsZero() {
				result.TimeToFirstToken = out.FirstTokenAt.Sub(start)
			}
			if err != nil {
				finish()
				return result, err
			}
			if output.Len() > 0 && out.Output != "" {
				output.WriteString("\n")
			}
			output.WriteString(out.Output)
			if out.Output != "" {
				events = append(events, TranscriptEvent{Kind: "message", Role: "assistant", Content: out.Output})
			}
		case "reload":
			// A reload builds a fresh session, so records recorded against the
			// old one have to be harvested before it is dropped.
			accumulated.Add(sess.ContextManager().TotalUsage())
			records = appendRecords(records, sess)
			ah, sess, err = h.reload(ctx, cfg, client, env, reg, todos, loop, approvals, sess)
			if err != nil {
				finish()
				return result, err
			}
		default:
			return nil, fmt.Errorf("evals: unknown step kind %q", step.Kind)
		}
	}

	accumulated.Add(sess.ContextManager().TotalUsage())
	result.Output = strings.TrimSpace(output.String())
	result.Input = strings.Join(inputs, "\n---\n")
	finish()
	result.Usage = Usage{
		Model:        model,
		InputTokens:  accumulated.PromptTokens,
		OutputTokens: accumulated.CompletionTokens,
		TotalTokens:  accumulated.TotalTokens,
		ToolCalls:    toolCalls,
	}
	return result, nil
}

func appendRecords(dst []session.Record, sess session.Session) []session.Record {
	recs, err := sess.FindRecords(session.RecordQuery{Lane: "main"})
	if err != nil {
		return dst
	}
	return append(dst, recs...)
}

func (h *Harness) buildAgent(
	ctx context.Context,
	cfg *config.Config,
	client *llm.Client,
	env execenv.ExecutionEnv,
	reg *tool.Registry,
	todos *tool.TodoStore,
	loop harness.LoopConfig,
	approvals harness.ApprovalBroker,
	existing session.Session,
) (*harness.AgentHarness, session.Session, error) {
	skills, _ := skill.LoadSkills(ctx, env)
	promptCfg := prompt.PromptConfig{
		CWD:           env.CWD(),
		SkillsSection: skill.FormatSkillsSection(skills),
	}
	tools := reg.AsLLMTools()
	hm := hooks.New()
	if h.opts.TransformSystemPrompt != nil {
		defaultPrompt := prompt.GetSystemPrompt(promptCfg, nil, tools)
		transformed := h.opts.TransformSystemPrompt(defaultPrompt)
		hm.OnTransformContext(func(_ context.Context, msgs *[]openai.ChatCompletionMessage) error {
			if msgs == nil || len(*msgs) == 0 {
				return nil
			}
			m := *msgs
			if m[0].Role == openai.ChatMessageRoleSystem {
				m[0].Content = transformed
				*msgs = m
			}
			return nil
		})
	}

	var sess session.Session
	if existing != nil {
		sess = existing
	} else {
		ctxMgr := contextmgr.NewContextManager(cfg, promptCfg, nil, tools)
		sess = session.NewInMemorySession("", ctxMgr)
	}

	ah, err := harness.NewAgentHarness(harness.HarnessConfig{
		Config:    cfg,
		Client:    client,
		Env:       env,
		Registry:  reg,
		Todos:     todos,
		Session:   sess,
		Approvals: approvals,
		Hooks:     hm,
		Loop:      loop,
		PromptCfg: promptCfg,
		Skills:    skills,
	})
	if err != nil {
		return nil, nil, err
	}
	return ah, sess, nil
}

func (h *Harness) reload(
	ctx context.Context,
	cfg *config.Config,
	client *llm.Client,
	env execenv.ExecutionEnv,
	reg *tool.Registry,
	todos *tool.TodoStore,
	loop harness.LoopConfig,
	approvals harness.ApprovalBroker,
	old session.Session,
) (*harness.AgentHarness, session.Session, error) {
	skills, _ := skill.LoadSkills(ctx, env)
	promptCfg := prompt.PromptConfig{
		CWD:           env.CWD(),
		SkillsSection: skill.FormatSkillsSection(skills),
	}
	tools := reg.AsLLMTools()
	ctxMgr := contextmgr.NewContextManager(cfg, promptCfg, nil, tools)
	newSess := session.NewInMemorySession(old.ID(), ctxMgr)
	for _, e := range old.Entries() {
		newSess.LoadEntry(e)
	}
	if err := newSess.RebuildContext(); err != nil {
		return nil, nil, fmt.Errorf("evals reload: rebuild context: %w", err)
	}
	return h.buildAgent(ctx, cfg, client, env, reg, todos, loop, approvals, newSess)
}

// replaySeed loads a recorded entry prefix into sess and rebuilds the derived
// context from it, so a run can continue from a trajectory cut.
func replaySeed(sess session.Session, seed []session.Entry) (session.Session, error) {
	loader, ok := sess.(*session.InMemorySession)
	if !ok {
		return nil, fmt.Errorf("evals: session %T cannot load a replay prefix", sess)
	}
	for _, e := range seed {
		loader.LoadEntry(e)
	}
	if err := loader.RebuildContext(); err != nil {
		return nil, fmt.Errorf("evals replay: rebuild context: %w", err)
	}
	return loader, nil
}

// promptHooks carries the per-step side effects drainPrompt performs while
// streaming (currently workspace snapshotting for prefix replay).
type promptHooks struct {
	workspace   string
	snapshotDir string
}

// promptOutcome is one prompt step's contribution to a Result.
type promptOutcome struct {
	Output       string
	Events       []TranscriptEvent
	ToolCalls    int
	FirstTokenAt time.Time
}

func drainPrompt(ctx context.Context, ah *harness.AgentHarness, content string, hooks promptHooks) (promptOutcome, error) {
	var outcome promptOutcome
	ch, err := ah.Prompt(ctx, content)
	if err != nil {
		return outcome, err
	}
	var out strings.Builder
	var events []TranscriptEvent
	toolCalls := 0
	var lastErr error
	var pendingName string
	markFirstToken := func() {
		if outcome.FirstTokenAt.IsZero() {
			outcome.FirstTokenAt = time.Now()
		}
	}
	for ev := range ch {
		switch ev.Type {
		case harness.EventContentDelta:
			markFirstToken()
			out.WriteString(ev.Content)
		case harness.EventThinkingDelta:
			markFirstToken()
		case harness.EventToolCallStart:
			markFirstToken()
			toolCalls++
			name, id := "", ""
			var args map[string]any
			if ev.ToolCall != nil {
				name = ev.ToolCall.Name
				id = ev.ToolCall.ID
				args = ev.ToolCall.Arguments
			}
			pendingName = name
			events = append(events, TranscriptEvent{
				Kind:       "tool_call",
				Name:       name,
				ToolCallID: id,
				Arguments:  args,
			})
		case harness.EventToolCallResult:
			content := ""
			isErr := false
			id := ""
			if ev.ToolResult != nil {
				content = ev.ToolResult.Content
				isErr = ev.ToolResult.IsError
			}
			if ev.ToolCall != nil {
				id = ev.ToolCall.ID
				if ev.ToolCall.Name != "" {
					pendingName = ev.ToolCall.Name
				}
			}
			events = append(events, TranscriptEvent{
				Kind:       "tool_result",
				Name:       pendingName,
				ToolCallID: id,
				Content:    content,
				IsError:    isErr,
			})
			// Snapshot after the effect has landed, keyed by tool call id:
			// that is the only identifier available both here and on the
			// persisted tool-result entry ReplayFrom searches.
			if hooks.snapshotDir != "" && id != "" {
				if err := SnapshotWorkspaceAt(hooks.workspace, hooks.snapshotDir, id); err != nil {
					lastErr = err
				}
			}
		case harness.EventError:
			if ev.Err != nil {
				lastErr = ev.Err
			} else {
				lastErr = fmt.Errorf("agent error")
			}
		case harness.EventTurnDone:
			// channel will close after this
		}
	}
	outcome.Output = strings.TrimSpace(out.String())
	outcome.Events = events
	outcome.ToolCalls = toolCalls
	return outcome, lastErr
}

func buildConfig(model string) *config.Config {
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return &config.Config{
		OpenAIAPIKey:     os.Getenv("OPENAI_API_KEY"),
		OpenRouterAPIKey: os.Getenv("OPENROUTER_API_KEY"),
		BaseURL:          base,
		Model:            model,
	}
}

// buildRegistry interprets ActiveTools: nil → all default tools; empty → no tools;
// non-empty → only the named tools from DefaultRegistry.
func buildRegistry(active []string) (*tool.Registry, *tool.TodoStore) {
	if active == nil {
		return tool.DefaultRegistry(nil)
	}
	if len(active) == 0 {
		return tool.NewRegistry(), tool.NewTodoStore()
	}
	full, todos := tool.DefaultRegistry(nil)
	filtered := tool.NewRegistry()
	for _, name := range active {
		if t, ok := full.Get(name); ok {
			filtered.Register(t)
		}
	}
	return filtered, todos
}

func mergeEvalLoop(override harness.LoopConfig) harness.LoopConfig {
	base := harness.DefaultLoopConfig()
	base.MaxWallClock = 2 * time.Minute
	base.MaxModelInvocations = 10
	if override.MaxModelInvocations != 0 {
		base.MaxModelInvocations = override.MaxModelInvocations
	}
	if override.MaxToolCallsPerTurn != 0 {
		base.MaxToolCallsPerTurn = override.MaxToolCallsPerTurn
	}
	if override.MaxWallClock != 0 {
		base.MaxWallClock = override.MaxWallClock
	}
	if override.MaxConsecutiveErrors != 0 {
		base.MaxConsecutiveErrors = override.MaxConsecutiveErrors
	}
	if override.LoopDetectionWindow != 0 {
		base.LoopDetectionWindow = override.LoopDetectionWindow
	}
	if override.MaxToolResultBytes != 0 {
		base.MaxToolResultBytes = override.MaxToolResultBytes
	}
	if override.ToolExecTimeout != 0 {
		base.ToolExecTimeout = override.ToolExecTimeout
	}
	if override.StreamTimeout != 0 {
		base.StreamTimeout = override.StreamTimeout
	}
	if override.ForceTool != "" {
		base.ForceTool = override.ForceTool
	}
	if override.ForceToolAttempts != 0 {
		base.ForceToolAttempts = override.ForceToolAttempts
	}
	return base
}

func writeSkills(ctx context.Context, env execenv.ExecutionEnv, skills []skill.Skill) error {
	for _, sk := range skills {
		name := sk.Name
		if name == "" {
			return fmt.Errorf("evals: skill with empty name")
		}
		var b strings.Builder
		b.WriteString("---\n")
		fmt.Fprintf(&b, "name: %s\n", name)
		if sk.Description != "" {
			fmt.Fprintf(&b, "description: %s\n", sk.Description)
		}
		if sk.DisableModelInvocation {
			b.WriteString("disable-model-invocation: true\n")
		}
		b.WriteString("---\n\n")
		b.WriteString(sk.Body)
		if !strings.HasSuffix(sk.Body, "\n") {
			b.WriteByte('\n')
		}
		path := ".golum/skills/" + name + ".md"
		if err := env.WriteFile(ctx, path, b.String()); err != nil {
			return fmt.Errorf("evals: write skill %s: %w", name, err)
		}
	}
	return nil
}
