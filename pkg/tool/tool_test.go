package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
)

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "main.txt", false},
		{"**/*.go", "pkg/tool/a.go", true},
		{"**/*.go", "a.go", true},
		{"pkg/**/x.go", "pkg/a/x.go", true},
		{"pkg/**/x.go", "pkg/x.go", true},
	}
	for _, tc := range cases {
		got, err := matchGlob(tc.pat, tc.name)
		if err != nil {
			t.Fatalf("%s: %v", tc.pat, err)
		}
		if got != tc.want {
			t.Errorf("matchGlob(%q,%q)=%v want %v", tc.pat, tc.name, got, tc.want)
		}
	}
}

func TestTodosTool(t *testing.T) {
	store := NewTodoStore()
	tt := NewTodosTool(store)
	env, _ := execenv.NewOsExecutionEnv(t.TempDir())
	res, err := tt.Execute(context.Background(), map[string]any{
		"items": []any{
			map[string]any{"id": "1", "content": "do thing", "status": "pending"},
		},
	}, env)
	if err != nil || res.IsError {
		t.Fatalf("%#v %v", res, err)
	}
	if len(store.List()) != 1 {
		t.Fatal("expected 1 todo")
	}
}

func TestGrepTool(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := execenv.NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := DefaultRegistry(nil)
	gt, _ := reg.Get("grep")
	res, err := gt.Execute(context.Background(), map[string]any{"pattern": "Hello", "include": "*.go"}, env)
	if err != nil || res.IsError {
		t.Fatalf("%#v %v", res, err)
	}
	if !strings.Contains(res.Content, "Hello") {
		t.Fatalf("content=%q", res.Content)
	}
}
