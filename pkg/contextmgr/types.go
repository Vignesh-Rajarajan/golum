package contextmgr

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/sashabaranov/go-openai"
)

// TokenUsage mirrors API usage totals (prompt + completion + total).
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Add accumulates usage into u (Python: total_usage += usage).
func (u *TokenUsage) Add(o TokenUsage) {
	u.PromptTokens += o.PromptTokens
	u.CompletionTokens += o.CompletionTokens
	u.TotalTokens += o.TotalTokens
}

// TokenUsageFromOpenAI maps OpenAI usage to TokenUsage.
func TokenUsageFromOpenAI(u *openai.Usage) TokenUsage {
	if u == nil {
		return TokenUsage{}
	}
	return TokenUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
}

// TokenUsageFromMeta parses prompt/completion/total from stream Meta (string map).
func TokenUsageFromMeta(meta map[string]string) TokenUsage {
	if meta == nil {
		return TokenUsage{}
	}
	var u TokenUsage
	if v, ok := meta["prompt_tokens"]; ok {
		u.PromptTokens, _ = strconv.Atoi(v)
	}
	if v, ok := meta["completion_tokens"]; ok {
		u.CompletionTokens, _ = strconv.Atoi(v)
	}
	if v, ok := meta["total_tokens"]; ok {
		u.TotalTokens, _ = strconv.Atoi(v)
	}
	return u
}

// MessageItem is one chat turn (user, assistant, or tool), with optional metadata.
type MessageItem struct {
	Role       string
	Content    string
	ToolCallID string
	ToolCalls  []openai.ToolCall
	TokenCount *int
	PrunedAt   *time.Time
}

// ToMap returns a dict-shaped map matching the Python MessageItem.to_dict() shape.
func (m *MessageItem) ToMap() map[string]any {
	result := map[string]any{"role": m.Role}
	if m.ToolCallID != "" {
		result["tool_call_id"] = m.ToolCallID
	}
	if len(m.ToolCalls) > 0 {
		raw, _ := json.Marshal(m.ToolCalls)
		var arr []any
		_ = json.Unmarshal(raw, &arr)
		result["tool_calls"] = arr
	}
	if m.Content != "" {
		result["content"] = m.Content
	}
	return result
}

// ToChatCompletionMessage converts to the go-openai request type.
func (m *MessageItem) ToChatCompletionMessage() openai.ChatCompletionMessage {
	toolCalls := append([]openai.ToolCall(nil), m.ToolCalls...)
	return openai.ChatCompletionMessage{
		Role:       m.Role,
		Content:    m.Content,
		ToolCalls:  toolCalls,
		ToolCallID: m.ToolCallID,
	}
}

func intPtr(n int) *int { return &n }
