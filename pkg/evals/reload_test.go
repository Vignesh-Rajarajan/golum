//go:build evals

package evals

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReloadAuthoredSkill(t *testing.T) {
	requireAPIKey(t)

	h := New(Options{
		Name:        "reload-skill",
		ActiveTools: []string{"write_file", "read_file", "list_dir", "edit"},
	})

	author := strings.Join([]string{
		`Create a new skill file at .golum/skills/magic-number.md.`,
		`Use this exact content (including the frontmatter):`,
		"",
		"---",
		"name: magic-number",
		"description: Returns the project magic number via a marker file",
		"---",
		"",
		"When the user asks for the magic number, you MUST first write_file",
		"magic.txt with content 42, then reply with exactly 42.",
		"",
		"Do not explain. Just create the skill file.",
	}, "\n")

	use := "Use the magic-number skill. What is the magic number? Reply with only the number."

	result, err := h.Run(context.Background(), t, Prompt(author), Reload, Prompt(use))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	workspaceOutsideHomeGolum(t, result.Workspace)

	skillPath := filepath.Join(result.Workspace, ".golum", "skills", "magic-number.md")
	if _, err := os.Stat(skillPath); err != nil {
		t.Fatalf("authored skill missing: %v (output=%q)", err, result.Output)
	}

	writeScore, err := ToolCalled("write_file", func(args map[string]any, _ string) bool {
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		return strings.Contains(path, "magic.txt") && strings.Contains(content, "42")
	})(context.Background(), result, use)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	t.Logf("magic.txt write score=%.0f %s", writeScore.Value, writeScore.Rationale)
	writeArtifact(t, result, []Score{writeScore, Score{Value: 1, Rationale: "structural ok"}})

	if writeScore.Value < 1 {
		// Soft-fail would hide reload bugs; this suite's purpose is proving Reload.
		t.Fatalf("after Reload, expected write_file magic.txt with 42: %s (output=%q)",
			writeScore.Rationale, result.Output)
	}
	if !strings.Contains(result.Output, "42") {
		t.Fatalf("expected final output to include 42, got %q", result.Output)
	}
}
