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
	// Generic <tool>...</tool> (models often emit this instead of tool_call)
	reToolWrapper = regexp.MustCompile(`(?is)<tool(?:\s[^>]*)?>.*?</tool>`)
	// Pseudo read_file blocks (plain-text XML)
	reReadFilePseudo = regexp.MustCompile(`(?is)<read_file[^>]*>.*?</read_file>`)
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
	out = reToolWrapper.ReplaceAllString(out, "")
	out = reReadFilePseudo.ReplaceAllString(out, "")
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
	if strings.Contains(rest, "</tool_call>") || strings.Contains(rest, "</tool>") {
		return s
	}
	restLower := lower[idx:]
	if strings.Contains(restLower, "_call") ||
		strings.Contains(restLower, "function=") ||
		strings.Contains(restLower, "<parameter") ||
		strings.Contains(restLower, "<function") ||
		strings.Contains(restLower, "<read_file") ||
		strings.Contains(restLower, "<path") {
		return strings.TrimSpace(s[:idx])
	}
	return s
}
