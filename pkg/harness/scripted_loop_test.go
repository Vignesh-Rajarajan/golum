package harness

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/harnesstest"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm/capability"
)

func TestScriptedPlainFinalAnswer(t *testing.T) {
	r := NewTestRig(t)
	r.Model.Script(harnesstest.Turn{Content: "hello"})
	r.Prompt("hi")
	if err := r.RunToCompletion(context.Background()); err != nil {
		t.Fatal(err)
	}
	outcome, _ := r.Outcome()
	if outcome != "completed" {
		t.Fatalf("outcome=%s", outcome)
	}
	if r.Model.RequestCount() != 1 {
		t.Fatalf("requests=%d", r.Model.RequestCount())
	}
}

func TestScriptedMalformedAndUnknownToolsStayBalanced(t *testing.T) {
	r := NewTestRig(t)
	r.Model.Script(
		harnesstest.Turn{ToolCalls: []harnesstest.ToolCall{
			{ID: "bad", Name: "read_file", Args: `{"path":`},
			{ID: "miss", Name: "read_file", Args: `{}`},
			{ID: "unk", Name: "nope", Args: `{}`},
		}},
		harnesstest.Turn{Content: "recovered"},
	)
	r.Prompt("go")
	if err := r.RunToCompletion(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.AssertInvariants()
}

func TestScriptedEmptyAndOversizedToolResults(t *testing.T) {
	r := NewTestRig(t)
	r.Cfg.MaxToolResultBytes = 80
	r.rebuildDriver()
	r.Model.Script(
		harnesstest.Turn{ToolCalls: []harnesstest.ToolCall{
			{ID: "c1", Name: "list_dir", Args: `{"path":"."}`},
		}},
		harnesstest.Turn{Content: "ok"},
	)
	r.Prompt("list")
	if err := r.RunToCompletion(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.AssertInvariants()
}

func TestScriptedProviderErrors(t *testing.T) {
	t.Run("rate_limit_then_success", func(t *testing.T) {
		r := NewTestRig(t)
		r.Model.Script(
			harnesstest.Turn{Status: 429, ErrorBody: `{"error":{"message":"rate limit exceeded"}}`},
			harnesstest.Turn{Content: "ok"},
		)
		r.Prompt("hi")
		if err := r.RunToCompletion(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("non_retryable_400", func(t *testing.T) {
		r := NewTestRig(t)
		r.Model.Script(
			harnesstest.Turn{Status: 400, ErrorBody: `{"error":{"message":"bad request"}}`},
			harnesstest.Turn{Content: "should not run"},
		)
		r.Prompt("hi")
		err := r.RunToCompletion(context.Background())
		if err == nil {
			t.Fatal("expected a non-retryable error")
		}
		if r.Model.RequestCount() != 1 {
			t.Fatalf("retried a non-retryable error: %d", r.Model.RequestCount())
		}
	})
	t.Run("malformed_usage", func(t *testing.T) {
		r := NewTestRig(t)
		r.Model.Script(harnesstest.Turn{Content: "ok", MalformedUsage: true})
		r.Prompt("hi")
		if err := r.RunToCompletion(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("drop_final", func(t *testing.T) {
		r := NewTestRig(t)
		r.Model.Script(harnesstest.Turn{Content: "partial", DropFinal: true})
		r.Prompt("hi")
		_ = r.RunToCompletion(context.Background())
	})
	t.Run("mid_stream_error", func(t *testing.T) {
		r := NewTestRig(t)
		r.Model.Script(harnesstest.Turn{Content: "hi", MidStreamError: "boom"})
		r.Prompt("hi")
		_ = r.RunToCompletion(context.Background())
	})
	t.Run("duplicate_tool_calls", func(t *testing.T) {
		r := NewTestRig(t)
		r.Model.Script(
			harnesstest.Turn{ToolCalls: []harnesstest.ToolCall{
				{ID: "same", Name: "list_dir", Args: `{"path":"."}`},
				{ID: "same", Name: "list_dir", Args: `{"path":"."}`},
			}},
			harnesstest.Turn{Content: "ok"},
		)
		r.Prompt("go")
		_ = r.RunToCompletion(context.Background())
		r.AssertInvariants()
	})
}

func TestScriptedGuardrails(t *testing.T) {
	t.Run("model_limit", func(t *testing.T) {
		r := NewTestRig(t, WithLoopConfig(func() LoopConfig {
			c := DefaultLoopConfig()
			c.MaxModelInvocations = 1
			c.ForceTool = "write_file"
			c.ForceToolAttempts = 3
			return c
		}()))
		r.Model.Script(harnesstest.Turn{Content: "chat"}, harnesstest.Turn{Content: "chat"})
		r.Prompt("hi")
		_ = r.RunToCompletion(context.Background())
		outcome, opErr := r.Outcome()
		if outcome != "failed" || opErr == nil || opErr.Code != "model_limit" && opErr.Code != "force_tool" {
			t.Fatalf("outcome=%s err=%v", outcome, opErr)
		}
	})
	t.Run("wall_clock", func(t *testing.T) {
		r := NewTestRig(t, WithLoopConfig(func() LoopConfig {
			c := DefaultLoopConfig()
			c.MaxWallClock = 20 * time.Millisecond
			c.StreamTimeout = 5 * time.Millisecond
			return c
		}()))
		r.Model.Script(harnesstest.Turn{Content: "late", Delay: 50 * time.Millisecond})
		r.Prompt("hi")
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		_ = RunAgentLoop(ctx, r.Deps, r.Cfg, nil)
	})
	t.Run("cancel_no_leak", func(t *testing.T) {
		before := runtime.NumGoroutine()
		r := NewTestRig(t)
		r.Model.Script(harnesstest.Turn{Content: "slow", Delay: 200 * time.Millisecond})
		r.Prompt("hi")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- r.RunToCompletion(ctx) }()
		time.Sleep(20 * time.Millisecond)
		cancel()
		<-done
		time.Sleep(50 * time.Millisecond)
		after := runtime.NumGoroutine()
		if after > before+20 {
			t.Fatalf("goroutine leak: before=%d after=%d", before, after)
		}
	})
}

func TestScriptedTokenAccountingFromRecords(t *testing.T) {
	r := NewTestRig(t)
	r.Model.Script(harnesstest.Turn{
		Content: "ok",
		Usage:   map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
	})
	r.Prompt("hi")
	if err := r.RunToCompletion(context.Background()); err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, rec := range r.Records() {
		if rec.Type == session.RecordUsage && rec.Usage != nil && rec.Usage.TotalTokens == 18 {
			saw = true
		}
	}
	if !saw {
		t.Fatal("usage record missing or tokens not journaled")
	}
}

func TestForceToolDistinguishesCalledSucceededAndComplete(t *testing.T) {
	t.Run("called_but_fails", func(t *testing.T) {
		r := NewTestRig(t, WithLoopConfig(func() LoopConfig {
			c := DefaultLoopConfig()
			c.ForceTool = "write_file"
			c.ForceToolAttempts = 1
			return c
		}()))
		r.Model.Script(harnesstest.Turn{ToolCalls: []harnesstest.ToolCall{{
			ID: "c1", Name: "write_file", Args: `{"path":""}`,
		}}})
		r.Prompt("hi")
		_ = r.RunToCompletion(context.Background())
		called, succeeded := false, false
		for _, e := range r.Session.Entries() {
			if e.Kind == session.EntryToolResult {
				called = true
				if isErr, _ := e.Meta["is_error"].(bool); !isErr {
					succeeded = true
				}
			}
		}
		if !called {
			t.Fatal("tool was not called")
		}
		if succeeded {
			t.Fatal("invalid write should not count as success")
		}
		outcome, _ := r.Outcome()
		if outcome == "completed" {
			t.Fatal("run must not complete after a failed required tool")
		}
	})
	t.Run("native_force_unknown", func(t *testing.T) {
		r := NewTestRig(t, WithLoopConfig(func() LoopConfig {
			c := DefaultLoopConfig()
			c.ForceTool = "write_file"
			c.ForceToolAttempts = 2
			return c
		}()))
		r.Deps.Caps = &capability.Registry{}
		r.rebuildDriver()
		r.Model.Script(
			harnesstest.Turn{Content: "chat"},
			harnesstest.Turn{Content: "still chatting"},
		)
		r.Prompt("hi")
		_ = r.RunToCompletion(context.Background())
		if len(r.Model.Requests) > 1 && harnesstest.RequestHasNamedToolChoice(r.Model.Requests[1], "write_file") {
			t.Fatal("unknown capability must not native-force tool_choice")
		}
		if !strings.Contains(joinSystem(r.Model.Requests), "write_file") {
			t.Fatal("soft instruction fallback missing")
		}
	})
	t.Run("wrong_final_answer_still_ran_tool", func(t *testing.T) {
		r := NewTestRig(t, WithLoopConfig(func() LoopConfig {
			c := DefaultLoopConfig()
			c.ForceTool = "write_file"
			return c
		}()))
		r.Model.Script(
			harnesstest.Turn{ToolCalls: []harnesstest.ToolCall{{
				ID: "c1", Name: "write_file", Args: `{"path":"note.txt","content":"EVAL_OK"}`,
			}}},
			harnesstest.Turn{Content: "WRONG"},
		)
		r.Prompt("hi")
		if err := r.RunToCompletion(context.Background()); err != nil {
			t.Fatal(err)
		}
		outcome, _ := r.Outcome()
		if outcome != "completed" {
			t.Fatalf("successful required tool must allow completion, got %s", outcome)
		}
	})
}

func joinSystem(reqs []map[string]any) string {
	var b strings.Builder
	for _, req := range reqs {
		for _, m := range harnesstest.MessagesOf(req) {
			if m["role"] == "system" {
				c, _ := m["content"].(string)
				b.WriteString(c)
			}
		}
	}
	return b.String()
}
