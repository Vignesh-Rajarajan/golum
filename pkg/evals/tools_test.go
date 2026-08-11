//go:build evals

package evals

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolsFileRoundTrip(t *testing.T) {
	requireAPIKey(t)

	h := New(Options{
		Name:        "tools",
		ActiveTools: []string{"write_file", "read_file", "edit", "list_dir", "glob"},
	})
	input := "Write a file named note.txt with exactly the content EVAL_OK. Then use read_file to confirm it. Reply with only EVAL_OK when done."
	result, err := h.Run(context.Background(), t, Prompt(input))
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(result.Workspace, "note.txt"))
	if err != nil {
		t.Fatalf("expected note.txt in workspace: %v (output=%q events=%d)", err, result.Output, len(result.Events))
	}
	if got := strings.TrimSpace(string(raw)); got != "EVAL_OK" {
		t.Fatalf("note.txt=%q want EVAL_OK", got)
	}

	scores := scoreAll(t, context.Background(), result, input,
		ToolCalled("write_file", func(args map[string]any, _ string) bool {
			path, _ := args["path"].(string)
			content, _ := args["content"].(string)
			return strings.Contains(path, "note.txt") && strings.Contains(content, "EVAL_OK")
		}),
		ToolCalled("read_file", nil),
	)
	writeArtifact(t, result, scores)

	for _, s := range scores {
		if s.Value < 1 {
			t.Fatalf("tool transcript assertion failed: %s", s.Rationale)
		}
	}
	workspaceOutsideHomeGolum(t, result.Workspace)
}
