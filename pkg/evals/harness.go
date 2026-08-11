package evals

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/hooks"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
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
	Output    string
	Events    []TranscriptEvent
	Entries   []session.Entry
	Usage     Usage
	Elapsed   time.Duration
	RunID     string
	Workspace string
	Harness   string
	Input     string
}

// Options configures an eval harness adapter.
type Options struct {
	Name                  string
	Model                 string // overrides GOLUM_EVAL_MODEL / OPENAI_MODEL
	ActiveTools           []string
	Skills                []skill.Skill
	TransformSystemPrompt func(defaultPrompt string) string
	Loop                  harness.LoopConfig // zero fields → eval defaults
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

	start := time.Now()
	runID := uuid.NewString()
	workspace := t.TempDir()

	env, err := execenv.NewOsExecutionEnv(workspace)
	if err != nil {
		return nil, err
	}
	if err := writeSkills(ctx, env, h.opts.Skills); err != nil {
		return nil, err
	}

	model, err := ResolveModel(h.opts.Model, os.Getenv)
	if err != nil {
		return nil, err
	}
	cfg := buildConfig(model)
	client := llm.NewClient(cfg)
	reg, todos := buildRegistry(h.opts.ActiveTools)
	loop := mergeEvalLoop(h.opts.Loop)

	ah, sess, err := h.buildAgent(ctx, cfg, client, env, reg, todos, loop, nil)
	if err != nil {
		return nil, err
	}

	result := &Result{
		RunID:     runID,
		Workspace: workspace,
		Harness:   h.Name,
		Usage:     Usage{Model: model},
	}
	var accumulated contextmgr.TokenUsage
	var inputs []string
	var output strings.Builder
	var events []TranscriptEvent
	toolCalls := 0

	for _, step := range steps {
		switch step.Kind {
		case "prompt":
			inputs = append(inputs, step.Content)
			out, evs, nTools, err := drainPrompt(ctx, ah, step.Content)
			if err != nil {
				result.Events = events
				result.Entries = sess.Entries()
				result.Elapsed = time.Since(start)
				return result, err
			}
			if output.Len() > 0 && out != "" {
				output.WriteString("\n")
			}
			output.WriteString(out)
			events = append(events, evs...)
			toolCalls += nTools
			if out != "" {
				events = append(events, TranscriptEvent{Kind: "message", Role: "assistant", Content: out})
			}
		case "reload":
			accumulated.Add(sess.ContextManager().TotalUsage())
			ah, sess, err = h.reload(ctx, cfg, client, env, reg, todos, loop, sess)
			if err != nil {
				result.Events = events
				result.Entries = sess.Entries()
				result.Elapsed = time.Since(start)
				return result, err
			}
		default:
			return nil, fmt.Errorf("evals: unknown step kind %q", step.Kind)
		}
	}

	usage := sess.ContextManager().TotalUsage()
	accumulated.Add(usage)
	result.Output = strings.TrimSpace(output.String())
	result.Events = events
	result.Entries = sess.Entries()
	result.Input = strings.Join(inputs, "\n---\n")
	result.Elapsed = time.Since(start)
	result.Usage = Usage{
		Model:        model,
		InputTokens:  accumulated.PromptTokens,
		OutputTokens: accumulated.CompletionTokens,
		TotalTokens:  accumulated.TotalTokens,
		ToolCalls:    toolCalls,
	}
	return result, nil
}

func (h *Harness) buildAgent(
	ctx context.Context,
	cfg *config.Config,
	client *llm.Client,
	env execenv.ExecutionEnv,
	reg *tool.Registry,
	todos *tool.TodoStore,
	loop harness.LoopConfig,
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
		Approvals: harness.AutoApprove{},
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
	return h.buildAgent(ctx, cfg, client, env, reg, todos, loop, newSess)
}

func drainPrompt(ctx context.Context, ah *harness.AgentHarness, content string) (string, []TranscriptEvent, int, error) {
	ch, err := ah.Prompt(ctx, content)
	if err != nil {
		return "", nil, 0, err
	}
	var out strings.Builder
	var events []TranscriptEvent
	toolCalls := 0
	var lastErr error
	var pendingName string
	for ev := range ch {
		switch ev.Type {
		case harness.EventContentDelta:
			out.WriteString(ev.Content)
		case harness.EventToolCallStart:
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
	if lastErr != nil {
		return strings.TrimSpace(out.String()), events, toolCalls, lastErr
	}
	return strings.TrimSpace(out.String()), events, toolCalls, nil
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
