package memory

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SemanticIndex builds and queries the "what is this system" tier.
//
// The implementation here is deliberately deterministic: a go/ast repo map plus
// go.mod facts plus hand-authored notes, all searchable through SQLite FTS5.
// A vector index would sit behind this same interface, but embeddings need an
// embedding endpoint, chunking, and re-indexing on every edit — and for finding
// a symbol the existing grep and glob tools are already exact. Swap the backend
// when the repo map measurably stops being enough.
type SemanticIndex interface {
	Reindex(ctx context.Context, root string) (int, error)
	Search(ctx context.Context, query string, limit int) ([]Record, error)
}

// GoRepoIndex indexes Go source structure and project facts into a Store.
type GoRepoIndex struct {
	store *Store
}

// NewGoRepoIndex creates an index backed by store.
func NewGoRepoIndex(store *Store) *GoRepoIndex { return &GoRepoIndex{store: store} }

var skipDirs = map[string]struct{}{
	".git": {}, "node_modules": {}, "vendor": {}, "bin": {}, "testdata": {}, ".golum": {},
}

// Reindex rebuilds the project's semantic memory from disk, replacing whatever
// was there before. Returns the number of records written.
func (g *GoRepoIndex) Reindex(ctx context.Context, root string) (int, error) {
	if g == nil || g.store == nil {
		return 0, fmt.Errorf("memory store unavailable")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return 0, err
	}
	// Semantic memory is derived, so a rebuild replaces rather than accumulates.
	if err := g.store.DeleteTier(ctx, TierSemantic, ScopeProject); err != nil {
		return 0, err
	}

	written := 0
	if rec, ok := moduleFacts(abs); ok {
		if _, err := g.store.Put(ctx, rec); err != nil {
			return written, err
		}
		written++
	}

	files, err := goFiles(ctx, abs)
	if err != nil {
		return written, err
	}
	byPackage := map[string][]string{}
	fset := token.NewFileSet()
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		pkg, decls := parseGoFile(fset, path, abs)
		if pkg == "" || len(decls) == 0 {
			continue
		}
		rel, _ := filepath.Rel(abs, path)
		rel = filepath.ToSlash(rel)
		rec := Record{
			Tier:     TierSemantic,
			Scope:    ScopeProject,
			Key:      "file:" + rel,
			FilePath: rel,
			Content:  fmt.Sprintf("%s (package %s): %s", rel, pkg, strings.Join(decls, ", ")),
		}
		if _, err := g.store.Put(ctx, rec); err != nil {
			return written, err
		}
		written++
		byPackage[pkg] = append(byPackage[pkg], rel)
	}

	// A per-package summary answers "where does X live" without reading files.
	for pkg, paths := range byPackage {
		sort.Strings(paths)
		rec := Record{
			Tier:    TierSemantic,
			Scope:   ScopeProject,
			Key:     "package:" + pkg,
			Content: fmt.Sprintf("package %s spans: %s", pkg, strings.Join(paths, ", ")),
		}
		if _, err := g.store.Put(ctx, rec); err != nil {
			return written, err
		}
		written++
	}

	// Hand-authored architectural notes rank alongside derived facts.
	for _, path := range globSorted(filepath.Join(abs, ".golum", "memory", "semantic", "*.md")) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		body := strings.TrimSpace(string(data))
		if body == "" {
			continue
		}
		rec := Record{
			Tier:    TierSemantic,
			Scope:   ScopeProject,
			Key:     "note:" + filepath.Base(path),
			Content: body,
		}
		if _, err := g.store.Put(ctx, rec); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// Search queries the semantic tier.
func (g *GoRepoIndex) Search(ctx context.Context, query string, limit int) ([]Record, error) {
	if g == nil || g.store == nil {
		return nil, fmt.Errorf("memory store unavailable")
	}
	return g.store.Search(ctx, TierSemantic, query, limit)
}

// moduleFacts records the module path and direct dependency versions, so the
// agent knows which framework versions apply without re-reading go.mod.
func moduleFacts(root string) (Record, bool) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return Record{}, false
	}
	var module string
	var goVersion string
	var deps []string
	inBlock := false
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "module "):
			module = strings.TrimSpace(strings.TrimPrefix(t, "module "))
		case strings.HasPrefix(t, "go "):
			goVersion = strings.TrimSpace(strings.TrimPrefix(t, "go "))
		case strings.HasPrefix(t, "require ("):
			inBlock = true
		case inBlock && t == ")":
			inBlock = false
		case inBlock && t != "":
			if strings.Contains(t, "// indirect") {
				continue
			}
			deps = append(deps, t)
		case strings.HasPrefix(t, "require ") && !inBlock:
			deps = append(deps, strings.TrimPrefix(t, "require "))
		}
	}
	if module == "" {
		return Record{}, false
	}
	content := fmt.Sprintf("Go module %s (go %s). Direct dependencies: %s",
		module, goVersion, strings.Join(deps, "; "))
	return Record{
		Tier: TierSemantic, Scope: ScopeProject,
		Key: "module", FilePath: "go.mod", Content: content,
	}, true
}

func goFiles(ctx context.Context, root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtrees are skipped, not fatal
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if _, skip := skipDirs[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// parseGoFile extracts the package name and exported declarations from one file.
func parseGoFile(fset *token.FileSet, path, root string) (string, []string) {
	src, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return "", nil
	}
	pkg := src.Name.Name
	var decls []string
	for _, d := range src.Decls {
		switch decl := d.(type) {
		case *ast.FuncDecl:
			if !decl.Name.IsExported() {
				continue
			}
			line := fset.Position(decl.Pos()).Line
			if decl.Recv != nil && len(decl.Recv.List) > 0 {
				decls = append(decls, fmt.Sprintf("func (%s) %s:%d",
					receiverName(decl.Recv.List[0].Type), decl.Name.Name, line))
			} else {
				decls = append(decls, fmt.Sprintf("func %s:%d", decl.Name.Name, line))
			}
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ts.Name.IsExported() {
					continue
				}
				kind := "type"
				switch ts.Type.(type) {
				case *ast.InterfaceType:
					kind = "interface"
				case *ast.StructType:
					kind = "struct"
				}
				decls = append(decls, fmt.Sprintf("%s %s:%d",
					kind, ts.Name.Name, fset.Position(ts.Pos()).Line))
			}
		}
	}
	return pkg, decls
}

func receiverName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return "*" + receiverName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return receiverName(t.X)
	case *ast.IndexListExpr:
		return receiverName(t.X)
	default:
		return "?"
	}
}
