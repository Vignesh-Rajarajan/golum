package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	// The memory store shares the session database, so build it the same way
	// the app does rather than hand-rolling a schema that could drift.
	s, err := session.OpenSQLiteStore(filepath.Join(dir, "g.db"),
		&config.Config{Model: "gpt-4o"}, prompt.PromptConfig{CWD: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return NewStore(s.DB())
}

func TestPut_AndList(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	if _, err := st.Put(ctx, Record{Tier: TierProcedural, Scope: ScopeUser,
		Key: "style", Content: "prefers table-driven tests"}); err != nil {
		t.Fatal(err)
	}
	recs, err := st.List(ctx, TierProcedural, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Content != "prefers table-driven tests" {
		t.Fatalf("unexpected records: %+v", recs)
	}
}

func TestPut_KeyUpsertsInsteadOfDuplicating(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	first, err := st.Put(ctx, Record{Tier: TierProcedural, Scope: ScopeUser,
		Key: "style", Content: "original"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.Put(ctx, Record{Tier: TierProcedural, Scope: ScopeUser,
		Key: "style", Content: "revised"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatal("same key should update in place, not create a second record")
	}
	recs, _ := st.List(ctx, TierProcedural, 10)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record after upsert, got %d", len(recs))
	}
	if recs[0].Content != "revised" {
		t.Fatalf("content not updated: %q", recs[0].Content)
	}
}

func TestPut_RejectsEmptyContent(t *testing.T) {
	st := testStore(t)
	if _, err := st.Put(context.Background(), Record{Content: "  "}); err == nil {
		t.Fatal("expected empty content to be rejected")
	}
}

func TestSearch_FindsByWord(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	_, _ = st.Put(ctx, Record{Tier: TierSemantic, Scope: ScopeProject,
		Content: "the retry backoff lives in chat_completion.go"})
	_, _ = st.Put(ctx, Record{Tier: TierSemantic, Scope: ScopeProject,
		Content: "the TUI is built with bubbletea"})

	recs, err := st.Search(ctx, "", "backoff", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || !strings.Contains(recs[0].Content, "backoff") {
		t.Fatalf("search returned %+v", recs)
	}
}

func TestSearch_TierFilter(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	_, _ = st.Put(ctx, Record{Tier: TierSemantic, Scope: ScopeProject, Content: "widget architecture"})
	_, _ = st.Put(ctx, Record{Tier: TierEpisodic, Scope: ScopeProject, Content: "widget refactor failed"})

	recs, err := st.Search(ctx, TierEpisodic, "widget", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Tier != TierEpisodic {
		t.Fatalf("tier filter ignored: %+v", recs)
	}
}

// FTS5 treats characters like " and * as syntax; user text must never reach it raw.
func TestSearch_MalformedQueryDoesNotError(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	_, _ = st.Put(ctx, Record{Tier: TierSemantic, Scope: ScopeProject, Content: "something"})

	for _, q := range []string{`"`, `foo AND (`, `*`, `^^^`, ``} {
		if _, err := st.Search(ctx, "", q, 5); err != nil {
			t.Fatalf("query %q returned error: %v", q, err)
		}
	}
}

func TestForget(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	rec, _ := st.Put(ctx, Record{Tier: TierProcedural, Scope: ScopeUser,
		Key: "gone", Content: "temporary"})
	n, err := st.Forget(ctx, rec.ID)
	if err != nil || n != 1 {
		t.Fatalf("forget by id: n=%d err=%v", n, err)
	}
	if recs, _ := st.List(ctx, "", 10); len(recs) != 0 {
		t.Fatalf("record survived deletion: %+v", recs)
	}
	if n, _ := st.Forget(ctx, "missing"); n != 0 {
		t.Fatal("forgetting an unknown id should report 0")
	}
}

func TestListScoped(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	_, _ = st.Put(ctx, Record{Tier: TierProcedural, Scope: ScopeUser, Content: "user fact"})
	_, _ = st.Put(ctx, Record{Tier: TierProcedural, Scope: ScopeProject, Content: "project fact"})

	recs, err := st.ListScoped(ctx, TierProcedural, ScopeUser, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Content != "user fact" {
		t.Fatalf("scope filter ignored: %+v", recs)
	}
}

func TestFormatForPrompt(t *testing.T) {
	out := FormatForPrompt([]Record{
		{Key: "style", Content: "table-driven tests"},
		{Content: "no key here"},
	})
	if !strings.Contains(out, "**style**") || !strings.Contains(out, "no key here") {
		t.Fatalf("unexpected rendering:\n%s", out)
	}
	if FormatForPrompt(nil) != "" {
		t.Fatal("empty input should render empty")
	}
}

func TestNilStoreIsSafe(t *testing.T) {
	var st *Store
	if _, err := st.Put(context.Background(), Record{Content: "x"}); err == nil {
		t.Fatal("expected an error from a nil store")
	}
	if recs, _ := st.ListScoped(context.Background(), TierProcedural, ScopeUser, 5); recs != nil {
		t.Fatal("nil store should return no records")
	}
}

// ---- procedural loading -----------------------------------------------------

func TestLoadProjectInstructions_NearestWins(t *testing.T) {
	root := t.TempDir()
	// A .git marker stops the upward walk at the repo root.
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "pkg", "inner")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "AGENTS.md"), "root rule")
	write(t, filepath.Join(sub, "AGENTS.md"), "inner rule")

	out, err := LoadProjectInstructions(sub)
	if err != nil {
		t.Fatal(err)
	}
	rootAt := strings.Index(out, "root rule")
	innerAt := strings.Index(out, "inner rule")
	if rootAt < 0 || innerAt < 0 {
		t.Fatalf("both files should be included:\n%s", out)
	}
	// Deeper instructions must come last so they take precedence on a read-through.
	if rootAt > innerAt {
		t.Fatalf("expected root before inner (nearest wins):\n%s", out)
	}
}

func TestLoadProjectInstructions_StopsAtRepoRoot(t *testing.T) {
	outer := t.TempDir()
	write(t, filepath.Join(outer, "AGENTS.md"), "SHOULD NOT BE READ")

	repo := filepath.Join(outer, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "AGENTS.md"), "repo rule")

	out, err := LoadProjectInstructions(repo)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "SHOULD NOT BE READ") {
		t.Fatal("walk must stop at the repository root")
	}
	if !strings.Contains(out, "repo rule") {
		t.Fatalf("repo AGENTS.md missing:\n%s", out)
	}
}

func TestLoadProjectInstructions_IncludesProceduralNotes(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".golum", "memory", "procedural")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "testing.md"), "always run go vet")

	out, err := LoadProjectInstructions(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "always run go vet") {
		t.Fatalf("procedural note missing:\n%s", out)
	}
}

func TestLoadProjectInstructions_EmptyWhenNothingPresent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := LoadProjectInstructions(root)
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Fatalf("expected empty, got %q", out)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
