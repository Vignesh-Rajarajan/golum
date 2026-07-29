package llm

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/sashabaranov/go-openai"
)

// streamReasoning reports whether reasoning/thinking deltas should be extracted.
// When enabled, reasoning is emitted as EventTypeThinkingDelta (not content).
func streamReasoning() bool {
	return os.Getenv("GOLUM_STREAM_REASONING") == "1"
}

// parseStreamingChunk unmarshals one SSE JSON line and extracts assistant text,
// optional reasoning text, finish reason, usage, and tool-call fragments.
func parseStreamingChunk(raw []byte) (
	content string,
	thinking string,
	finishReason string,
	usage *openai.Usage,
	toolCalls []openai.ToolCall,
	hasChoices bool,
	err error,
) {
	var resp openai.ChatCompletionStreamResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", "", "", nil, nil, false, err
	}
	usage = resp.Usage
	if len(resp.Choices) == 0 {
		return "", "", "", usage, nil, false, nil
	}
	hasChoices = true
	ch := resp.Choices[0]
	finishReason = string(ch.FinishReason)
	toolCalls = ch.Delta.ToolCalls

	// Do not TrimSpace per chunk: streams often split on word boundaries, so a chunk may be
	// only a space or end with a trailing space; trimming destroys inter-word spacing.
	content = ch.Delta.Content
	if content == "" {
		content, thinking = deltaTextFromRawJSON(raw)
	} else if streamReasoning() {
		_, thinking = deltaTextFromRawJSON(raw)
	}
	return content, thinking, finishReason, usage, toolCalls, true, nil
}

func deltaTextFromRawJSON(line []byte) (content, thinking string) {
	var top map[string]interface{}
	if json.Unmarshal(line, &top) != nil {
		return "", ""
	}
	choices, ok := top["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return "", ""
	}
	ch0, ok := choices[0].(map[string]interface{})
	if !ok {
		return "", ""
	}
	deltaRaw, ok := ch0["delta"].(map[string]interface{})
	if !ok {
		return "", ""
	}
	return extractTextFromDeltaMap(deltaRaw)
}

// extractTextFromDeltaMap returns (content, thinking). Content is preferred for the
// assistant message; thinking is only populated when GOLUM_STREAM_REASONING=1 and
// is never mixed into content.
func extractTextFromDeltaMap(m map[string]interface{}) (content, thinking string) {
	if s, ok := m["content"].(string); ok && strings.TrimSpace(s) != "" {
		content = s
	}
	if streamReasoning() {
		for _, k := range []string{"reasoning_content", "reasoning", "thinking"} {
			if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
				thinking = s
				break
			}
		}
	}
	if content == "" {
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
				content = b.String()
			}
		}
	}
	return content, thinking
}
