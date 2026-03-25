package llm

import (
	"encoding/json"
	"strings"

	"github.com/sashabaranov/go-openai"
)

// parseStreamingChunk unmarshals one SSE JSON line and extracts assistant text.
// Some providers (e.g. GLM via OpenRouter) put visible text in reasoning_content or
// other fields not mapped to Delta.Content in go-openai, which would otherwise yield
// an empty reply in the TUI.
func parseStreamingChunk(raw []byte) (
	content string,
	finishReason string,
	usage *openai.Usage,
	toolCalls []openai.ToolCall,
	hasChoices bool,
	err error,
) {
	var resp openai.ChatCompletionStreamResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", "", nil, nil, false, err
	}
	usage = resp.Usage
	if len(resp.Choices) == 0 {
		return "", "", usage, nil, false, nil
	}
	hasChoices = true
	ch := resp.Choices[0]
	finishReason = string(ch.FinishReason)
	toolCalls = ch.Delta.ToolCalls

	content = strings.TrimSpace(ch.Delta.Content)
	if content == "" {
		content = deltaTextFromRawJSON(raw)
	}
	return content, finishReason, usage, toolCalls, true, nil
}

func deltaTextFromRawJSON(line []byte) string {
	var top map[string]interface{}
	if json.Unmarshal(line, &top) != nil {
		return ""
	}
	choices, ok := top["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return ""
	}
	ch0, ok := choices[0].(map[string]interface{})
	if !ok {
		return ""
	}
	deltaRaw, ok := ch0["delta"].(map[string]interface{})
	if !ok {
		return ""
	}
	return extractTextFromDeltaMap(deltaRaw)
}

func extractTextFromDeltaMap(m map[string]interface{}) string {
	if s, ok := m["content"].(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	for _, k := range []string{"reasoning_content", "reasoning", "thinking"} {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	if arr, ok := m["content"].([]interface{}); ok {
		var b strings.Builder
		for _, item := range arr {
			part, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			typ, _ := part["type"].(string)
			if typ == "text" || typ == "" {
				if t, ok := part["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	return ""
}
