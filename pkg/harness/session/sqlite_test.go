package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/sashabaranov/go-openai"
)

func testStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{Model: "gpt-4o"}
	tools := []llm.Tool{{Type: "function", Function: llm.ToolFunction{Name: "read_file", Description: "r"}}}
	store, err := OpenSQLiteStore(filepath.Join(dir, "golum.db"), cfg, prompt.PromptConfig{CWD: dir}, tools)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// assertBalanced enforces the invariant RunAgentLoop depends on: every
// tool_calls entry on an assistant message has a matching tool result.
func assertBalanced(t *testing.T, sess Session) {
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

func TestSQLite_RoundTripWithToolCalls(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	sess, err := store.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := sess.ID()

	if _, err := sess.AppendUserMessage("read the readme"); err != nil {
		t.Fatal(err)
	}
	call := openai.ToolCall{
		ID:       "call_1",
		Type:     openai.ToolTypeFunction,
		Function: openai.FunctionCall{Name: "read_file", Arguments: `{"path":"README.md"}`},
	}
	if _, err := sess.AppendAssistantMessage("looking", []openai.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendToolResult("call_1", "file contents"); err != nil {
		t.Fatal(err)
	}
	assertBalanced(t, sess)

	// Reopen from disk — the tool_calls must survive the JSON round trip, or the
	// assistant message loses its calls and the next request 400s.
	reopened, err := store.Open(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	assertBalanced(t, reopened)

	msgs, err := reopened.BuildContext()
	if err != nil {
		t.Fatal(err)
	}
	var found *openai.ChatCompletionMessage
	for i := range msgs {
		if msgs[i].Role == openai.ChatMessageRoleAssistant {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no assistant message after reopen")
	}
	if len(found.ToolCalls) != 1 {
		t.Fatalf("expected 1 rehydrated tool call, got %d", len(found.ToolCalls))
	}
	if found.ToolCalls[0].ID != "call_1" || found.ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("tool call not rehydrated correctly: %+v", found.ToolCalls[0])
	}
	if found.ToolCalls[0].Function.Arguments != `{"path":"README.md"}` {
		t.Fatalf("arguments lost: %q", found.ToolCalls[0].Function.Arguments)
	}
}

func TestSQLite_LabelPersists(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	sess, err := store.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.SetLabel("my session"); err != nil {
		t.Fatal(err)
	}

	// This is the JsonlSession regression: labels must survive a reopen.
	list, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Label != "my session" {
		t.Fatalf("label did not persist: %+v", list)
	}

	reopened, err := store.Open(ctx, sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Label() != "my session" {
		t.Fatalf("label after reopen = %q", reopened.Label())
	}
}

func TestSQLite_AlwaysAllowSurvivesReopen(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	sess, err := store.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendApprovalAlways("shell"); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(ctx, sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.AlwaysAllowed("shell") {
		t.Fatal("always-allow decision did not survive reopen")
	}
	if reopened.AlwaysAllowed("write_file") {
		t.Fatal("unrelated tool must not be auto-approved")
	}
}

func TestSQLite_ListDeleteAndCascade(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	a, _ := store.Create(ctx)
	if _, err := a.AppendUserMessage("one"); err != nil {
		t.Fatal(err)
	}
	b, _ := store.Create(ctx)
	if _, err := b.AppendUserMessage("two"); err != nil {
		t.Fatal(err)
	}

	list, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(list))
	}
	for _, m := range list {
		if m.EntryCount != 1 {
			t.Fatalf("expected 1 entry for %s, got %d", m.ID, m.EntryCount)
		}
	}

	if err := store.Delete(ctx, a.ID()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE session_id = ?`, a.ID()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("entries not cascade-deleted: %d remain", n)
	}
	if err := store.Delete(ctx, "sess_does_not_exist"); err == nil {
		t.Fatal("expected error deleting unknown session")
	}
}

func TestSQLite_ForkCopiesUpToEntry(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	src, _ := store.Create(ctx)
	e1, _ := src.AppendUserMessage("first")
	_, _ = src.AppendAssistantMessage("reply one", nil)
	_, _ = src.AppendUserMessage("second")

	forked, err := store.Fork(ctx, src.ID(), e1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(forked.Entries()); got != 1 {
		t.Fatalf("expected fork to stop after the cut entry, got %d entries", got)
	}

	// The original must be untouched by the fork.
	reopened, err := store.Open(ctx, src.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reopened.Entries()); got != 3 {
		t.Fatalf("source session mutated by fork: %d entries", got)
	}
}

func TestSQLite_EntryIDsAreSortable(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	sess, _ := store.Create(ctx)

	var ids []string
	for i := 0; i < 20; i++ {
		e, err := sess.AppendUserMessage("m")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i-1] >= ids[i] {
			t.Fatalf("entry ids not monotonically sortable: %q >= %q", ids[i-1], ids[i])
		}
	}
}

func TestMigrateJSONL_ImportsLegacySessions(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	dir := t.TempDir()

	call := openai.ToolCall{
		ID:       "call_x",
		Type:     openai.ToolTypeFunction,
		Function: openai.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`},
	}
	entries := []Entry{
		{ID: "e_1", Seq: 1, Kind: EntryUserMessage, Role: "user", Content: "hi"},
		{ID: "e_2", Seq: 2, Kind: EntryAssistantMessage, Role: "assistant", Content: "working",
			Meta: map[string]any{"tool_calls": []openai.ToolCall{call}}},
		{ID: "e_3", Seq: 3, Kind: EntryToolResult, Role: "tool", Content: "contents",
			Meta: map[string]any{"tool_call_id": "call_x"}},
		{ID: "e_4", Seq: 4, Kind: EntryLabel, Content: "legacy label"},
	}
	path := filepath.Join(dir, "sess_legacy.jsonl")
	var sb strings.Builder
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := MigrateJSONL(ctx, store, dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 imported session, got %d", n)
	}

	sess, err := store.Open(ctx, "sess_legacy")
	if err != nil {
		t.Fatal(err)
	}
	assertBalanced(t, sess)
	if sess.Label() != "legacy label" {
		t.Fatalf("label not imported: %q", sess.Label())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expected legacy file to be renamed after import")
	}

	// Idempotent: a second run must not duplicate.
	n2, err := MigrateJSONL(ctx, store, dir)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("migration not idempotent, imported %d on second run", n2)
	}
}

func TestMigrateJSONL_MissingDirIsNoop(t *testing.T) {
	store := testStore(t)
	n, err := MigrateJSONL(context.Background(), store, filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0, got %d", n)
	}
}
