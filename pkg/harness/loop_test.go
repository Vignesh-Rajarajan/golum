package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
	"github.com/sashabaranov/go-openai"
)

type denyBroker struct{}

func (denyBroker) Request(context.Context, llm.ToolCall) (bool, error) { return false, nil }

func balancedToolMessages(t *testing.T, sess session.Session) {
	t.Helper()
	msgs, err := sess.BuildContext()
	if err != nil {
		t.Fatal(err)
	}
	pending := map[string]bool{}
	for _, m := range msgs {
		if m.Role == openai.ChatMessageRoleAssistant {
			for _, tc := range m.ToolCalls {
				pending[tc.ID] = true
			}
		}
		if m.Role == openai.ChatMessageRoleTool {
			if !pending[m.ToolCallID] {
				t.Fatalf("tool result for unknown id %q", m.ToolCallID)
			}
			delete(pending, m.ToolCallID)
		}
	}
	if len(pending) > 0 {
		t.Fatalf("unbalanced tool_calls without results: %v", pending)
	}
}

func testSession(t *testing.T, root string) (session.Session, *tool.Registry, execenv.ExecutionEnv, *tool.TodoStore) {
	t.Helper()
	env, err := execenv.NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	reg, todos := tool.DefaultRegistry(nil)
	cfg := &config.Config{Model: "gpt-4o"}
	ctxMgr := contextmgr.NewContextManager(cfg, prompt.PromptConfig{CWD: root}, nil, reg.AsLLMTools())
	sess := session.NewInMemorySession("test", ctxMgr)
	return sess, reg, env, todos
}

// testDeps assembles LoopDeps for the dispatch-level tests.
func testDeps(sess session.Session, reg *tool.Registry, env execenv.ExecutionEnv,
	approvals ApprovalBroker, todos *tool.TodoStore) LoopDeps {
	return LoopDeps{
		Session:   sess,
		Registry:  reg,
		Env:       env,
		Approvals: approvals,
		Todos:     todos,
	}
}

func TestMissingToolResult_ApprovalDenied(t *testing.T) {
	root := t.TempDir()
	sess, reg, env, todos := testSession(t, root)

	tc := &llm.ToolCall{
		ID:           "call_1",
		Name:         "write_file",
		RawArguments: `{"path":"x.txt","content":"hi"}`,
		Arguments:    map[string]interface{}{"path": "x.txt", "content": "hi"},
	}
	_, _ = sess.AppendAssistantMessage("", []openai.ToolCall{tc.ToOpenAI()})

	content, result, _ := dispatchToolCall(context.Background(), tc, testDeps(sess, reg, env, denyBroker{}, todos), DefaultLoopConfig(), func(AgentEvent) {})
	if !result.IsError || !strings.Contains(content, "rejected") {
		t.Fatalf("expected rejection, got %#v", result)
	}
	_, _ = sess.AppendToolResult(tc.ID, content)
	balancedToolMessages(t, sess)
}

func TestMissingToolResult_ArgsErr(t *testing.T) {
	root := t.TempDir()
	sess, reg, env, todos := testSession(t, root)

	tc := &llm.ToolCall{
		ID:           "call_bad",
		Name:         "read_file",
		RawArguments: `{"path":`,
		ArgsErr:      fmt.Errorf("unexpected end of JSON"),
	}
	_, _ = sess.AppendAssistantMessage("", []openai.ToolCall{tc.ToOpenAI()})
	content, result, _ := dispatchToolCall(context.Background(), tc, testDeps(sess, reg, env, AutoApprove{}, todos), DefaultLoopConfig(), func(AgentEvent) {})
	if !result.IsError || !strings.Contains(content, "Invalid tool arguments") {
		t.Fatalf("expected ArgsErr result, got %#v content=%q", result, content)
	}
	_, _ = sess.AppendToolResult(tc.ID, content)
	balancedToolMessages(t, sess)
}

func TestMissingToolResult_UnknownTool(t *testing.T) {
	root := t.TempDir()
	sess, reg, env, todos := testSession(t, root)

	tc := &llm.ToolCall{
		ID:           "call_unk",
		Name:         "no_such_tool",
		RawArguments: `{}`,
		Arguments:    map[string]interface{}{},
	}
	_, _ = sess.AppendAssistantMessage("", []openai.ToolCall{tc.ToOpenAI()})
	content, result, _ := dispatchToolCall(context.Background(), tc, testDeps(sess, reg, env, AutoApprove{}, todos), DefaultLoopConfig(), func(AgentEvent) {})
	if !result.IsError || !strings.Contains(content, "Unknown tool") {
		t.Fatalf("expected unknown tool, got %#v", result)
	}
	_, _ = sess.AppendToolResult(tc.ID, content)
	balancedToolMessages(t, sess)
}

func TestMissingToolResult_CancelRemaining(t *testing.T) {
	root := t.TempDir()
	sess, _, _, _ := testSession(t, root)

	calls := []*llm.ToolCall{
		{ID: "c1", Name: "read_file", RawArguments: `{"path":"a"}`, Arguments: map[string]interface{}{"path": "a"}},
		{ID: "c2", Name: "read_file", RawArguments: `{"path":"b"}`, Arguments: map[string]interface{}{"path": "b"}},
	}
	_, _ = sess.AppendAssistantMessage("", toOpenAIToolCalls(calls))
	for _, tc := range calls {
		_, _ = sess.AppendToolResult(tc.ID, "Tool call cancelled.")
	}
	balancedToolMessages(t, sess)
}

func TestReadFileTool_works(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, reg, env, _ := testSession(t, root)
	ttool, ok := reg.Get("read_file")
	if !ok {
		t.Fatal("missing read_file")
	}
	res, err := ttool.Execute(context.Background(), map[string]any{"path": "hello.txt"}, env)
	if err != nil || res.IsError {
		t.Fatalf("unexpected: %#v err=%v", res, err)
	}
	if !strings.Contains(res.Content, "line1") {
		t.Fatalf("content=%q", res.Content)
	}
}

func TestWriteFile_outsideRootRefused(t *testing.T) {
	root := t.TempDir()
	_, reg, env, _ := testSession(t, root)
	ttool, _ := reg.Get("write_file")
	res, err := ttool.Execute(context.Background(), map[string]any{
		"path":    "/tmp/golum-outside-test.txt",
		"content": "hi",
	}, env)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "outside the workspace") {
		t.Fatalf("expected outside workspace error, got %#v", res)
	}
}

func TestToolCallHash_stable(t *testing.T) {
	a := &llm.ToolCall{Name: "read_file", Arguments: map[string]interface{}{"path": "a.go", "offset": 1.0}}
	b := &llm.ToolCall{Name: "read_file", Arguments: map[string]interface{}{"offset": 1.0, "path": "a.go"}}
	if toolCallHash(a) != toolCallHash(b) {
		t.Fatal("hash should be key-order independent")
	}
}

func TestEditTool_ambiguous(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("aa aa aa"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, reg, env, _ := testSession(t, root)
	ttool, _ := reg.Get("edit")
	res, err := ttool.Execute(context.Background(), map[string]any{
		"path": "f.txt", "old_string": "aa", "new_string": "bb",
	}, env)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "ambiguous") {
		t.Fatalf("expected ambiguous, got %#v", res)
	}
}

func TestRunAgentLoop_cancelSynthesizesResults(t *testing.T) {
	root := t.TempDir()
	sess, _, _, _ := testSession(t, root)
	calls := []*llm.ToolCall{
		{ID: "x1", Name: "shell", RawArguments: `{"command":"true"}`, Arguments: map[string]interface{}{"command": "true"}},
	}
	_, _ = sess.AppendAssistantMessage("working", toOpenAIToolCalls(calls))
	for _, tc := range calls {
		_, _ = sess.AppendToolResult(tc.ID, "Tool call cancelled.")
	}
	balancedToolMessages(t, sess)
}

func TestDefaultRegistry_hasEightTools(t *testing.T) {
	reg, _ := tool.DefaultRegistry(nil)
	want := []string{"read_file", "write_file", "edit", "list_dir", "glob", "grep", "shell", "todos"}
	for _, name := range want {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("missing tool %s", name)
		}
	}
	if len(reg.Names()) != 8 {
		t.Fatalf("expected 8 tools, got %d: %v", len(reg.Names()), reg.Names())
	}
}

func TestRequiresApproval(t *testing.T) {
	if !tool.RequiresApproval("write_file") || !tool.RequiresApproval("shell") || !tool.RequiresApproval("edit") {
		t.Fatal("expected approval on mutating tools")
	}
	if tool.RequiresApproval("read_file") {
		t.Fatal("read_file should not require approval")
	}
}
