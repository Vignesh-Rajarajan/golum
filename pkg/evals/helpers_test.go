//go:build evals

package evals

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func scoreAll(t *testing.T, ctx context.Context, result *Result, input string, judges ...Judge) []Score {
	t.Helper()
	out := make([]Score, 0, len(judges))
	for _, j := range judges {
		s, err := j(ctx, result, input)
		if err != nil {
			t.Fatalf("judge: %v", err)
		}
		out = append(out, s)
		t.Logf("score=%.0f rationale=%s", s.Value, s.Rationale)
	}
	return out
}

func writeArtifact(t *testing.T, result *Result, scores []Score) {
	t.Helper()
	dir, err := ArtifactDir()
	if err != nil {
		t.Fatalf("artifact dir: %v", err)
	}
	if err := WriteRunArtifact(dir, result.RunID, result, scores); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	t.Logf("artifacts: %s", dir)
}

func requireAPIKey(t *testing.T) {
	t.Helper()
	if os.Getenv("OPENAI_API_KEY") == "" && os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Fatal("set OPENAI_API_KEY or OPENROUTER_API_KEY to run evals")
	}
}

func workspaceOutsideHomeGolum(t *testing.T, workspace string) {
	t.Helper()
	home := os.Getenv("HOME")
	if home == "" {
		return
	}
	if strings.HasPrefix(workspace, filepath.Join(home, ".golum")) {
		t.Fatalf("eval workspace must not be under ~/.golum: %s", workspace)
	}
}
