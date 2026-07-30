package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goRepo writes a small module to disk for indexing.
func goRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), `module example.com/demo

go 1.25

require (
	github.com/some/dep v1.2.3
	golang.org/x/sys v0.1.0 // indirect
)
`)
	if err := os.MkdirAll(filepath.Join(root, "pkg", "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "main.go"), `package main

func main() {}

// Run is exported.
func Run(n int) error { return nil }
`)
	write(t, filepath.Join(root, "pkg", "store", "store.go"), `package store

type Widget struct{ Name string }

type Repository interface {
	Save(w Widget) error
}

func (w *Widget) Rename(s string) {}

func NewWidget() *Widget { return &Widget{} }

func unexported() {}
`)
	return root
}

func TestReindex_IndexesDeclarations(t *testing.T) {
	st := testStore(t)
	root := goRepo(t)
	idx := NewGoRepoIndex(st)

	n, err := idx.Reindex(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("expected records to be written")
	}

	recs, err := st.List(context.Background(), TierSemantic, 100)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, r := range recs {
		all.WriteString(r.Content)
		all.WriteString("\n")
	}
	got := all.String()

	for _, want := range []string{
		"struct Widget", "interface Repository", "func NewWidget", "func (*Widget) Rename", "func Run",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("index missing %q\n---\n%s", want, got)
		}
	}
	// Unexported declarations are noise for navigation.
	if strings.Contains(got, "unexported") {
		t.Error("unexported declarations should not be indexed")
	}
}

func TestReindex_RecordsModuleAndDirectDepsOnly(t *testing.T) {
	st := testStore(t)
	root := goRepo(t)

	if _, err := NewGoRepoIndex(st).Reindex(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	recs, _ := st.Search(context.Background(), TierSemantic, "module", 10)
	var found string
	for _, r := range recs {
		if r.Key == "module" {
			found = r.Content
		}
	}
	if found == "" {
		t.Fatal("no module record written")
	}
	if !strings.Contains(found, "example.com/demo") {
		t.Errorf("module path missing: %q", found)
	}
	if !strings.Contains(found, "github.com/some/dep") {
		t.Errorf("direct dependency missing: %q", found)
	}
	if strings.Contains(found, "golang.org/x/sys") {
		t.Errorf("indirect dependency should be excluded: %q", found)
	}
}

func TestReindex_PackageSummaries(t *testing.T) {
	st := testStore(t)
	root := goRepo(t)

	if _, err := NewGoRepoIndex(st).Reindex(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	recs, _ := st.List(context.Background(), TierSemantic, 100)
	var pkgRec string
	for _, r := range recs {
		if r.Key == "package:store" {
			pkgRec = r.Content
		}
	}
	if pkgRec == "" {
		t.Fatal("expected a package:store summary")
	}
	if !strings.Contains(pkgRec, "pkg/store/store.go") {
		t.Errorf("package summary should list its files: %q", pkgRec)
	}
}

func TestReindex_IsIdempotentAndReplaces(t *testing.T) {
	st := testStore(t)
	root := goRepo(t)
	idx := NewGoRepoIndex(st)
	ctx := context.Background()

	first, err := idx.Reindex(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	after1, _ := st.List(ctx, TierSemantic, 500)

	second, err := idx.Reindex(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	after2, _ := st.List(ctx, TierSemantic, 500)

	if first != second {
		t.Fatalf("reindex should be stable: %d then %d", first, second)
	}
	// Semantic memory is derived, so a rebuild must replace, not accumulate.
	if len(after1) != len(after2) {
		t.Fatalf("reindex accumulated duplicates: %d -> %d", len(after1), len(after2))
	}
}

func TestReindex_DropsRecordsForDeletedFiles(t *testing.T) {
	st := testStore(t)
	root := goRepo(t)
	idx := NewGoRepoIndex(st)
	ctx := context.Background()

	if _, err := idx.Reindex(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "pkg", "store", "store.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Reindex(ctx, root); err != nil {
		t.Fatal(err)
	}

	recs, _ := st.List(ctx, TierSemantic, 500)
	for _, r := range recs {
		if strings.Contains(r.Content, "Widget") {
			t.Fatalf("stale record survived reindex: %q", r.Content)
		}
	}
}

func TestReindex_PreservesOtherTiers(t *testing.T) {
	st := testStore(t)
	root := goRepo(t)
	ctx := context.Background()

	if _, err := st.Put(ctx, Record{Tier: TierProcedural, Scope: ScopeUser,
		Key: "style", Content: "keep me"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(ctx, Record{Tier: TierEpisodic, Scope: ScopeProject,
		Content: "something happened"}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewGoRepoIndex(st).Reindex(ctx, root); err != nil {
		t.Fatal(err)
	}

	if recs, _ := st.List(ctx, TierProcedural, 10); len(recs) != 1 {
		t.Fatalf("reindex clobbered procedural memory: %+v", recs)
	}
	if recs, _ := st.List(ctx, TierEpisodic, 10); len(recs) != 1 {
		t.Fatalf("reindex clobbered episodic memory: %+v", recs)
	}
}

func TestReindex_IncludesSemanticNotes(t *testing.T) {
	st := testStore(t)
	root := goRepo(t)
	dir := filepath.Join(root, ".golum", "memory", "semantic")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "arch.md"), "Billing rules live in the ledger service.")

	if _, err := NewGoRepoIndex(st).Reindex(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	recs, err := st.Search(context.Background(), TierSemantic, "billing ledger", 10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range recs {
		if strings.Contains(r.Content, "ledger service") {
			found = true
		}
	}
	if !found {
		t.Fatalf("hand-authored note not searchable: %+v", recs)
	}
}

func TestSearch_FindsSymbolByName(t *testing.T) {
	st := testStore(t)
	root := goRepo(t)
	idx := NewGoRepoIndex(st)
	ctx := context.Background()

	if _, err := idx.Reindex(ctx, root); err != nil {
		t.Fatal(err)
	}
	recs, err := idx.Search(ctx, "Repository", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 {
		t.Fatal("expected to locate the Repository interface")
	}
	var sawFile bool
	for _, r := range recs {
		if strings.Contains(r.Content, "pkg/store/store.go") {
			sawFile = true
		}
	}
	if !sawFile {
		t.Fatalf("search should point at the defining file: %+v", recs)
	}
}

func TestReindex_SkipsVendorAndTests(t *testing.T) {
	st := testStore(t)
	root := goRepo(t)
	for _, dir := range []string{"vendor", "node_modules", ".git"} {
		p := filepath.Join(root, dir)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(p, "junk.go"), "package junk\n\nfunc ShouldNotAppear() {}\n")
	}
	write(t, filepath.Join(root, "main_test.go"), "package main\n\nfunc TestShouldNotAppear(t *testing.T) {}\n")

	if _, err := NewGoRepoIndex(st).Reindex(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	recs, _ := st.List(context.Background(), TierSemantic, 500)
	for _, r := range recs {
		if strings.Contains(r.Content, "ShouldNotAppear") {
			t.Fatalf("indexed an excluded path: %q", r.Content)
		}
	}
}

func TestReindex_UnparseableFileDoesNotFail(t *testing.T) {
	st := testStore(t)
	root := goRepo(t)
	write(t, filepath.Join(root, "broken.go"), "package main\n\nfunc oops( {{{\n")

	n, err := NewGoRepoIndex(st).Reindex(context.Background(), root)
	if err != nil {
		t.Fatalf("a syntax error in one file must not fail the whole index: %v", err)
	}
	if n == 0 {
		t.Fatal("valid files should still be indexed")
	}
}

func TestReindex_NilStore(t *testing.T) {
	var idx *GoRepoIndex
	if _, err := idx.Reindex(context.Background(), t.TempDir()); err == nil {
		t.Fatal("expected an error from a nil index")
	}
	if _, err := NewGoRepoIndex(nil).Search(context.Background(), "x", 5); err == nil {
		t.Fatal("expected an error searching a nil store")
	}
}

// GoRepoIndex must satisfy the interface that a vector backend would replace.
var _ SemanticIndex = (*GoRepoIndex)(nil)
