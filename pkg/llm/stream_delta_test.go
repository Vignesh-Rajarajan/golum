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
	content, thinking := extractTextFromDeltaMap(m)
	if content != "answer" {
		t.Fatalf("got content %q want answer", content)
	}
	if thinking != "" {
		t.Fatalf("got thinking %q want empty when GOLUM_STREAM_REASONING unset", thinking)
	}
}

func TestExtractTextFromDeltaMap_reasoningIgnoredByDefault(t *testing.T) {
	t.Cleanup(func() { _ = os.Unsetenv("GOLUM_STREAM_REASONING") })
	_ = os.Unsetenv("GOLUM_STREAM_REASONING")

	m := map[string]interface{}{
		"reasoning_content": "only planning, no answer",
	}
	content, thinking := extractTextFromDeltaMap(m)
	if content != "" || thinking != "" {
		t.Fatalf("got content=%q thinking=%q want both empty when GOLUM_STREAM_REASONING unset", content, thinking)
	}
}

func TestExtractTextFromDeltaMap_reasoningWhenEnvSet(t *testing.T) {
	t.Cleanup(func() { _ = os.Unsetenv("GOLUM_STREAM_REASONING") })
	t.Setenv("GOLUM_STREAM_REASONING", "1")

	m := map[string]interface{}{
		"reasoning_content": "thinking text",
	}
	content, thinking := extractTextFromDeltaMap(m)
	if content != "" {
		t.Fatalf("got content %q want empty", content)
	}
	if thinking != "thinking text" {
		t.Fatalf("got thinking %q want thinking text", thinking)
	}
}

func TestExtractTextFromDeltaMap_contentAndThinkingSeparate(t *testing.T) {
	t.Cleanup(func() { _ = os.Unsetenv("GOLUM_STREAM_REASONING") })
	t.Setenv("GOLUM_STREAM_REASONING", "1")

	m := map[string]interface{}{
		"content":           "answer",
		"reasoning_content": "planning",
	}
	content, thinking := extractTextFromDeltaMap(m)
	if content != "answer" {
		t.Fatalf("got content %q want answer", content)
	}
	if thinking != "planning" {
		t.Fatalf("got thinking %q want planning", thinking)
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
	content, _ := extractTextFromDeltaMap(m)
	if content != "Hello world" {
		t.Fatalf("got %q want %q", content, "Hello world")
	}
}

func TestExtractTextFromDeltaMap_emptyWhenNoRecognizedFields(t *testing.T) {
	m := map[string]interface{}{"role": "assistant"}
	content, thinking := extractTextFromDeltaMap(m)
	if content != "" || thinking != "" {
		t.Fatalf("got content=%q thinking=%q want empty", content, thinking)
	}
}

func TestDeltaTextFromRawJSON_malformedJSON(t *testing.T) {
	content, thinking := deltaTextFromRawJSON([]byte("not json"))
	if content != "" || thinking != "" {
		t.Fatalf("got content=%q thinking=%q want empty on malformed JSON", content, thinking)
	}
}

func TestDeltaTextFromRawJSON_noChoices(t *testing.T) {
	content, thinking := deltaTextFromRawJSON([]byte(`{"choices":[]}`))
	if content != "" || thinking != "" {
		t.Fatalf("got content=%q thinking=%q want empty when choices is empty", content, thinking)
	}
}

func TestDeltaTextFromRawJSON_fallbackExtractsContent(t *testing.T) {
	raw := `{"choices":[{"delta":{"content":"fallback text"}}]}`
	content, _ := deltaTextFromRawJSON([]byte(raw))
	if content != "fallback text" {
		t.Fatalf("got %q want %q", content, "fallback text")
	}
}

func TestParseStreamingChunk_preservesInterWordSpacing(t *testing.T) {
	raw := `{"id":"1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":" world"}}]}`
	content, _, _, _, _, hasChoices, err := parseStreamingChunk([]byte(raw))
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
	_, _, finishReason, _, toolCalls, hasChoices, err := parseStreamingChunk([]byte(raw))
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
	_, _, _, _, _, hasChoices, err := parseStreamingChunk([]byte("{not valid json"))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if hasChoices {
		t.Fatal("expected hasChoices false on error")
	}
}

func TestParseStreamingChunk_noChoicesUsageOnly(t *testing.T) {
	raw := `{"id":"1","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}}`
	content, thinking, finishReason, usage, toolCalls, hasChoices, err := parseStreamingChunk([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasChoices {
		t.Fatal("expected hasChoices false for empty choices")
	}
	if content != "" || thinking != "" || finishReason != "" || toolCalls != nil {
		t.Fatalf("expected zero-value fields, got %q %q %q %v", content, thinking, finishReason, toolCalls)
	}
	if usage == nil || usage.TotalTokens != 12 {
		t.Fatalf("expected usage with total_tokens=12, got %+v", usage)
	}
}

func TestBuildToolCall_zeroArgEmptyMap(t *testing.T) {
	b := &toolCallBuilder{id: "c1", name: "todos"}
	tc := buildToolCall(b)
	if tc.Arguments == nil {
		t.Fatal("expected empty map, got nil")
	}
	if len(tc.Arguments) != 0 {
		t.Fatalf("expected empty map, got %+v", tc.Arguments)
	}
	if tc.ArgsErr != nil {
		t.Fatalf("unexpected ArgsErr: %v", tc.ArgsErr)
	}
}

func TestBuildToolCall_malformedJSON(t *testing.T) {
	b := &toolCallBuilder{id: "c1", name: "read_file"}
	b.args.WriteString(`{"path":`)
	tc := buildToolCall(b)
	if tc.ArgsErr == nil {
		t.Fatal("expected ArgsErr for malformed JSON")
	}
	if tc.ID != "c1" || tc.Name != "read_file" {
		t.Fatalf("must still emit id/name, got %+v", tc)
	}
	if tc.RawArguments != `{"path":` {
		t.Fatalf("RawArguments = %q", tc.RawArguments)
	}
}

func TestBuildToolCall_validArgs(t *testing.T) {
	b := &toolCallBuilder{id: "c1", name: "read_file"}
	b.args.WriteString(`{"path":"a.go"}`)
	tc := buildToolCall(b)
	if tc.ArgsErr != nil {
		t.Fatalf("unexpected ArgsErr: %v", tc.ArgsErr)
	}
	if tc.Arguments["path"] != "a.go" {
		t.Fatalf("path = %v", tc.Arguments["path"])
	}
}

func TestToolCall_ToOpenAI_usesRawArguments(t *testing.T) {
	tc := &ToolCall{
		ID:           "call_1",
		Name:         "read_file",
		RawArguments: `{"path":"a.go","offset":1}`,
		Arguments:    map[string]interface{}{"offset": 1.0, "path": "a.go"}, // different key order
	}
	oa := tc.ToOpenAI()
	if oa.Function.Arguments != `{"path":"a.go","offset":1}` {
		t.Fatalf("ToOpenAI must use RawArguments, got %q", oa.Function.Arguments)
	}
}
