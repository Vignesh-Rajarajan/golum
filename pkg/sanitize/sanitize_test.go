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

func TestStripPseudoToolMarkup_empty(t *testing.T) {
	if got := StripPseudoToolMarkup(""); got != "" {
		t.Fatalf("expected empty string unchanged, got %q", got)
	}
}

func TestStripPseudoToolMarkup_readFilePseudoOnly(t *testing.T) {
	const in = `Checking the file now.

<read_file>
<path>main.go</path>
</read_file>

Done.`
	out := StripPseudoToolMarkup(in)
	if strings.Contains(out, "<read_file") || strings.Contains(out, "<path") {
		t.Fatalf("expected pseudo read_file block removed, got:\n%s", out)
	}
	if !strings.Contains(out, "Checking the file now") || !strings.Contains(out, "Done") {
		t.Fatalf("expected prose kept: %q", out)
	}
}

func TestStripPseudoToolMarkup_unclosedSuffixVariants(t *testing.T) {
	cases := []struct {
		name string
		in   string
		keep string
	}{
		{"tool_call suffix", "Let me look.<tool\n_call", "Let me look."},
		{"function= suffix", "Let me look.<tool function=read_file", "Let me look."},
		{"parameter suffix", "Let me look.<tool><parameter=path", "Let me look."},
		{"function tag suffix", "Let me look.<tool><function name=", "Let me look."},
		{"read_file suffix", "Let me look.<tool><read_file", "Let me look."},
		{"path suffix", "Let me look.<tool><path>a.go", "Let me look."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := StripPseudoToolMarkup(tc.in)
			if out != tc.keep {
				t.Fatalf("got %q want %q", out, tc.keep)
			}
		})
	}
}

func TestStripPseudoToolMarkup_closedToolTagNotTruncated(t *testing.T) {
	// "<toolXYZ>" doesn't match any of the tool_call/tool-wrapper regexes (no
	// whitespace or ">" directly after "tool"), so it reaches
	// stripUnclosedToolLikeSuffix still containing "<tool". Because it has a
	// matching "</tool>" later in the string, it must be treated as closed and
	// left untouched rather than truncated as a dangling unclosed suffix.
	const in = `Before.<toolXYZ>hello</tool>After.`
	if got := StripPseudoToolMarkup(in); got != in {
		t.Fatalf("expected unchanged text, got %q", got)
	}
}

func TestStripPseudoToolMarkup_unrelatedToolMentionUnchanged(t *testing.T) {
	// "<tool" with no closing tag and no suspicious suffix keyword should be
	// left untouched (falls through to the final `return s`).
	const in = "The <tool belt on my desk has a hammer in it."
	if got := StripPseudoToolMarkup(in); got != in {
		t.Fatalf("expected unchanged text, got %q", got)
	}
}

func TestStripPseudoToolMarkup_toolWrapper(t *testing.T) {
	const in = `I'll check AGENTS.md.

<tool>
<read_file>
<path>AGENTS.md</path>
</read_file>
</tool>

More text.`
	out := StripPseudoToolMarkup(in)
	if strings.Contains(out, "<tool") || strings.Contains(out, "read_file") {
		t.Fatalf("expected pseudo tool XML removed, got:\n%s", out)
	}
	if !strings.Contains(out, "I'll check") || !strings.Contains(out, "More text") {
		t.Fatalf("expected prose kept: %q", out)
	}
}
