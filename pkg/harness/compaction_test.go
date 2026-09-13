package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/harnesstest"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
	"github.com/sashabaranov/go-openai"
)

// ---- RunAgentLoop end-to-end ------------------------------------------------

func loopTestEnv(t *testing.T, root string, cfg *config.Config) (session.Session, LoopDeps) {
	t.Helper()
	env, err := execenv.NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	reg, todos := tool.DefaultRegistry(nil)
	ctxMgr := contextmgr.NewContextManager(cfg, prompt.PromptConfig{CWD: root}, nil, reg.AsLLMTools())
	sess := session.NewInMemorySession("loop-test", ctxMgr)
	return sess, LoopDeps{
		Session:   sess,
		Registry:  reg,
		Env:       env,
		Approvals: AutoApprove{},
		Todos:     todos,
	}
}

func collect(events *[]AgentEvent) func(AgentEvent) {
	return func(ev AgentEvent) { *events = append(*events, ev) }
}

func typesOf(events []AgentEvent) []AgentEventType {
	out := make([]AgentEventType, 0, len(events))
	for _, e := range events {
		out = append(out, e.Type)
	}
	return out
}

func hasType(events []AgentEvent, want AgentEventType) bool {
	for _, e := range events {
		if e.Type == want {
			return true
		}
	}
	return false
}

// TestRunAgentLoop_ToolRoundTrip drives the real loop: the model asks for a
// tool, the loop executes it, feeds the result back, and the model answers.
func TestRunAgentLoop_ToolRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mock := harnesstest.NewScriptedModel()
	defer mock.Close()
	mock.Script(
		harnesstest.Turn{Content: "reading", ToolCalls: []harnesstest.ToolCall{
			{ID: "call_1", Name: "read_file", Args: `{"path":"hello.txt"}`},
		}},
		harnesstest.Turn{Content: "The file says world."},
	)

	cfg := &config.Config{Model: "gpt-4o", ContextWindow: 128000}
	sess, deps := loopTestEnv(t, root, cfg)
	deps.Client = mock.Client()
	if _, err := sess.AppendUserMessage("what is in hello.txt?"); err != nil {
		t.Fatal(err)
	}

	var events []AgentEvent
	if err := RunAgentLoop(context.Background(), deps, DefaultLoopConfig(), collect(&events)); err != nil {
		t.Fatalf("loop error: %v", err)
	}

	if !hasType(events, EventToolCallStart) || !hasType(events, EventToolCallResult) {
		t.Fatalf("expected tool call events, got %v", typesOf(events))
	}
	if !hasType(events, EventTurnDone) {
		t.Fatalf("expected turn done, got %v", typesOf(events))
	}
	if mock.RequestCount() != 2 {
		t.Fatalf("expected 2 model invocations, got %d", mock.RequestCount())
	}
	balancedToolMessages(t, sess)

	// The tool result must have actually reached the context.
	msgs, _ := sess.BuildContext()
	var sawResult bool
	for _, m := range msgs {
		if m.Role == openai.ChatMessageRoleTool && strings.Contains(m.Content, "world") {
			sawResult = true
		}
	}
	if !sawResult {
		t.Fatal("tool result never reached the context")
	}
}

type bulkyTool struct{ payload string }

func (t bulkyTool) Name() string               { return "bulky" }
func (t bulkyTool) Description() string        { return "returns a large payload" }
func (t bulkyTool) Parameters() map[string]any { return map[string]any{} }
func (t bulkyTool) Execute(context.Context, map[string]any, execenv.ExecutionEnv) (tool.Result, error) {
	return tool.Result{Content: t.payload}, nil
}

// Oversized tool output is clipped before the next model request and the
// original size is journaled so evals can assert the bound without parsing
// the preview string.
func TestRunAgentLoop_TruncatesOversizedToolResult(t *testing.T) {
	root := t.TempDir()
	mock := harnesstest.NewScriptedModel()
	defer mock.Close()
	mock.Script(
		harnesstest.Turn{ToolCalls: []harnesstest.ToolCall{
			{ID: "call_1", Name: "bulky", Args: `{}`},
		}},
		harnesstest.Turn{Content: "done"},
	)

	cfg := &config.Config{Model: "gpt-4o", ContextWindow: 128000}
	sess, deps := loopTestEnv(t, root, cfg)
	deps.Client = mock.Client()
	payload := strings.Repeat("A", 10_000)
	deps.Registry.Register(bulkyTool{payload: payload})
	if _, err := sess.AppendUserMessage("call bulky"); err != nil {
		t.Fatal(err)
	}

	loop := DefaultLoopConfig()
	loop.MaxToolResultBytes = 200
	if err := RunAgentLoop(context.Background(), deps, loop, nil); err != nil {
		t.Fatalf("loop error: %v", err)
	}
	if mock.RequestCount() != 2 {
		t.Fatalf("expected 2 model invocations, got %d", mock.RequestCount())
	}

	var result session.Entry
	for _, e := range sess.Entries() {
		if e.Kind == session.EntryToolResult {
			result = e
			break
		}
	}
	if result.ID == "" {
		t.Fatal("no tool result was journaled")
	}
	if len(result.Content) > loop.MaxToolResultBytes {
		t.Fatalf("journaled %d bytes, limit %d", len(result.Content), loop.MaxToolResultBytes)
	}
	truncated, _ := result.Meta["truncated"].(bool)
	if !truncated {
		t.Fatalf("expected truncated=true in meta: %+v", result.Meta)
	}
	if bytes := metaInt(result.Meta["output_bytes"]); bytes < len(payload) {
		t.Fatalf("output_bytes=%d want >= %d", bytes, len(payload))
	}
	path, _ := result.Meta["artifact_path"].(string)
	if path == "" {
		t.Fatal("expected artifact_path in meta")
	}
	saved, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if string(saved) != payload {
		t.Fatalf("artifact size %d want %d", len(saved), len(payload))
	}
	if !strings.Contains(result.Content, path) {
		t.Fatalf("preview missing artifact path: %q", result.Content)
	}

	followUp := mock.Requests[1]
	toolContent := harnesstest.ToolRoleContent(followUp)
	if len(toolContent) > loop.MaxToolResultBytes {
		t.Fatalf("follow-up request still carries %d bytes", len(toolContent))
	}
	if strings.Count(toolContent, "A") == len(payload) {
		t.Fatal("follow-up request received the unbounded payload")
	}
	if !harnesstest.RequestHasAgentStatus(followUp) {
		t.Fatal("follow-up request missing request-scoped agent_status")
	}
	for _, e := range sess.Entries() {
		if strings.Contains(e.Content, "<agent_status>") {
			t.Fatal("agent_status was persisted into the durable session")
		}
	}
}

func TestRunAgentLoop_ForceToolCannotFinishWithoutCall(t *testing.T) {
	root := t.TempDir()
	mock := harnesstest.NewScriptedModel()
	defer mock.Close()
	mock.Script(
		harnesstest.Turn{Content: "hello there"},
		harnesstest.Turn{Content: "still just talking"},
	)
	cfg := &config.Config{Model: "gpt-4o", ContextWindow: 128000}
	sess, deps := loopTestEnv(t, root, cfg)
	deps.Client = mock.Client()
	_, _ = sess.AppendUserMessage("say hi")

	loop := DefaultLoopConfig()
	loop.ForceTool = "write_file"
	loop.ForceToolAttempts = 2
	if err := RunAgentLoop(context.Background(), deps, loop, nil); err != nil {
		t.Fatalf("loop error: %v", err)
	}
	if mock.RequestCount() != 2 {
		t.Fatalf("expected 2 attempts, got %d", mock.RequestCount())
	}
	records, _ := sess.FindRecords(session.RecordQuery{Lane: "main"})
	var finished session.Record
	for _, r := range records {
		if r.Type == session.RecordOperationFinished {
			finished = r
		}
	}
	if finished.Outcome != "failed" || finished.Error == nil || finished.Error.Code != "force_tool" {
		t.Fatalf("finish = %+v", finished)
	}
}

func TestRunAgentLoop_ForceToolSucceedsOnRetry(t *testing.T) {
	root := t.TempDir()
	mock := harnesstest.NewScriptedModel()
	defer mock.Close()
	mock.Script(
		harnesstest.Turn{Content: "hello"},
		harnesstest.Turn{ToolCalls: []harnesstest.ToolCall{
			{ID: "call_w", Name: "write_file", Args: `{"path":"note.txt","content":"EVAL_OK"}`},
		}},
		harnesstest.Turn{Content: "done"},
	)
	cfg := &config.Config{Model: "gpt-4o", ContextWindow: 128000}
	sess, deps := loopTestEnv(t, root, cfg)
	deps.Client = mock.Client()
	_, _ = sess.AppendUserMessage("say hi")

	loop := DefaultLoopConfig()
	loop.ForceTool = "write_file"
	if err := RunAgentLoop(context.Background(), deps, loop, nil); err != nil {
		t.Fatalf("loop error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "note.txt")); err != nil {
		t.Fatal("expected write_file to run")
	}
	records, _ := sess.FindRecords(session.RecordQuery{Lane: "main"})
	for _, r := range records {
		if r.Type == session.RecordOperationFinished && r.Outcome != "completed" {
			t.Fatalf("outcome=%s err=%v", r.Outcome, r.Error)
		}
	}
	if !harnesstest.RequestHasNamedToolChoice(mock.Requests[1], "write_file") {
		t.Fatalf("retry request should name write_file: %#v", mock.Requests[1]["tool_choice"])
	}
}

func metaInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

// TestRunAgentLoop_UnknownToolStaysBalanced verifies the loop's core invariant
// through the real code path rather than by calling dispatchToolCall directly.
func TestRunAgentLoop_UnknownToolStaysBalanced(t *testing.T) {
	root := t.TempDir()
	mock := harnesstest.NewScriptedModel()
	defer mock.Close()
	mock.Script(
		harnesstest.Turn{ToolCalls: []harnesstest.ToolCall{
			{ID: "call_bad", Name: "no_such_tool", Args: `{}`},
		}},
		harnesstest.Turn{Content: "recovered"},
	)

	cfg := &config.Config{Model: "gpt-4o", ContextWindow: 128000}
	sess, deps := loopTestEnv(t, root, cfg)
	deps.Client = mock.Client()
	_, _ = sess.AppendUserMessage("go")

	var events []AgentEvent
	if err := RunAgentLoop(context.Background(), deps, DefaultLoopConfig(), collect(&events)); err != nil {
		t.Fatalf("loop error: %v", err)
	}
	balancedToolMessages(t, sess)
}

// TestRunAgentLoop_ApprovalDeniedStaysBalanced covers the rejection path
// end-to-end, including the follow-up turn the model gets to recover in.
func TestRunAgentLoop_ApprovalDeniedStaysBalanced(t *testing.T) {
	root := t.TempDir()
	mock := harnesstest.NewScriptedModel()
	defer mock.Close()
	mock.Script(
		harnesstest.Turn{ToolCalls: []harnesstest.ToolCall{
			{ID: "call_w", Name: "write_file", Args: `{"path":"x.txt","content":"hi"}`},
		}},
		harnesstest.Turn{Content: "understood, not writing"},
	)

	cfg := &config.Config{Model: "gpt-4o", ContextWindow: 128000}
	sess, deps := loopTestEnv(t, root, cfg)
	deps.Client = mock.Client()
	deps.Approvals = denyBroker{}
	_, _ = sess.AppendUserMessage("write x.txt")

	var events []AgentEvent
	if err := RunAgentLoop(context.Background(), deps, DefaultLoopConfig(), collect(&events)); err != nil {
		t.Fatalf("loop error: %v", err)
	}
	if !hasType(events, EventToolCallAwaitingApproval) {
		t.Fatalf("expected approval event, got %v", typesOf(events))
	}
	balancedToolMessages(t, sess)

	if _, err := os.Stat(filepath.Join(root, "x.txt")); !os.IsNotExist(err) {
		t.Fatal("denied write must not touch the filesystem")
	}
}

// TestRunAgentLoop_ContextOverflowRecovers asserts the loop compacts and
// retries once when the provider rejects the request for length.
func TestRunAgentLoop_ContextOverflowRecovers(t *testing.T) {
	root := t.TempDir()
	mock := harnesstest.NewScriptedModel()
	defer mock.Close()
	mock.Script(
		harnesstest.Turn{Status: 400, ErrorBody: `{"error":{"message":"This model's maximum context length is 8192 tokens","code":"context_length_exceeded"}}`},
		harnesstest.Turn{NonStream: true, Content: "## ORIGINAL GOAL\nDo the thing.\n## COMPLETED ACTIONS\n- step one"},
		harnesstest.Turn{Content: "recovered after compaction"},
	)

	// A large window keeps auto-compaction from firing, so the only thing that
	// can trigger compaction here is the provider's overflow rejection.
	cfg := &config.Config{Model: "gpt-4o", ContextWindow: 128000}
	sess, deps := loopTestEnv(t, root, cfg)
	deps.Client = mock.Client()
	deps.Compactor = NewCompactor(deps.Client)

	// Enough turns that a valid cut point exists.
	for i := 0; i < 4; i++ {
		_, _ = sess.AppendUserMessage(strings.Repeat("context ", 40))
		_, _ = sess.AppendAssistantMessage(strings.Repeat("reply ", 40), nil)
	}
	_, _ = sess.AppendUserMessage("continue")

	var events []AgentEvent
	err := RunAgentLoop(context.Background(), deps, DefaultLoopConfig(), collect(&events))
	if err != nil {
		t.Fatalf("loop should recover from overflow, got %v", err)
	}
	if !hasType(events, EventCompactionDone) {
		t.Fatalf("expected compaction events, got %v", typesOf(events))
	}
	balancedToolMessages(t, sess)
}

// ---- cut points -------------------------------------------------------------

func TestFindValidEntryCutPoints_NeverSplitsToolPairs(t *testing.T) {
	call := openai.ToolCall{ID: "c1", Type: openai.ToolTypeFunction,
		Function: openai.FunctionCall{Name: "read_file", Arguments: `{}`}}

	entries := []session.Entry{
		{ID: "e1", Kind: session.EntryUserMessage},
		{ID: "e2", Kind: session.EntryAssistantMessage,
			Meta: map[string]any{"tool_calls": []openai.ToolCall{call}}},
		{ID: "e3", Kind: session.EntryToolResult,
			Meta: map[string]any{"tool_call_id": "c1"}},
		{ID: "e4", Kind: session.EntryAssistantMessage},
		{ID: "e5", Kind: session.EntryUserMessage},
	}

	cuts := session.FindValidEntryCutPoints(entries)
	if len(cuts) != 1 || cuts[0] != 4 {
		t.Fatalf("expected the only valid cut to be index 4 (start of the second user turn), got %v", cuts)
	}
	for _, c := range cuts {
		if entries[c].Kind != session.EntryUserMessage {
			t.Fatalf("cut %d is not at a user turn", c)
		}
	}
}

func TestFindValidEntryCutPoints_NoCutWhileToolCallOutstanding(t *testing.T) {
	call := openai.ToolCall{ID: "c1", Type: openai.ToolTypeFunction,
		Function: openai.FunctionCall{Name: "shell", Arguments: `{}`}}
	// A user message arriving while a tool call is still unresolved must not be
	// a cut point — cutting there would strand the assistant's tool_calls.
	entries := []session.Entry{
		{ID: "e1", Kind: session.EntryUserMessage},
		{ID: "e2", Kind: session.EntryAssistantMessage,
			Meta: map[string]any{"tool_calls": []openai.ToolCall{call}}},
		{ID: "e3", Kind: session.EntryUserMessage},
	}
	if cuts := session.FindValidEntryCutPoints(entries); len(cuts) != 0 {
		t.Fatalf("expected no valid cut while a tool call is outstanding, got %v", cuts)
	}
}

func TestFindValidCutPoints_MessageSpaceMirrorsEntrySpace(t *testing.T) {
	msgs := []contextmgr.MessageItem{
		{Role: openai.ChatMessageRoleUser},
		{Role: openai.ChatMessageRoleAssistant, ToolCalls: []openai.ToolCall{{ID: "c1"}}},
		{Role: openai.ChatMessageRoleTool, ToolCallID: "c1"},
		{Role: openai.ChatMessageRoleUser},
	}
	cuts := contextmgr.FindValidCutPoints(msgs)
	if len(cuts) != 1 || cuts[0] != 3 {
		t.Fatalf("expected cut at 3, got %v", cuts)
	}
}

// ---- derived context --------------------------------------------------------

func TestDeriveContextEntries_NoCompactionReturnsAll(t *testing.T) {
	entries := []session.Entry{
		{ID: "a", Kind: session.EntryUserMessage},
		{ID: "b", Kind: session.EntryAssistantMessage},
	}
	got := session.DeriveContextEntries(entries)
	if len(got) != 2 {
		t.Fatalf("expected all entries, got %d", len(got))
	}
}

func TestDeriveContextEntries_SummaryPlusTail(t *testing.T) {
	entries := []session.Entry{
		{ID: "a", Kind: session.EntryUserMessage},
		{ID: "b", Kind: session.EntryAssistantMessage},
		{ID: "c", Kind: session.EntryUserMessage},
		{ID: "d", Kind: session.EntryAssistantMessage},
		{ID: "z", Kind: session.EntryCompaction, Content: "summary",
			Meta: map[string]any{session.MetaCutEntryID: "b"}},
	}
	got := session.DeriveContextEntries(entries)
	if len(got) != 3 {
		t.Fatalf("expected summary + 2 surviving entries, got %d: %+v", len(got), got)
	}
	if got[0].Kind != session.EntryCompaction {
		t.Fatalf("summary must come first, got %s", got[0].Kind)
	}
	if got[1].ID != "c" || got[2].ID != "d" {
		t.Fatalf("expected tail c,d — got %s,%s", got[1].ID, got[2].ID)
	}
	// The full log is untouched: forking before the compaction still works.
	if len(entries) != 5 {
		t.Fatal("DeriveContextEntries must not mutate the log")
	}
}

func TestDeriveContextEntries_LastCompactionWins(t *testing.T) {
	entries := []session.Entry{
		{ID: "a", Kind: session.EntryUserMessage},
		{ID: "s1", Kind: session.EntryCompaction, Content: "old",
			Meta: map[string]any{session.MetaCutEntryID: "a"}},
		{ID: "b", Kind: session.EntryUserMessage},
		{ID: "s2", Kind: session.EntryCompaction, Content: "new",
			Meta: map[string]any{session.MetaCutEntryID: "b"}},
	}
	got := session.DeriveContextEntries(entries)
	if len(got) != 1 || got[0].Content != "new" {
		t.Fatalf("expected only the newest summary, got %+v", got)
	}
}

// ---- compaction -------------------------------------------------------------

func TestCompactor_KeepsRecentHistoryAndStaysBalanced(t *testing.T) {
	root := t.TempDir()
	mock := harnesstest.NewScriptedModel()
	defer mock.Close()
	mock.Script(harnesstest.Turn{NonStream: true,
		Content: "## ORIGINAL GOAL\nBuild it.\n## COMPLETED ACTIONS\n- did stuff"})

	cfg := &config.Config{Model: "gpt-4o", ContextWindow: 1000}
	sess, _ := loopTestEnv(t, root, cfg)

	call := openai.ToolCall{ID: "c1", Type: openai.ToolTypeFunction,
		Function: openai.FunctionCall{Name: "read_file", Arguments: `{"path":"a"}`}}
	for i := 0; i < 3; i++ {
		_, _ = sess.AppendUserMessage(strings.Repeat("older ", 50))
		_, _ = sess.AppendAssistantMessage("calling", []openai.ToolCall{call})
		_, _ = sess.AppendToolResult("c1", strings.Repeat("result ", 50))
	}
	_, _ = sess.AppendUserMessage("most recent request")

	c := NewCompactor(mock.Client())
	res, err := c.Compact(context.Background(), sess, nil)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if res.Tier != TierSummary && res.Tier != TierPrune {
		t.Fatalf("unexpected tier %v", res.Tier)
	}
	if !sess.ContextManager().HasBalancedToolCalls() {
		t.Fatal("compaction produced an unbalanced context")
	}

	if res.Tier == TierSummary {
		msgs, _ := sess.BuildContext()
		var joined strings.Builder
		for _, m := range msgs {
			joined.WriteString(m.Content)
		}
		all := joined.String()
		if !strings.Contains(all, "most recent request") {
			t.Fatal("compaction discarded the most recent turn")
		}
		if !strings.Contains(all, "ORIGINAL GOAL") {
			t.Fatal("summary missing from the rebuilt context")
		}
	}
}

func TestCompactor_CooldownSuppressesRepeat(t *testing.T) {
	root := t.TempDir()
	mock := harnesstest.NewScriptedModel()
	defer mock.Close()
	mock.Script(harnesstest.Turn{NonStream: true, Content: "summary"})

	cfg := &config.Config{Model: "gpt-4o", ContextWindow: 10}
	sess, _ := loopTestEnv(t, root, cfg)
	for i := 0; i < 4; i++ {
		_, _ = sess.AppendUserMessage(strings.Repeat("x ", 50))
		_, _ = sess.AppendAssistantMessage("y", nil)
	}

	c := NewCompactor(mock.Client())
	if !c.ShouldCompact(sess) {
		t.Fatal("expected the tiny window to trigger compaction")
	}
	if _, err := c.Compact(context.Background(), sess, nil); err != nil {
		t.Fatalf("compact: %v", err)
	}
	// Still over threshold, but the cooldown must suppress an immediate repeat.
	if c.ShouldCompact(sess) {
		t.Fatal("cooldown did not suppress a second compaction")
	}
	res, err := c.MaybeCompact(context.Background(), sess, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Tier != TierNone {
		t.Fatalf("expected no-op during cooldown, got tier %v", res.Tier)
	}
}

func TestCompactor_SurvivesReload(t *testing.T) {
	dir := t.TempDir()
	mock := harnesstest.NewScriptedModel()
	defer mock.Close()
	mock.Script(harnesstest.Turn{NonStream: true, Content: "## ORIGINAL GOAL\nthe goal"})

	cfg := &config.Config{Model: "gpt-4o", ContextWindow: 1000}
	store, err := session.OpenSQLiteStore(filepath.Join(dir, "g.db"), cfg,
		prompt.PromptConfig{CWD: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sess, err := store.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		_, _ = sess.AppendUserMessage(strings.Repeat("older ", 40))
		_, _ = sess.AppendAssistantMessage(strings.Repeat("reply ", 40), nil)
	}
	_, _ = sess.AppendUserMessage("latest")

	c := NewCompactor(mock.Client())
	if _, err := c.Compact(context.Background(), sess, nil); err != nil {
		t.Fatalf("compact: %v", err)
	}
	before, _ := sess.BuildContext()

	// The whole point of recording a cut point rather than deleting entries:
	// a reload must reproduce the identical context.
	reopened, err := store.Open(context.Background(), sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	after, _ := reopened.BuildContext()
	if len(before) != len(after) {
		t.Fatalf("context changed across reload: %d messages before, %d after", len(before), len(after))
	}
	for i := range before {
		if before[i].Role != after[i].Role || before[i].Content != after[i].Content {
			t.Fatalf("message %d differs across reload:\n before=%q\n after=%q",
				i, before[i].Content, after[i].Content)
		}
	}
}

func TestIsContextOverflowError(t *testing.T) {
	cases := map[string]bool{
		"This model's maximum context length is 8192 tokens": true,
		"context_length_exceeded":                            true,
		"Please reduce the length of the messages":           true,
		"rate limit exceeded":                                false,
		"connection refused":                                 false,
	}
	for msg, want := range cases {
		if got := llm.IsContextOverflowError(errString(msg)); got != want {
			t.Errorf("IsContextOverflowError(%q) = %v, want %v", msg, got, want)
		}
	}
	if llm.IsContextOverflowError(nil) {
		t.Error("nil must not be an overflow error")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
