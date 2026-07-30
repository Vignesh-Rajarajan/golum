package tool

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/memory"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
)

func memTool(t *testing.T) (AgentTool, *memory.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := session.OpenSQLiteStore(filepath.Join(dir, "g.db"),
		&config.Config{Model: "gpt-4o"}, prompt.PromptConfig{CWD: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ms := memory.NewStore(store.DB())
	return NewMemoryTool(ms, func() string { return "sess_test" }), ms
}

func TestMemoryTool_RememberThenSearch(t *testing.T) {
	mt, _ := memTool(t)
	ctx := context.Background()

	res, err := mt.Execute(ctx, map[string]any{
		"action": "remember", "key": "style", "content": "prefers small functions",
	}, nil)
	if err != nil || res.IsError {
		t.Fatalf("remember failed: %#v err=%v", res, err)
	}

	res, err = mt.Execute(ctx, map[string]any{"action": "search", "query": "functions"}, nil)
	if err != nil || res.IsError {
		t.Fatalf("search failed: %#v err=%v", res, err)
	}
	if !strings.Contains(res.Content, "prefers small functions") {
		t.Fatalf("search missed the memory: %q", res.Content)
	}
}

func TestMemoryTool_DefaultsToUserProcedural(t *testing.T) {
	mt, ms := memTool(t)
	ctx := context.Background()

	if _, err := mt.Execute(ctx, map[string]any{
		"action": "remember", "content": "likes tabs",
	}, nil); err != nil {
		t.Fatal(err)
	}
	// The prompt describes memory as user-specific preferences, so an
	// unqualified remember must land in procedural/user where the system
	// prompt's memory section reads from.
	recs, err := ms.ListScoped(ctx, memory.TierProcedural, memory.ScopeUser, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected the default tier/scope to be procedural/user, got %+v", recs)
	}
}

func TestMemoryTool_RejectsBadInput(t *testing.T) {
	mt, _ := memTool(t)
	ctx := context.Background()

	cases := []map[string]any{
		{"action": "remember"},                                  // no content
		{"action": "bogus"},                                     // unknown action
		{"action": "remember", "content": "x", "tier": "wrong"}, // bad tier
		{"action": "remember", "content": "x", "scope": "wrong"},
		{"action": "forget"}, // no id
	}
	for i, args := range cases {
		res, err := mt.Execute(ctx, args, nil)
		if err != nil {
			t.Fatalf("case %d returned a hard error (should be tool-visible): %v", i, err)
		}
		if !res.IsError {
			t.Fatalf("case %d should be an error result: %#v", i, res)
		}
	}
}

func TestMemoryTool_Forget(t *testing.T) {
	mt, _ := memTool(t)
	ctx := context.Background()

	if _, err := mt.Execute(ctx, map[string]any{
		"action": "remember", "key": "temp", "content": "short lived",
	}, nil); err != nil {
		t.Fatal(err)
	}
	res, err := mt.Execute(ctx, map[string]any{"action": "forget", "id": "temp"}, nil)
	if err != nil || res.IsError {
		t.Fatalf("forget failed: %#v err=%v", res, err)
	}
	res, _ = mt.Execute(ctx, map[string]any{"action": "list"}, nil)
	if !strings.Contains(res.Content, "No memories") {
		t.Fatalf("memory survived forget: %q", res.Content)
	}
}

func TestMemoryTool_UnavailableStoreIsToolVisible(t *testing.T) {
	mt := NewMemoryTool(nil, nil)
	res, err := mt.Execute(context.Background(), map[string]any{"action": "list"}, nil)
	if err != nil {
		t.Fatalf("missing store must not be a hard error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected a tool-visible error")
	}
}

func TestRegisterMemory_AddsToolAndKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	store, err := session.OpenSQLiteStore(filepath.Join(dir, "g.db"),
		&config.Config{Model: "gpt-4o"}, prompt.PromptConfig{CWD: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	reg, _ := DefaultRegistry(nil)
	before := len(reg.Names())
	RegisterMemory(reg, memory.NewStore(store.DB()), nil)

	if _, ok := reg.Get("memory"); !ok {
		t.Fatal("memory tool not registered")
	}
	if len(reg.Names()) != before+1 {
		t.Fatalf("expected exactly one new tool, got %d -> %d", before, len(reg.Names()))
	}
	// A nil store must leave the registry untouched, so the prompt never
	// advertises a tool that cannot work.
	reg2, _ := DefaultRegistry(nil)
	RegisterMemory(reg2, nil, nil)
	if _, ok := reg2.Get("memory"); ok {
		t.Fatal("memory tool must not be registered without a store")
	}
}
