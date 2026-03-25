package sanitize

import (
	"regexp"
	"strings"
)

var (
	// Contiguous <tool_call>...</tool_call>
	reToolCallBlock = regexp.MustCompile(`(?is)<tool_call[^>]*>.*?</tool_call>`)
	// Split opening tag: <tool newline _call>
	reToolSplitOpen = regexp.MustCompile(`(?is)<tool\s*[\r\n]+\s*_call[^>]*>.*?</tool_call>`)
	// Variants like <tool_call with spaces
	reToolLoose = regexp.MustCompile(`(?is)<tool\s*_call[^>]*>.*?</tool_call>`)
)

// StripPseudoToolMarkup removes pseudo-XML tool blocks that models sometimes emit as
// plain text (e.g. <tool_call>, <function=todos>) when no tool runtime exists.
func StripPseudoToolMarkup(s string) string {
	if s == "" {
		return s
	}
	out := reToolCallBlock.ReplaceAllString(s, "")
	out = reToolSplitOpen.ReplaceAllString(out, "")
	out = reToolLoose.ReplaceAllString(out, "")
	out = stripUnclosedToolLikeSuffix(out)
	return strings.TrimSpace(out)
}

func stripUnclosedToolLikeSuffix(s string) string {
	lower := strings.ToLower(s)
	idx := strings.Index(lower, "<tool")
	if idx < 0 {
		return s
	}
	rest := s[idx:]
	if strings.Contains(rest, "</tool_call>") {
		return s
	}
	restLower := lower[idx:]
	if strings.Contains(restLower, "_call") ||
		strings.Contains(restLower, "function=") ||
		strings.Contains(restLower, "<parameter") ||
		strings.Contains(restLower, "<function") {
		return strings.TrimSpace(s[:idx])
	}
	return s
}
