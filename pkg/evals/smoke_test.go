//go:build evals

package evals

import (
	"context"
	"strings"
	"testing"
)

func TestSmoke(t *testing.T) {
	requireAPIKey(t)

	h := New(Options{
		Name:        "smoke",
		ActiveTools: []string{}, // no tools
	})
	input := "What's the capital of France? Reply with only the city name."
	result, err := h.Run(context.Background(), t, Prompt(input))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	scores := scoreAll(t, context.Background(), result, input, Equals("Paris"))
	writeArtifact(t, result, scores)

	if got := strings.TrimSpace(result.Output); got != "Paris" {
		t.Fatalf("output=%q want Paris", got)
	}
	if result.Usage.ToolCalls != 0 {
		t.Fatalf("tool calls=%d want 0", result.Usage.ToolCalls)
	}
	// Usage accounting is a soft signal, not a hard invariant: some providers
	// (confirmed for at least one free-tier OpenRouter model) omit usage from
	// the stream entirely even with stream_options.include_usage requested.
	// When it's present, it should be sane; when it's absent, that's the
	// provider's choice, not a harness bug.
	if result.Usage.TotalTokens <= 0 {
		t.Logf("provider reported no usage stats for model %q (total tokens=%d)", result.Usage.Model, result.Usage.TotalTokens)
	} else {
		t.Logf("total tokens=%d", result.Usage.TotalTokens)
	}
}
