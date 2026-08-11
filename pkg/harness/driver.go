package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/hooks"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/Vignesh-Rajarajan/golum/pkg/sanitize"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
)

type Driver struct {
	deps LoopDeps
	cfg  LoopConfig
	emit func(AgentEvent)

	startedHere map[string]bool
}

func NewDriver(deps LoopDeps, cfg LoopConfig, emit func(AgentEvent)) *Driver {
	if emit == nil {
		emit = func(AgentEvent) {}
	}
	return &Driver{deps: deps, cfg: cfg, emit: emit, startedHere: map[string]bool{}}
}

func (d *Driver) PeekAction(ctx context.Context) (*Action, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records, err := d.deps.Session.FindRecords(session.RecordQuery{Lane: "main"})
	if err != nil {
		return nil, err
	}
	reduced, err := ReduceLaneState(ReductionInput{
		Lane: "main", LeafID: d.deps.Session.Leaf(),
		Entries: d.deps.Session.Entries(), Records: records,
	})
	if err != nil {
		return nil, err
	}
	op := reduced.State.Operation
	if op == nil {
		return nil, nil
	}
	if op.Aborting {
		r := session.Record{
			ID: stableID("r_finish_abort_", op.ID), Lane: "main",
			Type: session.RecordOperationFinished, RunID: op.ID, Outcome: "aborted",
		}
		return &Action{Kind: ActionFinishOperation, Record: &r}, nil
	}
	if len(op.PendingWrites) > 0 {
		p := op.PendingWrites[0]
		return &Action{Kind: ActionApplyPendingWrite, Entry: &p}, nil
	}
	if len(op.MissingInitialMessages) > 0 {
		p := op.MissingInitialMessages[0]
		return &Action{Kind: ActionAppendEntry, Entry: &p}, nil
	}
	if op.Step != nil {
		if op.Step.Step == "compaction" {
			return &Action{Kind: ActionCompact, Record: &op.Step.Record}, nil
		}
		if op.Step.Step != "assistant" {
			return nil, fmt.Errorf("unsupported in-flight step %q", op.Step.Step)
		}
		return &Action{Kind: ActionStreamAssistant, Record: &op.Step.Record}, nil
	}
	if op.ToolBatch != nil && op.ToolBatch.Unresolved {
		for _, call := range op.ToolBatch.Calls {
			if call.Result != nil {
				continue
			}
			tc := reducerToolCall(call)
			if call.Started == nil {
				policy := d.replayPolicy(tc.Name)
				r := session.Record{
					ID:   stableID("r_tool_", op.ID, op.ToolBatch.AssistantEntryID, fmt.Sprint(call.Index)),
					Lane: "main", Type: session.RecordToolStarted, RunID: op.ID,
					AssistantEntryID: op.ToolBatch.AssistantEntryID, ToolIndex: call.Index,
					ToolCallID: tc.ID, ToolName: tc.Name, EffectiveArgs: tc.Arguments,
					ResultEntryID: stableID("e_tool_", op.ID, op.ToolBatch.AssistantEntryID, fmt.Sprint(call.Index)),
					Replay:        policy,
				}
				return &Action{Kind: ActionAppendRecord, Record: &r}, nil
			}
			action := &Action{Kind: ActionExecuteTool, ToolCall: tc, ToolStarted: call.Started}
			if d.cfg.MaxToolCallsPerTurn > 0 {
				rank := 0
				for _, r := range records {
					if r.RunID == op.ID && r.Type == session.RecordToolStarted {
						rank++
						if r.ID == call.Started.ID {
							break
						}
					}
				}
				if rank > d.cfg.MaxToolCallsPerTurn {
					action.SyntheticResult = &tool.Result{
						Content: fmt.Sprintf("tool call limit exceeded (%d per turn)", d.cfg.MaxToolCallsPerTurn),
						IsError: true, Display: "tool limit",
					}
				}
			}
			return action, nil
		}
	}
	if len(op.PendingSteer) > 0 {
		p := op.PendingSteer[0]
		return &Action{Kind: ActionConsumeQueueItem, Entry: &p}, nil
	}
	if op.NewestOwn != nil && op.ToolBatch == nil && len(op.PendingFollowUp) > 0 {
		p := op.PendingFollowUp[0]
		return &Action{Kind: ActionCommitFollowUp, Entry: &p}, nil
	}
	newerUser := false
	if op.NewestOwn != nil {
		if leaf, ok := d.deps.Session.GetEntry(d.deps.Session.Leaf()); ok {
			newerUser = leaf.Kind == session.EntryUserMessage && int64(leaf.Seq) > op.NewestOwn.Seq
		}
	}
	if op.NewestOwn == nil || op.ToolBatch != nil || newerUser {
		if d.cfg.MaxConsecutiveErrors > 0 &&
			consecutiveToolErrors(records, d.deps.Session.Entries(), op.ID) >= d.cfg.MaxConsecutiveErrors {
			r := session.Record{
				ID: stableID("r_finish_errors_", op.ID), Lane: "main",
				Type: session.RecordOperationFinished, RunID: op.ID, Outcome: "failed",
				Error: &session.OpError{Code: "tool_errors", Message: "too many consecutive tool errors"},
			}
			return &Action{Kind: ActionFinishOperation, Record: &r}, nil
		}
		if notice := d.loopNotice(records, op.ID); notice != nil {
			return &Action{Kind: ActionAppendEntry, Entry: notice}, nil
		}
		if d.deps.Compactor != nil && d.deps.Compactor.ShouldCompact(d.deps.Session) {
			n := 1
			for _, r := range records {
				if r.RunID == op.ID && r.Type == session.RecordStepAttempt && r.Step == "compaction" {
					n++
				}
			}
			resultID := stableID("e_compact_", op.ID, fmt.Sprint(n))
			r := session.Record{
				ID: stableID("r_compact_", op.ID, fmt.Sprint(n)), Lane: "main",
				Type: session.RecordStepAttempt, RunID: op.ID, Step: "compaction",
				Attempt: n, ResultEntryID: resultID, CompactionReason: "threshold",
			}
			return &Action{Kind: ActionAppendRecord, Record: &r}, nil
		}
		n := 1
		for _, r := range records {
			if r.RunID == op.ID && r.Type == session.RecordStepAttempt && r.Step == "assistant" {
				n++
			}
		}
		if d.cfg.MaxModelInvocations > 0 && n > d.cfg.MaxModelInvocations {
			r := session.Record{
				ID: stableID("r_finish_models_", op.ID), Lane: "main",
				Type: session.RecordOperationFinished, RunID: op.ID, Outcome: "failed",
				Error: &session.OpError{Code: "model_limit",
					Message: fmt.Sprintf("model invocation limit exceeded (%d)", d.cfg.MaxModelInvocations)},
			}
			return &Action{Kind: ActionFinishOperation, Record: &r}, nil
		}
		resultID := stableID("e_step_", op.ID, fmt.Sprint(n))
		r := session.Record{
			ID: stableID("r_step_", op.ID, fmt.Sprint(n)), Lane: "main",
			Type: session.RecordStepAttempt, RunID: op.ID, Step: "assistant",
			Attempt: n, ResultEntryID: resultID,
		}
		return &Action{Kind: ActionAppendRecord, Record: &r}, nil
	}
	r := session.Record{
		ID: stableID("r_finish_", op.ID), Lane: "main",
		Type: session.RecordOperationFinished, RunID: op.ID, Outcome: "completed",
	}
	if op.TerminalFailure != nil {
		r.Outcome, r.Error = "failed", op.TerminalFailure
	}
	return &Action{Kind: ActionFinishOperation, Record: &r}, nil
}

func (d *Driver) ExecuteAction(ctx context.Context) (*Action, error) {
	action, err := d.PeekAction(ctx)
	if err != nil || action == nil {
		return action, err
	}
	switch action.Kind {
	case ActionAppendRecord:
		stored, err := d.deps.Session.AppendRecord(*action.Record)
		if err == nil && stored.Type == session.RecordToolStarted {
			d.startedHere[stored.ID] = true
		}
		return action, err
	case ActionAppendEntry, ActionApplyPendingWrite, ActionCommitFollowUp:
		_, err := d.deps.Session.AppendProvisioned(*action.Entry)
		return action, err
	case ActionConsumeQueueItem:
		_, err := d.deps.Session.AppendProvisioned(*action.Entry)
		if err == nil {
			d.emit(AgentEvent{
				Type: EventQueueConsumed,
				Meta: map[string]string{"entry_id": action.Entry.ID},
			})
		}
		return action, err
	case ActionStreamAssistant:
		return action, d.streamAssistant(ctx, *action.Record)
	case ActionCompact:
		_, err := d.deps.Compactor.compact(ctx, d.deps.Session, d.emit, action.Record.ResultEntryID)
		return action, err
	case ActionExecuteTool:
		return action, d.executeTool(ctx, action)
	case ActionFinishOperation:
		_, err := d.deps.Session.AppendRecord(*action.Record)
		if err == nil {
			if action.Record.Error != nil {
				d.emit(AgentEvent{Type: EventError, Err: fmt.Errorf("%s", action.Record.Error.Message)})
			} else {
				recordEpisode(ctx, d.deps)
			}
			d.emit(AgentEvent{Type: EventTurnDone})
		}
		return action, err
	default:
		return action, fmt.Errorf("unsupported action %q", action.Kind)
	}
}

func (d *Driver) RunToCompletion(ctx context.Context) error {
	for {
		action, err := d.ExecuteAction(ctx)
		if err != nil {
			d.emit(AgentEvent{Type: EventError, Err: err})
			d.emit(AgentEvent{Type: EventTurnDone, Cancelled: ctx.Err() != nil})
			return err
		}
		if action == nil {
			return nil
		}
	}
}

func (d *Driver) streamAssistant(ctx context.Context, attempt session.Record) error {
	messages, err := d.deps.Session.BuildContext()
	if err != nil {
		return err
	}
	if d.deps.Hooks != nil {
		if err := d.deps.Hooks.Emit(ctx, hooks.TransformContext, &hooks.TransformContextEvent{Messages: &messages}); err != nil {
			return d.recordHookFailure(attempt.RunID, err)
		}
	}
	opts := llm.ChatCompletionOptions{
		Model: d.deps.Model, Stream: true, Tools: d.activeLLMTools(),
		MaxRetries: 3, Timeout: d.cfg.StreamTimeout,
	}
	if d.deps.Hooks != nil {
		if err := d.deps.Hooks.Emit(ctx, hooks.BeforeRequest, &hooks.BeforeRequestEvent{
			Messages: &messages, Options: &opts,
		}); err != nil {
			return d.recordHookFailure(attempt.RunID, err)
		}
	}
	events := d.deps.Client.ChatCompletion(ctx, messages, opts)
	var content strings.Builder
	var calls []*llm.ToolCall
	var usage map[string]string
	stopReason := ""
	for ev := range events {
		switch ev.Type {
		case llm.EventTypeContentDelta:
			content.WriteString(ev.Content)
			d.emit(AgentEvent{Type: EventContentDelta, Content: ev.Content})
		case llm.EventTypeThinkingDelta:
			d.emit(AgentEvent{Type: EventThinkingDelta, Content: ev.Content})
		case llm.EventTypeToolCall:
			if ev.Tool != nil {
				calls = append(calls, ev.Tool)
			}
		case llm.EventTypeContentDone:
			usage, stopReason = ev.Meta, ev.FinishReason
			d.emit(AgentEvent{Type: EventContentDone, Meta: ev.Meta, Cancelled: ev.Cancelled})
			if ev.Cancelled {
				return context.Canceled
			}
		case llm.EventTypeError:
			if llm.IsContextOverflowError(ev.Error) && d.deps.Compactor != nil {
				records, _ := d.deps.Session.FindRecords(session.RecordQuery{Lane: "main", RunID: attempt.RunID})
				retried := false
				for _, r := range records {
					if r.Type == session.RecordUsage && r.Cause == "overflow" {
						retried = true
						break
					}
				}
				if !retried {
					if _, err := d.deps.Session.AppendRecord(session.Record{
						Lane: "main", Type: session.RecordUsage, RunID: attempt.RunID,
						Cause: "overflow",
					}); err != nil {
						return err
					}
					if _, err := d.deps.Compactor.Compact(ctx, d.deps.Session, d.emit); err == nil {
						return nil
					}
				}
			}
			meta := map[string]any{"stop_reason": "error", "error": ev.Error.Error()}
			_, _ = d.deps.Session.AppendProvisioned(session.ProvisionedEntry{
				ID: attempt.ResultEntryID, Kind: session.EntryAssistantMessage,
				Role: "assistant", Content: sanitize.StripPseudoToolMarkup(content.String()), Meta: meta,
			})
			return ev.Error
		}
	}
	meta := map[string]any{"stop_reason": stopReason}
	if d.deps.Model != "" {
		meta["model"] = d.deps.Model
	}
	if len(calls) > 0 {
		meta["tool_calls"] = toOpenAIToolCalls(calls)
	}
	_, err = d.deps.Session.AppendProvisioned(session.ProvisionedEntry{
		ID: attempt.ResultEntryID, Kind: session.EntryAssistantMessage, Role: "assistant",
		Content: sanitize.StripPseudoToolMarkup(content.String()), Meta: meta,
	})
	if err != nil {
		return err
	}
	if usage != nil {
		u := contextmgr.TokenUsageFromMeta(usage)
		if cm := d.deps.Session.ContextManager(); cm != nil {
			cm.SetLatestUsage(u)
			cm.AddUsage(u)
		}
		if _, err := d.deps.Session.AppendRecord(session.Record{
			Lane: "main", Type: session.RecordUsage, RunID: attempt.RunID, Usage: &u,
			Cause: "assistant",
		}); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) executeTool(ctx context.Context, action *Action) error {
	started := action.ToolStarted
	tc := action.ToolCall
	if action.SyntheticResult != nil {
		return d.persistToolResult(*started, tc, *action.SyntheticResult)
	}
	if started.Replay == session.ReplayNever && !d.startedHere[started.ID] {
		result := tool.Result{Content: "interrupted; not replayed", IsError: true, Display: "interrupted"}
		return d.persistToolResult(*started, tc, result)
	}
	var hookEvent *hooks.BeforeToolEvent
	if d.deps.Hooks != nil {
		hookEvent = &hooks.BeforeToolEvent{ToolName: tc.Name, Args: tc.Arguments}
		if err := d.deps.Hooks.Emit(ctx, hooks.BeforeTool, hookEvent); err != nil {
			return d.recordHookFailure(started.RunID, err)
		}
		tc.Arguments = hookEvent.Args
		if hookEvent.Skip {
			if hookEvent.Result == nil {
				hookEvent.Result = &tool.Result{Content: "tool skipped by hook", IsError: true}
			}
			if err := d.persistToolResult(*started, tc, *hookEvent.Result); err != nil {
				return err
			}
			d.emit(AgentEvent{Type: EventToolCallResult, ToolCall: tc, ToolResult: hookEvent.Result})
			return nil
		}
	}
	d.emit(AgentEvent{Type: EventToolCallStart, ToolCall: tc})
	content, result, todosChanged := dispatchToolCall(ctx, tc, d.deps, d.cfg, d.emit)
	result.Content = content
	if d.deps.Hooks != nil {
		if err := d.deps.Hooks.Emit(ctx, hooks.AfterTool, &hooks.AfterToolEvent{
			ToolName: tc.Name, Args: tc.Arguments, Result: &result,
		}); err != nil {
			return d.recordHookFailure(started.RunID, err)
		}
		content = result.Content
	}
	if err := d.persistToolResult(*started, tc, result); err != nil {
		return err
	}
	if d.deps.Episodic != nil {
		d.deps.Episodic.Observe(tc, result.IsError, content)
	}
	d.emit(AgentEvent{Type: EventToolCallResult, ToolCall: tc, ToolResult: &result})
	if todosChanged && d.deps.Todos != nil {
		d.emit(AgentEvent{Type: EventTodosChanged, Todos: d.deps.Todos.List()})
	}
	return nil
}

func (d *Driver) recordHookFailure(runID string, hookErr error) error {
	_, err := d.deps.Session.AppendRecord(session.Record{
		Lane: "main", Type: session.RecordUsage, RunID: runID, Cause: "hook",
		Error: &session.OpError{Code: "hook", Message: hookErr.Error()},
	})
	if err != nil {
		return err
	}
	return hookErr
}

func (d *Driver) persistToolResult(started session.Record, tc *llm.ToolCall, result tool.Result) error {
	_, err := d.deps.Session.AppendProvisioned(session.ProvisionedEntry{
		ID: started.ResultEntryID, Kind: session.EntryToolResult, Role: "tool",
		Content: result.Content, Meta: map[string]any{
			"tool_call_id": tc.ID, "is_error": result.IsError,
		},
	})
	return err
}

func (d *Driver) replayPolicy(name string) session.ReplayPolicy {
	if t, ok := d.deps.Registry.Get(name); ok {
		if aware, ok := t.(tool.ReplayAware); ok {
			if aware.Replay() == tool.ReplayNever {
				return session.ReplayNever
			}
			return session.ReplaySafe
		}
	}
	if tool.RequiresApproval(name) {
		return session.ReplayNever
	}
	return session.ReplaySafe
}

func reducerToolCall(call ToolCallState) *llm.ToolCall {
	tc := &llm.ToolCall{ID: call.Call.ID, Name: call.Call.Name, RawArguments: call.Call.Arguments}
	if strings.TrimSpace(call.Call.Arguments) != "" {
		tc.ArgsErr = json.Unmarshal([]byte(call.Call.Arguments), &tc.Arguments)
	}
	if tc.Arguments == nil {
		tc.Arguments = map[string]any{}
	}
	return tc
}

func stableID(prefix string, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return prefix + hex.EncodeToString(h.Sum(nil)[:16])
}

func (d *Driver) activeLLMTools() []llm.Tool {
	all := d.deps.Registry.AsLLMTools()
	if len(d.deps.ActiveTools) == 0 {
		return all
	}
	active := map[string]bool{}
	for _, name := range d.deps.ActiveTools {
		active[name] = true
	}
	out := all[:0]
	for _, candidate := range all {
		if active[candidate.Function.Name] {
			out = append(out, candidate)
		}
	}
	return out
}

func consecutiveToolErrors(records []session.Record, entries []session.Entry, runID string) int {
	byID := map[string]session.Entry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	count := 0
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		if r.RunID != runID || r.Type != session.RecordToolStarted {
			continue
		}
		e, ok := byID[r.ResultEntryID]
		if !ok {
			continue
		}
		isError, _ := e.Meta["is_error"].(bool)
		if !isError {
			break
		}
		count++
	}
	return count
}

func (d *Driver) loopNotice(records []session.Record, runID string) *session.ProvisionedEntry {
	window := d.cfg.LoopDetectionWindow
	if window <= 1 {
		return nil
	}
	var tools []session.Record
	for _, r := range records {
		if r.RunID == runID && r.Type == session.RecordToolStarted {
			tools = append(tools, r)
		}
	}
	if len(tools) < window {
		return nil
	}
	last := tools[len(tools)-1]
	lastArgs, _ := canonicalJSON(last.EffectiveArgs)
	repeated := 0
	for i := len(tools) - 1; i >= 0; i-- {
		args, _ := canonicalJSON(tools[i].EffectiveArgs)
		if tools[i].ToolName == last.ToolName && args == lastArgs {
			repeated++
		}
	}
	if repeated < window {
		return nil
	}
	id := stableID("e_loop_", runID, last.ToolName, lastArgs)
	if _, ok := d.deps.Session.GetEntry(id); ok {
		return nil
	}
	return &session.ProvisionedEntry{
		ID: id, Kind: session.EntrySystemNotice, Role: "system",
		Content: prompt.CreateLoopBreakerPrompt("repeated tool call " + last.ToolName),
	}
}
