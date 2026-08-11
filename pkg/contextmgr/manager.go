package contextmgr

import (
	"strings"
	"sync"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/Vignesh-Rajarajan/golum/pkg/tokenizer"
	"github.com/sashabaranov/go-openai"
)

const (
	PruneProtectTokens  = 40_000
	PruneMinimumTokens  = 20_000
	oldToolResultMarker = "[Old tool result content cleared]"
)

// ContextManager holds system prompt, rolling messages, and usage accounting.
type ContextManager struct {
	mu sync.RWMutex

	systemPrompt string
	config       *config.Config
	modelName    string
	messages     []MessageItem

	latestUsage TokenUsage
	// totalUsage accumulates usage across requests (Python: total_usage).
	totalUsage TokenUsage

	// systemTokens caches the frozen system prompt's token count.
	systemTokens int
}

// NewContextManager builds a manager with a frozen system prompt from prompt.GetSystemPrompt.
func NewContextManager(
	cfg *config.Config,
	promptCfg prompt.PromptConfig,
	userMemory *string,
	tools []llm.Tool,
) *ContextManager {
	sys := prompt.GetSystemPrompt(promptCfg, userMemory, tools)
	model := ""
	if cfg != nil {
		model = cfg.Model
	}
	if model == "" {
		model = tokenizer.DefaultModel
	}
	return &ContextManager{
		systemPrompt: sys,
		config:       cfg,
		modelName:    model,
		messages:     nil,
	}
}

// MessageCount returns the number of stored messages (excluding system).
func (m *ContextManager) MessageCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.messages)
}

// AddUserMessage appends a user message with token count.
func (m *ContextManager) AddUserMessage(content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addUserMessage(content)
}

func (m *ContextManager) addUserMessage(content string) {
	n := tokenizer.CountTokens(content, m.modelName)
	m.messages = append(m.messages, MessageItem{
		Role:       openai.ChatMessageRoleUser,
		Content:    content,
		TokenCount: intPtr(n),
	})
}

// AddAssistantMessage appends an assistant message; toolCalls may be nil.
func (m *ContextManager) AddAssistantMessage(content string, toolCalls []openai.ToolCall) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addAssistantMessage(content, toolCalls)
}

func (m *ContextManager) addAssistantMessage(content string, toolCalls []openai.ToolCall) {
	n := tokenizer.CountTokens(content, m.modelName)
	m.messages = append(m.messages, MessageItem{
		Role:       openai.ChatMessageRoleAssistant,
		Content:    content,
		ToolCalls:  toolCalls,
		TokenCount: intPtr(n),
	})
}

// AddToolResult appends a tool role message with tool_call_id.
func (m *ContextManager) AddToolResult(toolCallID, content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := tokenizer.CountTokens(content, m.modelName)
	m.messages = append(m.messages, MessageItem{
		Role:       openai.ChatMessageRoleTool,
		Content:    content,
		ToolCallID: toolCallID,
		TokenCount: intPtr(n),
	})
}

// AddSystemNotice appends a system-role interstitial (e.g. loop-breaker notice).
func (m *ContextManager) AddSystemNotice(content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := tokenizer.CountTokens(content, m.modelName)
	m.messages = append(m.messages, MessageItem{
		Role:       openai.ChatMessageRoleSystem,
		Content:    content,
		TokenCount: intPtr(n),
	})
}

// GetMessages returns system (if any) plus each message as map[string]any (Python get_messages).
func (m *ContextManager) GetMessages() []map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]map[string]any, 0, 1+len(m.messages))
	if strings.TrimSpace(m.systemPrompt) != "" {
		out = append(out, map[string]any{
			"role":    openai.ChatMessageRoleSystem,
			"content": m.systemPrompt,
		})
	}
	for i := range m.messages {
		out = append(out, m.messages[i].ToMap())
	}
	return out
}

// ChatCompletionMessages returns messages in go-openai form, including system when set.
func (m *ContextManager) ChatCompletionMessages() []openai.ChatCompletionMessage {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]openai.ChatCompletionMessage, 0, 1+len(m.messages))
	if strings.TrimSpace(m.systemPrompt) != "" {
		out = append(out, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: m.systemPrompt,
		})
	}
	for i := range m.messages {
		out = append(out, m.messages[i].ToChatCompletionMessage())
	}
	return out
}

// NeedsCompression is true when the last response usage exceeds 80% of context window.
func (m *ContextManager) NeedsCompression() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var limit int
	if m.config != nil {
		limit = m.config.ContextWindowOrDefault()
	} else {
		limit = 128_000
	}
	if limit <= 0 {
		return false
	}
	return float64(m.latestUsage.TotalTokens) > 0.8*float64(limit)
}

// SetLatestUsage stores usage from the most recent API response.
func (m *ContextManager) SetLatestUsage(u TokenUsage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latestUsage = u
}

// AddUsage accumulates into TotalUsage.
func (m *ContextManager) AddUsage(u TokenUsage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.totalUsage.Add(u)
}

// TotalUsage returns a snapshot of cumulative request usage.
func (m *ContextManager) TotalUsage() TokenUsage {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.totalUsage
}

// ReplaceWithSummary clears history and injects summary + ack + continue user messages.
func (m *ContextManager) ReplaceWithSummary(summary string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = nil

	continuationContent := "# Context Restoration (Previous Session Compacted)\n\n" +
		"The previous conversation was compacted due to context length limits. Below is a detailed summary of the work done so far.\n\n" +
		"**CRITICAL: Actions listed under \"COMPLETED ACTIONS\" are already done. DO NOT repeat them.**\n\n" +
		"---\n\n" +
		summary + "\n\n" +
		"---\n\n" +
		"Resume work from where we left off. Focus ONLY on the remaining tasks."

	m.addUserMessage(continuationContent)

	ackContent := "I've reviewed the context from the previous session. I understand:\n" +
		"- The original goal and what was requested\n" +
		"- Which actions are ALREADY COMPLETED (I will NOT repeat these)\n" +
		"- The current state of the project\n" +
		"- What still needs to be done\n\n" +
		"I'll continue with the REMAINING tasks only, starting from where we left off."

	m.addAssistantMessage(ackContent, nil)

	continueContent := "Continue with the REMAINING work only. Do NOT repeat any completed actions. " +
		"Proceed with the next step as described in the context above."

	m.addUserMessage(continueContent)
}

// PruneToolOutputs clears old tool message bodies when enough tokens can be reclaimed.
// Returns the number of tool messages pruned.
func (m *ContextManager) PruneToolOutputs() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if countRole(m.messages, openai.ChatMessageRoleUser) < 2 {
		return 0
	}

	totalTokens := 0
	prunedTokens := 0
	var toPruneIdx []int

	for i := len(m.messages) - 1; i >= 0; i-- {
		msg := &m.messages[i]
		if msg.Role != openai.ChatMessageRoleTool || msg.ToolCallID == "" {
			continue
		}
		if msg.PrunedAt != nil {
			break
		}
		tokens := 0
		if msg.TokenCount != nil {
			tokens = *msg.TokenCount
		} else {
			tokens = tokenizer.CountTokens(msg.Content, m.modelName)
		}
		totalTokens += tokens
		if totalTokens > PruneProtectTokens {
			prunedTokens += tokens
			toPruneIdx = append(toPruneIdx, i)
		}
	}

	if prunedTokens < PruneMinimumTokens {
		return 0
	}

	now := time.Now()
	n := 0
	for _, i := range toPruneIdx {
		msg := &m.messages[i]
		msg.Content = oldToolResultMarker
		tc := tokenizer.CountTokens(msg.Content, m.modelName)
		msg.TokenCount = intPtr(tc)
		t := now
		msg.PrunedAt = &t
		n++
	}
	return n
}

// Clear removes all non-system messages (same as Python clear).
func (m *ContextManager) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = nil
}

func countRole(messages []MessageItem, role string) int {
	n := 0
	for i := range messages {
		if messages[i].Role == role {
			n++
		}
	}
	return n
}
