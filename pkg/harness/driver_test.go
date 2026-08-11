package harness

import (
	"context"
	"reflect"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/hooks"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
	"github.com/sashabaranov/go-openai"
)

type captureTool struct {
	name  string
	calls int
	args  map[string]any
}

func (t *captureTool) Name() string               { return t.name }
func (t *captureTool) Description() string        { return "capture" }
func (t *captureTool) Parameters() map[string]any { return map[string]any{} }
func (t *captureTool) Execute(_ context.Context, args map[string]any, _ execenv.ExecutionEnv) (tool.Result, error) {
	t.calls++
	t.args = args
	return tool.Result{Content: "executed"}, nil
}

func driverSession() *session.InMemorySession {
	cm := contextmgr.NewContextManager(&config.Config{Model: "gpt-4o"}, prompt.PromptConfig{}, nil, nil)
	return session.NewInMemorySession("test", cm)
}

func TestPeekActionIsDeterministicAndReadOnly(t *testing.T) {
	sess := driverSession()
	_, _ = sess.AppendUserMessage("hello")
	_, err := sess.AppendRecord(session.Record{
		Type: session.RecordOperationStarted, RunID: "run",
		Intent: &session.OperationIntent{Kind: "run"},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := NewDriver(LoopDeps{Session: sess, Registry: tool.NewRegistry()}, DefaultLoopConfig(), nil)
	first, err := d.PeekAction(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.PeekAction(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("peek changed action:\n%#v\n%#v", first, second)
	}
	records, _ := sess.FindRecords(session.RecordQuery{})
	if len(records) != 1 {
		t.Fatalf("peek wrote %d records", len(records)-1)
	}
}

func TestPeekActionResumesAtCrashBoundaries(t *testing.T) {
	sess := driverSession()
	_, _ = sess.AppendUserMessage("hello")
	_, _ = sess.AppendRecord(session.Record{
		Type: session.RecordOperationStarted, RunID: "run",
		Intent: &session.OperationIntent{Kind: "run"},
	})
	_, _ = sess.AppendRecord(session.Record{
		Type: session.RecordStepAttempt, RunID: "run", Step: "assistant",
		Attempt: 1, ResultEntryID: "assistant",
	})
	d := NewDriver(LoopDeps{Session: sess, Registry: tool.NewRegistry()}, DefaultLoopConfig(), nil)
	action, err := d.PeekAction(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if action.Kind != ActionStreamAssistant {
		t.Fatalf("after step_attempt got %q", action.Kind)
	}

	_, _ = sess.AppendProvisioned(session.ProvisionedEntry{
		ID: "assistant", Kind: session.EntryAssistantMessage, Role: "assistant",
		Meta: map[string]any{"stop_reason": "tool_calls", "tool_calls": []openai.ToolCall{{
			ID: "call", Type: "function", Function: openai.FunctionCall{Name: "shell", Arguments: `{}`},
		}}},
	})
	action, err = d.PeekAction(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if action.Kind != ActionAppendRecord || action.Record.Type != session.RecordToolStarted {
		t.Fatalf("after assistant got %#v", action)
	}
	if _, err := sess.AppendRecord(*action.Record); err != nil {
		t.Fatal(err)
	}
	restarted := NewDriver(LoopDeps{Session: sess, Registry: tool.NewRegistry()}, DefaultLoopConfig(), nil)
	action, err = restarted.ExecuteAction(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if action.Kind != ActionExecuteTool {
		t.Fatalf("after tool_started got %q", action.Kind)
	}
	result, ok := sess.GetEntry(action.ToolStarted.ResultEntryID)
	if !ok || result.Content != "interrupted; not replayed" {
		t.Fatalf("never-replay result=%#v ok=%v", result, ok)
	}
}

func TestBeforeToolHookCanRewriteAndSkip(t *testing.T) {
	for _, skip := range []bool{false, true} {
		t.Run(map[bool]string{false: "rewrite", true: "skip"}[skip], func(t *testing.T) {
			sess := driverSession()
			registry := tool.NewRegistry()
			captured := &captureTool{name: "capture"}
			registry.Register(captured)
			_, _ = sess.AppendRecord(session.Record{
				Type: session.RecordOperationStarted, RunID: "run",
				Intent: &session.OperationIntent{Kind: "run"},
			})
			_, _ = sess.AppendRecord(session.Record{
				Type: session.RecordStepAttempt, RunID: "run", Step: "assistant",
				Attempt: 1, ResultEntryID: "assistant",
			})
			_, _ = sess.AppendProvisioned(session.ProvisionedEntry{
				ID: "assistant", Kind: session.EntryAssistantMessage, Role: "assistant",
				Meta: map[string]any{"tool_calls": []openai.ToolCall{{
					ID: "call", Type: "function",
					Function: openai.FunctionCall{Name: "capture", Arguments: `{"value":"old"}`},
				}}},
			})
			hm := hooks.New()
			hm.OnBeforeTool(func(_ context.Context, ev *hooks.BeforeToolEvent) error {
				ev.Args["value"] = "new"
				ev.Skip = skip
				if skip {
					ev.Result = &tool.Result{Content: "hook result"}
				}
				return nil
			})
			d := NewDriver(LoopDeps{Session: sess, Registry: registry, Hooks: hm}, DefaultLoopConfig(), nil)
			if action, err := d.ExecuteAction(context.Background()); err != nil || action.Kind != ActionAppendRecord {
				t.Fatalf("append tool_started: action=%#v err=%v", action, err)
			}
			if action, err := d.ExecuteAction(context.Background()); err != nil || action.Kind != ActionExecuteTool {
				t.Fatalf("execute tool: action=%#v err=%v", action, err)
			}
			if skip {
				if captured.calls != 0 {
					t.Fatal("skipped tool executed")
				}
			} else if captured.calls != 1 || captured.args["value"] != "new" {
				t.Fatalf("rewrite not applied: calls=%d args=%v", captured.calls, captured.args)
			}
		})
	}
}
