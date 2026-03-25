package tokenizer

import (
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(""); got != 1 {
		t.Fatalf("empty: want 1, got %d", got)
	}
	if got := EstimateTokens("abc"); got != 1 {
		t.Fatalf("abc: want 1, got %d", got)
	}
	if got := EstimateTokens("abcd"); got != 1 {
		t.Fatalf("abcd: want 1, got %d", got)
	}
	if got := EstimateTokens(strings.Repeat("a", 8)); got != 2 {
		t.Fatalf("8 runes: want 2, got %d", got)
	}
}

func TestCountTokens(t *testing.T) {
	n := CountTokens("hello world", "gpt-4")
	if n < 1 {
		t.Fatalf("expected positive token count, got %d", n)
	}
}

func TestTruncateTextNoOp(t *testing.T) {
	s := "short"
	out := TruncateText(s, "gpt-4", 10_000, DefaultTruncateSuffix, true)
	if out != s {
		t.Fatalf("want %q, got %q", s, out)
	}
}

func TestTruncateTextPreservesLines(t *testing.T) {
	long := strings.Repeat("word ", 500) + "\n" + strings.Repeat("other ", 500)
	out := TruncateText(long, "gpt-4", 50, DefaultTruncateSuffix, true)
	if !strings.HasSuffix(out, DefaultTruncateSuffix) {
		t.Fatalf("expected suffix, got len %d", len(out))
	}
	if strings.Count(out, "\n") < 1 {
		t.Fatal("expected at least one newline when preserving lines on multi-line input")
	}
}

func TestTruncateTextByRunes(t *testing.T) {
	long := strings.Repeat("x", 10_000)
	out := TruncateText(long, "gpt-4", 20, DefaultTruncateSuffix, false)
	if len(out) >= len(long) {
		t.Fatal("expected truncation")
	}
	if !strings.HasSuffix(out, DefaultTruncateSuffix) {
		t.Fatal("expected suffix")
	}
}

func TestTruncateTextTargetNonPositive(t *testing.T) {
	out := TruncateText("hello", "gpt-4", 1, "\n... [truncated]", true)
	if strings.TrimSpace(out) == "" {
		t.Fatal("expected trimmed suffix only")
	}
}
