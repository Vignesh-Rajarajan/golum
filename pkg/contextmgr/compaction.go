package contextmgr

import (
	"github.com/Vignesh-Rajarajan/golum/pkg/tokenizer"
	"github.com/sashabaranov/go-openai"
)

// CompactionThreshold is the fraction of the context window at which compaction
// should run.
const CompactionThreshold = 0.8

// FindValidCutPoints returns the indices at which msgs may be split such that
// msgs[:i] can be summarized away and msgs[i:] kept verbatim.
//
// A cut is valid only at the start of a user turn with no unresolved tool
// calls. Cutting anywhere else can strand an assistant message carrying
// tool_calls without its matching tool results (or vice versa), which the API
// rejects on the very next request. Note that msgs must NOT include the system
// prompt — the system prompt is re-prepended on every request and is never part
// of the compactable span.
func FindValidCutPoints(msgs []MessageItem) []int {
	var cuts []int
	pending := 0
	for i := range msgs {
		// Decide before consuming msgs[i]: everything before i is complete.
		if i > 0 && pending == 0 && msgs[i].Role == openai.ChatMessageRoleUser {
			cuts = append(cuts, i)
		}
		switch msgs[i].Role {
		case openai.ChatMessageRoleAssistant:
			pending += len(msgs[i].ToolCalls)
		case openai.ChatMessageRoleTool:
			if pending > 0 {
				pending--
			}
		}
	}
	return cuts
}

// ValidCutPoints returns the valid cut indices for the current history.
func (m *ContextManager) ValidCutPoints() []int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return FindValidCutPoints(m.messages)
}

// HasBalancedToolCalls reports whether every assistant tool call in the current
// history has a matching tool result. Compaction verifies this before letting a
// rebuilt context be sent: an unbalanced context is rejected by the API, and
// failing loudly here beats a 400 mid-turn.
func (m *ContextManager) HasBalancedToolCalls() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pending := map[string]bool{}
	for i := range m.messages {
		switch m.messages[i].Role {
		case openai.ChatMessageRoleAssistant:
			for _, tc := range m.messages[i].ToolCalls {
				pending[tc.ID] = true
			}
		case openai.ChatMessageRoleTool:
			id := m.messages[i].ToolCallID
			if !pending[id] {
				return false // result with no preceding call
			}
			delete(pending, id)
		}
	}
	return len(pending) == 0
}

// messageTokens returns the recorded (or freshly counted) token cost of one message.
func (m *ContextManager) messageTokens(i int) int {
	if m.messages[i].TokenCount != nil {
		return *m.messages[i].TokenCount
	}
	return tokenizer.CountTokens(m.messages[i].Content, m.modelName)
}

// systemPromptTokens counts the frozen system prompt once and caches it.
func (m *ContextManager) systemPromptTokens() int {
	if m.systemTokens == 0 && m.systemPrompt != "" {
		m.systemTokens = tokenizer.CountTokens(m.systemPrompt, m.modelName)
	}
	return m.systemTokens
}

// EstimateContextTokens approximates what the next request will cost, using the
// per-message counts already recorded by every Add* method plus the system
// prompt. Unlike NeedsCompression this is available *before* a request is sent,
// which is what lets the loop compact pre-emptively instead of after a failure.
func (m *ContextManager) EstimateContextTokens() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.estimateContextTokens()
}

func (m *ContextManager) estimateContextTokens() int {
	total := m.systemPromptTokens()
	for i := range m.messages {
		total += m.messageTokens(i)
	}
	return total
}

// TokensFrom returns the estimated token cost of messages[i:].
func (m *ContextManager) TokensFrom(i int) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tokensFrom(i)
}

func (m *ContextManager) tokensFrom(i int) int {
	if i < 0 {
		i = 0
	}
	total := 0
	for ; i < len(m.messages); i++ {
		total += m.messageTokens(i)
	}
	return total
}

// ContextLimit returns the configured context window.
func (m *ContextManager) ContextLimit() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.contextLimit()
}

func (m *ContextManager) contextLimit() int {
	if m.config != nil {
		return m.config.ContextWindowOrDefault()
	}
	return 128_000
}

// ContextUsageRatio reports how full the context is, taking the larger of the
// last reported usage and the local estimate. Usage is authoritative but only
// arrives after a response; the estimate covers everything appended since.
func (m *ContextManager) ContextUsageRatio() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	limit := m.contextLimit()
	if limit <= 0 {
		return 0
	}
	used := m.latestUsage.TotalTokens
	if est := m.estimateContextTokens(); est > used {
		used = est
	}
	return float64(used) / float64(limit)
}

// ShouldCompact reports whether the context has crossed the compaction
// threshold by either signal.
func (m *ContextManager) ShouldCompact() bool {
	return m.ContextUsageRatio() > CompactionThreshold
}

// ChooseCutPoint picks the valid cut that keeps as much recent history as fits
// within keepTokens. Returns -1 when there is nothing safe to compact.
func (m *ContextManager) ChooseCutPoint(keepTokens int) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cuts := FindValidCutPoints(m.messages)
	if len(cuts) == 0 {
		return -1
	}
	// cuts is ascending, so tail size shrinks as the index grows: the first cut
	// that fits the budget is the one preserving the most recent context.
	for _, c := range cuts {
		if m.tokensFrom(c) <= keepTokens {
			return c
		}
	}
	// Nothing fits; fall back to the cut that discards the most.
	return cuts[len(cuts)-1]
}

// MessagesBefore returns messages[:cut] as API messages, for summarization.
func (m *ContextManager) MessagesBefore(cut int) []openai.ChatCompletionMessage {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if cut <= 0 || cut > len(m.messages) {
		cut = len(m.messages)
	}
	out := make([]openai.ChatCompletionMessage, 0, cut)
	for i := 0; i < cut; i++ {
		out = append(out, m.messages[i].ToChatCompletionMessage())
	}
	return out
}

// ApplySummaryPreamble appends the two-message summary handoff used when
// history below a compaction boundary has been folded away. Unlike
// ReplaceWithSummary it does not clear existing messages — the caller rebuilds
// context in order, so the preamble lands first and surviving turns follow.
func (m *ContextManager) ApplySummaryPreamble(summary string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addUserMessage("# Context Restoration (Earlier Turns Compacted)\n\n" +
		"Earlier turns in this session were summarized to stay within the context limit. " +
		"Everything below the summary is the verbatim recent history.\n\n" +
		"**Actions listed as completed are already done. Do NOT repeat them.**\n\n" +
		"---\n\n" + summary)

	m.addAssistantMessage("Understood. I have the summary of earlier work and the recent "+
		"history below it. I will continue without repeating completed actions.", nil)
}

// LastSummary returns the most recent compaction summary embedded in history,
// or "" when the session has never been compacted.
func (m *ContextManager) LastSummary() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.messages {
		c := m.messages[i].Content
		if m.messages[i].Role == openai.ChatMessageRoleUser && isSummaryPreamble(c) {
			if idx := indexOfSummaryBody(c); idx >= 0 {
				return c[idx:]
			}
		}
	}
	return ""
}

func isSummaryPreamble(s string) bool {
	return len(s) > 0 && (hasPrefix(s, "# Context Restoration (Earlier Turns Compacted)") ||
		hasPrefix(s, "# Context Restoration (Previous Session Compacted)"))
}

func indexOfSummaryBody(s string) int {
	const sep = "---\n\n"
	i := lastIndex(s, sep)
	if i < 0 {
		return -1
	}
	return i + len(sep)
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

func lastIndex(s, sub string) int {
	for i := len(s) - len(sub); i >= 0; i-- {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
