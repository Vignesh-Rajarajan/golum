package sanitize

import (
	"strings"
	"testing"
)

func TestStripPseudoToolMarkup_userSample(t *testing.T) {
	const in = `I'll write a FizzBuzz program in Brainfuck. This is a classic challenge given Brainfuck's minimal instruction set.<tool
_call>
<function=todos>
<parameter=todos>
[{"content": "Design Brainfuck FizzBuzz algorithm and memory layout", "status": "in_progress"}]
</parameter>
</function>
</tool_call>`

	out := StripPseudoToolMarkup(in)
	if strings.Contains(out, "tool_call") || strings.Contains(out, "function=todos") {
		t.Fatalf("expected tool markup removed, got:\n%s", out)
	}
	if !strings.Contains(out, "FizzBuzz") || !strings.Contains(out, "Brainfuck") {
		t.Fatalf("expected prose kept: %q", out)
	}
}

func TestStripPseudoToolMarkup_plain(t *testing.T) {
	s := "Hello **world**"
	if StripPseudoToolMarkup(s) != s {
		t.Fatal("plain text should be unchanged")
	}
}
