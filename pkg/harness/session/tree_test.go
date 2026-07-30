package session

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/sashabaranov/go-openai"
)

func treeStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenSQLiteStore(filepath.Join(dir, "g.db"),
		&config.Config{Model: "gpt-4o"}, prompt.PromptConfig{CWD: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestGetPathToRoot(t *testing.T) {
	store := treeStore(t)
	sess, _ := store.Create(context.Background())

	e1, _ := sess.AppendUserMessage("one")
	e2, _ := sess.AppendAssistantMessage("two", nil)
	e3, _ := sess.AppendUserMessage("three")

	path, err := sess.GetPathToRoot(e3.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(path))
	}
	if path[0].ID != e1.ID || path[1].ID != e2.ID || path[2].ID != e3.ID {
		t.Fatal("path must be ordered root-first")
	}

	if _, err := sess.GetPathToRoot("nope"); err == nil {
		t.Fatal("expected error for unknown entry")
	}
}

func TestMoveTo_TruncatesLiveContext(t *testing.T) {
	store := treeStore(t)
	sess, _ := store.Create(context.Background())

	_, _ = sess.AppendUserMessage("first")
	e2, _ := sess.AppendAssistantMessage("second", nil)
	_, _ = sess.AppendUserMessage("third")
	_, _ = sess.AppendAssistantMessage("fourth", nil)

	full, _ := sess.BuildContext()

	if err := sess.MoveTo(e2.ID); err != nil {
		t.Fatal(err)
	}
	moved, _ := sess.BuildContext()
	if len(moved) >= len(full) {
		t.Fatalf("MoveTo should shorten the live context: %d -> %d", len(full), len(moved))
	}
	if sess.Leaf() != e2.ID {
		t.Fatalf("leaf = %q want %q", sess.Leaf(), e2.ID)
	}
	// The abandoned entries stay in the log — that is what makes them forkable.
	if len(sess.Entries()) != 4 {
		t.Fatalf("MoveTo must not delete entries, got %d", len(sess.Entries()))
	}
}

func TestMoveTo_BranchesOnNextAppend(t *testing.T) {
	store := treeStore(t)
	sess, _ := store.Create(context.Background())

	_, _ = sess.AppendUserMessage("first")
	e2, _ := sess.AppendAssistantMessage("second", nil)
	_, _ = sess.AppendUserMessage("abandoned branch")

	if err := sess.MoveTo(e2.ID); err != nil {
		t.Fatal(err)
	}
	newEntry, err := sess.AppendUserMessage("new branch")
	if err != nil {
		t.Fatal(err)
	}
	if newEntry.ParentID != e2.ID {
		t.Fatalf("new entry should branch from the moved-to leaf, parent=%q", newEntry.ParentID)
	}

	msgs, _ := sess.BuildContext()
	var joined string
	for _, m := range msgs {
		joined += m.Content
	}
	if contains(joined, "abandoned branch") {
		t.Fatal("abandoned branch must not appear in the live context")
	}
	if !contains(joined, "new branch") {
		t.Fatal("new branch missing from context")
	}
}

func TestMoveTo_PersistsAcrossReload(t *testing.T) {
	store := treeStore(t)
	ctx := context.Background()
	sess, _ := store.Create(ctx)

	_, _ = sess.AppendUserMessage("first")
	e2, _ := sess.AppendAssistantMessage("second", nil)
	_, _ = sess.AppendUserMessage("third")

	if err := sess.MoveTo(e2.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := sess.BuildContext()

	reopened, err := store.Open(ctx, sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Leaf() != e2.ID {
		t.Fatalf("leaf not restored: %q", reopened.Leaf())
	}
	after, _ := reopened.BuildContext()
	if len(before) != len(after) {
		t.Fatalf("context differs across reload: %d vs %d", len(before), len(after))
	}
}

func TestGetBranch(t *testing.T) {
	store := treeStore(t)
	sess, _ := store.Create(context.Background())

	_, _ = sess.AppendUserMessage("a")
	e2, _ := sess.AppendAssistantMessage("b", nil)
	_, _ = sess.AppendUserMessage("c")
	_, _ = sess.AppendAssistantMessage("d", nil)

	branch, err := sess.GetBranch(e2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(branch) != 3 {
		t.Fatalf("expected e2 and its 2 descendants, got %d", len(branch))
	}
	if branch[0].ID != e2.ID {
		t.Fatal("branch must start at the requested entry")
	}
}

func TestFork_RemapsCompactionCutPoint(t *testing.T) {
	store := treeStore(t)
	ctx := context.Background()
	sess, _ := store.Create(ctx)

	_, _ = sess.AppendUserMessage("one")
	e2, _ := sess.AppendAssistantMessage("two", nil)
	_, _ = sess.AppendUserMessage("three")
	if _, err := sess.AppendCompactionAt("the summary", e2.ID); err != nil {
		t.Fatal(err)
	}
	if err := sess.RebuildContext(); err != nil {
		t.Fatal(err)
	}
	before, _ := sess.BuildContext()

	forked, err := store.Fork(ctx, sess.ID(), "")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := forked.BuildContext()

	// Fork rewrites entry ids; if the compaction's recorded cut point were not
	// remapped, the boundary would dangle and the fork's context would differ.
	if len(before) != len(after) {
		t.Fatalf("forked context differs: %d vs %d", len(before), len(after))
	}
	for i := range before {
		if before[i].Content != after[i].Content {
			t.Fatalf("message %d differs after fork:\n before=%q\n after=%q",
				i, before[i].Content, after[i].Content)
		}
	}
}

func TestActiveEntries_LinearSessionUnchanged(t *testing.T) {
	store := treeStore(t)
	sess, _ := store.Create(context.Background())

	call := openai.ToolCall{ID: "c1", Type: openai.ToolTypeFunction,
		Function: openai.FunctionCall{Name: "read_file", Arguments: `{}`}}
	_, _ = sess.AppendUserMessage("go")
	_, _ = sess.AppendAssistantMessage("calling", []openai.ToolCall{call})
	_, _ = sess.AppendToolResult("c1", "result")

	msgs, err := sess.BuildContext()
	if err != nil {
		t.Fatal(err)
	}
	// system + 3
	if len(msgs) != 4 {
		t.Fatalf("linear session context changed shape: %d messages", len(msgs))
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
