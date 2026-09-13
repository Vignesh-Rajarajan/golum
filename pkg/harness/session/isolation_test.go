package session

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
)

func TestForkDoesNotMutateParent(t *testing.T) {
	store := treeStore(t)
	ctx := context.Background()
	parent, err := store.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := parent.AppendUserMessage("parent only")
	if _, err := parent.AppendApprovalAlways("write_file"); err != nil {
		t.Fatal(err)
	}
	if err := parent.SetLabel("parent"); err != nil {
		t.Fatal(err)
	}
	child, err := store.Fork(ctx, parent.ID(), e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := child.AppendUserMessage("child only"); err != nil {
		t.Fatal(err)
	}
	if child.ID() == parent.ID() {
		t.Fatal("fork reused the parent id")
	}
	var parentHasChild bool
	for _, ent := range parent.Entries() {
		if ent.Content == "child only" {
			parentHasChild = true
		}
	}
	if parentHasChild {
		t.Fatal("fork mutated the parent")
	}
	if parent.Label() != "parent" {
		t.Fatalf("parent label drifted: %q", parent.Label())
	}
}

func TestSiblingBranchesDoNotLeakEntries(t *testing.T) {
	store := treeStore(t)
	sess, _ := store.Create(context.Background())
	root, _ := sess.AppendUserMessage("root")
	left, _ := sess.AppendUserMessage("left")
	if err := sess.MoveTo(root.ID); err != nil {
		t.Fatal(err)
	}
	right, _ := sess.AppendUserMessage("right")

	leftBranch, err := sess.GetBranch(left.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range leftBranch {
		if e.ID == right.ID {
			t.Fatal("right sibling leaked into the left branch")
		}
	}
	rightBranch, err := sess.GetBranch(right.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rightBranch {
		if e.ID == left.ID {
			t.Fatal("left sibling leaked into the right branch")
		}
	}
}

func TestAlwaysAllowDoesNotCrossSessions(t *testing.T) {
	a := NewInMemorySession("a", contextmgr.NewContextManager(&config.Config{Model: "gpt-4o"}, prompt.PromptConfig{}, nil, nil))
	b := NewInMemorySession("b", contextmgr.NewContextManager(&config.Config{Model: "gpt-4o"}, prompt.PromptConfig{}, nil, nil))
	if _, err := a.AppendApprovalAlways("shell"); err != nil {
		t.Fatal(err)
	}
	if !a.AlwaysAllowed("shell") {
		t.Fatal("expected always-allow on session a")
	}
	if b.AlwaysAllowed("shell") {
		t.Fatal("always-allow leaked into session b")
	}
}

func TestConcurrentSessionsShareNothing(t *testing.T) {
	var wg sync.WaitGroup
	ids := make([]string, 8)
	todos := make([]*tool.TodoStore, 8)
	errc := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			cfg := &config.Config{Model: "gpt-4o"}
			cm := contextmgr.NewContextManager(cfg, prompt.PromptConfig{}, nil, nil)
			sess := NewInMemorySession("", cm)
			ids[i] = sess.ID()
			todos[i] = tool.NewTodoStore()
			if _, err := sess.AppendUserMessage("hello"); err != nil {
				errc <- err
				return
			}
			if _, err := sess.AppendApprovalAlways("write_file"); err != nil {
				errc <- err
				return
			}
			if _, err := sess.AppendTodos([]tool.TodoItem{{ID: sess.ID(), Content: sess.ID()}}); err != nil {
				errc <- err
			}
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" {
			t.Fatal("empty session id")
		}
		if seen[id] {
			t.Fatalf("duplicate session id %s", id)
		}
		seen[id] = true
	}
}

func TestTodosStayOnSelectedBranch(t *testing.T) {
	store := treeStore(t)
	sess, _ := store.Create(context.Background())
	root, _ := sess.AppendUserMessage("root")
	if _, err := sess.AppendTodos([]tool.TodoItem{{ID: "1", Content: "left todo"}}); err != nil {
		t.Fatal(err)
	}
	if err := sess.MoveTo(root.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendTodos([]tool.TodoItem{{ID: "2", Content: "right todo"}}); err != nil {
		t.Fatal(err)
	}
	path, err := sess.GetPathToRoot(sess.Leaf())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range path {
		if e.Kind == EntryTodos {
			raw, _ := jsonTodos(e)
			if contains(raw, "left todo") {
				t.Fatal("left-branch todo leaked onto the selected path")
			}
		}
	}
}

func jsonTodos(e Entry) (string, bool) {
	if e.Meta == nil {
		return "", false
	}
	raw, _ := e.Meta["items"]
	s, _ := raw.(string)
	if s != "" {
		return s, true
	}
	return fmtTodos(raw), true
}

func fmtTodos(v any) string {
	return fmt.Sprintf("%v", v)
}
