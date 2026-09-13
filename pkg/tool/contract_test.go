package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
)

func TestDefaultToolsHaveStableNamesAndSchemas(t *testing.T) {
	reg, _ := DefaultRegistry(nil)
	want := []string{"read_file", "write_file", "edit", "list_dir", "glob", "grep", "shell", "todos", "invoke"}
	if got := reg.Names(); len(got) != len(want) {
		t.Fatalf("names=%v", got)
	}
	for _, name := range want {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("missing %s", name)
		}
		if tool.Name() != name {
			t.Fatalf("name drifted: %q", tool.Name())
		}
		if strings.TrimSpace(tool.Description()) == "" {
			t.Fatalf("%s has empty description", name)
		}
		schema := tool.Parameters()
		raw, err := json.Marshal(schema)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"type":"object"`) {
			t.Fatalf("%s schema is not an object: %s", name, raw)
		}
	}
}

func TestDefaultToolsRejectMissingAndWrongTypes(t *testing.T) {
	env, err := execenv.NewOsExecutionEnv(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := DefaultRegistry(nil)
	ctx := context.Background()
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"read_file", map[string]any{}, "missing required"},
		{"write_file", map[string]any{"path": "a.txt"}, "missing required"},
		{"edit", map[string]any{"path": "a.txt"}, "missing required"},
		{"shell", map[string]any{}, "missing required"},
		{"invoke", map[string]any{}, "action must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool, _ := reg.Get(tc.name)
			res, err := tool.Execute(ctx, tc.args, env)
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError || !strings.Contains(res.Content, tc.want) {
				t.Fatalf("got %#v", res)
			}
		})
	}
}

func TestWriteFileCancelledByContext(t *testing.T) {
	env, err := execenv.NewOsExecutionEnv(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tool, _ := DefaultRegistry(nil)
	w, _ := tool.Get("write_file")
	res, err := w.Execute(ctx, map[string]any{"path": "a.txt", "content": "x"}, env)
	if err == nil && !res.IsError {
		t.Fatal("expected a cancelled write to fail")
	}
}

func TestRequiresApprovalAndReplayClassification(t *testing.T) {
	if !RequiresApproval("write_file") || !RequiresApproval("shell") || !RequiresApproval("edit") {
		t.Fatal("mutating tools must require approval")
	}
	if RequiresApproval("read_file") || RequiresApproval("invoke") {
		t.Fatal("read-only tools must not require approval")
	}
}
