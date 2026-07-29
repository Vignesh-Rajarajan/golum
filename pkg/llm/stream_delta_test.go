package llm

import (
	"os"
	"testing"
)

func TestExtractTextFromDeltaMap_contentPreferred(t *testing.T) {
	m := map[string]interface{}{
		"content":           "answer",
		"reasoning_content": "planning",
	}
	if got := extractTextFromDeltaMap(m); got != "answer" {
		t.Fatalf("got %q want answer", got)
	}
}

func TestExtractTextFromDeltaMap_reasoningIgnoredByDefault(t *testing.T) {
	t.Cleanup(func() { _ = os.Unsetenv("GOLUM_STREAM_REASONING") })
	_ = os.Unsetenv("GOLUM_STREAM_REASONING")

	m := map[string]interface{}{
		"reasoning_content": "only planning, no answer",
	}
	if got := extractTextFromDeltaMap(m); got != "" {
		t.Fatalf("got %q want empty when GOLUM_STREAM_REASONING unset", got)
	}
}

func TestExtractTextFromDeltaMap_reasoningWhenEnvSet(t *testing.T) {
	t.Cleanup(func() { _ = os.Unsetenv("GOLUM_STREAM_REASONING") })
	t.Setenv("GOLUM_STREAM_REASONING", "1")

	m := map[string]interface{}{
		"reasoning_content": "thinking text",
	}
	if got := extractTextFromDeltaMap(m); got != "thinking text" {
		t.Fatalf("got %q want thinking text", got)
	}
}

func TestExtractTextFromDeltaMap_contentArrayParts(t *testing.T) {
	m := map[string]interface{}{
		"content": []interface{}{
			map[string]interface{}{"type": "text", "text": "Hello "},
			map[string]interface{}{"type": "text", "text": "world"},
			map[string]interface{}{"type": "image", "text": "ignored"},
		},
	}
	if got := extractTextFromDeltaMap(m); got != "Hello world" {
		t.Fatalf("got %q want %q", got, "Hello world")
	}
}

func TestExtractTextFromDeltaMap_emptyWhenNoRecognizedFields(t *testing.T) {
	m := map[string]interface{}{"role": "assistant"}
	if got := extractTextFromDeltaMap(m); got != "" {
		t.Fatalf("got %q want empty", got)
	}
}

func TestDeltaTextFromRawJSON_malformedJSON(t *testing.T) {
	if got := deltaTextFromRawJSON([]byte("not json")); got != "" {
		t.Fatalf("got %q want empty on malformed JSON", got)
	}
}

func TestDeltaTextFromRawJSON_noChoices(t *testing.T) {
	if got := deltaTextFromRawJSON([]byte(`{"choices":[]}`)); got != "" {
		t.Fatalf("got %q want empty when choices is empty", got)
	}
}

func TestDeltaTextFromRawJSON_fallbackExtractsContent(t *testing.T) {
	raw := `{"choices":[{"delta":{"content":"fallback text"}}]}`
	if got := deltaTextFromRawJSON([]byte(raw)); got != "fallback text" {
		t.Fatalf("got %q want %q", got, "fallback text")
	}
}

func TestParseStreamingChunk_preservesInterWordSpacing(t *testing.T) {
	// Regression: content must not be TrimSpace'd per-chunk, or a chunk that is
	// only a leading/trailing space (a common word-boundary split) would collapse
	// "Hello" + " " + "world" into "Helloworld".
	raw := `{"id":"1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":" world"}}]}`
	content, _, _, _, hasChoices, err := parseStreamingChunk([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasChoices {
		t.Fatal("expected hasChoices true")
	}
	if content != " world" {
		t.Fatalf("got %q want %q (leading space must be preserved)", content, " world")
	}
}

func TestParseStreamingChunk_toolCallsAndFinishReason(t *testing.T) {
	raw := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]},"finish_reason":"tool_calls"}]}`
	_, finishReason, _, toolCalls, hasChoices, err := parseStreamingChunk([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasChoices {
		t.Fatal("expected hasChoices true")
	}
	if finishReason != "tool_calls" {
		t.Fatalf("got finishReason %q want tool_calls", finishReason)
	}
	if len(toolCalls) != 1 || toolCalls[0].Function.Name != "read_file" {
		t.Fatalf("expected one read_file tool call, got %+v", toolCalls)
	}
}

func TestParseStreamingChunk_malformedJSON(t *testing.T) {
	_, _, _, _, hasChoices, err := parseStreamingChunk([]byte("{not valid json"))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if hasChoices {
		t.Fatal("expected hasChoices false on error")
	}
}

func TestParseStreamingChunk_noChoicesUsageOnly(t *testing.T) {
	raw := `{"id":"1","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}}`
	content, finishReason, usage, toolCalls, hasChoices, err := parseStreamingChunk([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasChoices {
		t.Fatal("expected hasChoices false for empty choices")
	}
	if content != "" || finishReason != "" || toolCalls != nil {
		t.Fatalf("expected zero-value content/finishReason/toolCalls, got %q %q %v", content, finishReason, toolCalls)
	}
	if usage == nil || usage.TotalTokens != 12 {
		t.Fatalf("expected usage with total_tokens=12, got %+v", usage)
	}
}
