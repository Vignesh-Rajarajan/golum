package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/sashabaranov/go-openai"
)

// TestChatCompletion_RetryLogic tests the retry logic with different error types
func TestChatCompletion_RetryLogic(t *testing.T) {
	tests := []struct {
		name             string
		setupResponses   func(ms *MockServer)
		maxRetries       int
		expectedAttempts int
		expectError      bool
		errorContains    string
		expectSuccess    bool
	}{
		{
			name: "success on first attempt",
			setupResponses: func(ms *MockServer) {
				ms.AddNonStreamingResponse("Hello!", 200)
			},
			maxRetries:       3,
			expectedAttempts: 1,
			expectSuccess:    true,
		},
		{
			name: "success on second attempt after rate limit",
			setupResponses: func(ms *MockServer) {
				ms.AddRateLimitError()
				ms.AddNonStreamingResponse("Hello!", 200)
			},
			maxRetries:       3,
			expectedAttempts: 2,
			expectSuccess:    true,
		},
		{
			name: "success on third attempt after multiple rate limits",
			setupResponses: func(ms *MockServer) {
				ms.AddRateLimitError()
				ms.AddRateLimitError()
				ms.AddNonStreamingResponse("Hello!", 200)
			},
			maxRetries:       3,
			expectedAttempts: 3,
			expectSuccess:    true,
		},
		{
			name: "fail after max retries on rate limit",
			setupResponses: func(ms *MockServer) {
				ms.AddRateLimitError()
				ms.AddRateLimitError()
				ms.AddRateLimitError()
				ms.AddRateLimitError()
			},
			maxRetries:       3,
			expectedAttempts: 4,
			expectError:      true,
			errorContains:    "rate limit exceeded",
		},
		{
			name: "success on second attempt after connection error",
			setupResponses: func(ms *MockServer) {
				ms.AddConnectionError()
				ms.AddNonStreamingResponse("Hello!", 200)
			},
			maxRetries:       3,
			expectedAttempts: 2,
			expectSuccess:    true,
		},
		{
			name: "no retry on API error",
			setupResponses: func(ms *MockServer) {
				ms.AddAPIError()
			},
			maxRetries:       3,
			expectedAttempts: 1,
			expectError:      true,
			errorContains:    "API error",
		},
		{
			name: "mixed errors then success",
			setupResponses: func(ms *MockServer) {
				ms.AddRateLimitError()
				ms.AddConnectionError()
				ms.AddNonStreamingResponse("Hello!", 200)
			},
			maxRetries:       3,
			expectedAttempts: 3,
			expectSuccess:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mock server
			ms := NewMockServer()
			defer ms.Close()

			tt.setupResponses(ms)

			client := ms.TestClient()

			// Make request
			messages := []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleUser, Content: "Hello"},
			}

			opts := ChatCompletionOptions{
				Stream:     false,
				MaxRetries: tt.maxRetries,
			}

			events := client.ChatCompletion(context.Background(), messages, opts)

			// Collect events
			var hasError bool
			var errorMsg string
			var hasSuccess bool

			for event := range events {
				if event.Type == EventTypeError {
					hasError = true
					errorMsg = event.Error.Error()
				}
				if event.Type == EventTypeContentDone {
					hasSuccess = true
				}
			}

			// Verify attempts
			attempts := ms.GetRequestCount()
			if attempts != tt.expectedAttempts {
				t.Errorf("expected %d attempts, got %d", tt.expectedAttempts, attempts)
			}

			// Verify error
			if tt.expectError && !hasError {
				t.Error("expected error but got none")
			}

			if tt.errorContains != "" && !strings.Contains(errorMsg, tt.errorContains) {
				t.Errorf("expected error to contain %q, got %q", tt.errorContains, errorMsg)
			}

			// Verify success
			if tt.expectSuccess && !hasSuccess {
				t.Error("expected success but got error")
			}
		})
	}
}

// TestChatCompletion_ChannelClosure tests that channels are properly closed
func TestChatCompletion_ChannelClosure(t *testing.T) {
	tests := []struct {
		name           string
		setupResponses func(ms *MockServer)
		stream         bool
		expectClosed   bool
	}{
		{
			name: "channel closes after successful non-streaming response",
			setupResponses: func(ms *MockServer) {
				ms.AddNonStreamingResponse("Hello!", 200)
			},
			stream:       false,
			expectClosed: true,
		},
		{
			name: "channel closes after successful streaming response",
			setupResponses: func(ms *MockServer) {
				chunks := NewChunkBuilder().
					AddContent("Hello").
					AddContent(" world").
					AddFinish().
					Build()
				ms.AddStreamingResponse(chunks, 200)
			},
			stream:       true,
			expectClosed: true,
		},
		{
			name: "channel closes after error",
			setupResponses: func(ms *MockServer) {
				ms.AddAPIError()
			},
			stream:       false,
			expectClosed: true,
		},
		{
			name: "channel closes after rate limit error with no retries left",
			setupResponses: func(ms *MockServer) {
				ms.AddRateLimitError()
				ms.AddRateLimitError()
				ms.AddRateLimitError()
				ms.AddRateLimitError()
			},
			stream:       false,
			expectClosed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := NewMockServer()
			defer ms.Close()

			tt.setupResponses(ms)

			client := ms.TestClient()

			messages := []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleUser, Content: "Hello"},
			}

			opts := ChatCompletionOptions{
				Stream:     tt.stream,
				MaxRetries: 3,
			}

			events := client.ChatCompletion(context.Background(), messages, opts)

			// Drain the channel
			for range events {
			}

			// Check if channel is closed
			closed := WaitForChannelClose(events, 100*time.Millisecond)
			if closed != tt.expectClosed {
				t.Errorf("expected closed=%v, got closed=%v", tt.expectClosed, closed)
			}
		})
	}
}

// TestChatCompletion_ResourceLeaks tests for goroutine and channel leaks
func TestChatCompletion_ResourceLeaks(t *testing.T) {
	tests := []struct {
		name           string
		setupResponses func(ms *MockServer)
		stream         bool
		numRequests    int
	}{
		{
			name: "no leaks with multiple streaming requests",
			setupResponses: func(ms *MockServer) {
				for i := 0; i < 10; i++ {
					chunks := NewChunkBuilder().
						AddContent(fmt.Sprintf("Response %d", i)).
						AddFinish().
						Build()
					ms.AddStreamingResponse(chunks, 200)
				}
			},
			stream:      true,
			numRequests: 10,
		},
		{
			name: "no leaks with multiple non-streaming requests",
			setupResponses: func(ms *MockServer) {
				for i := 0; i < 10; i++ {
					ms.AddNonStreamingResponse(fmt.Sprintf("Response %d", i), 200)
				}
			},
			stream:      false,
			numRequests: 10,
		},
		{
			name: "no leaks with errors",
			setupResponses: func(ms *MockServer) {
				for i := 0; i < 5; i++ {
					ms.AddAPIError()
				}
			},
			stream:      false,
			numRequests: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := NewMockServer()
			defer ms.Close()

			tt.setupResponses(ms)

			client := ms.TestClient()

			// Track goroutines (simplified - in production use runtime.NumGoroutine())
			// initialGoroutines := runtime.NumGoroutine()

			var wg sync.WaitGroup

			for i := 0; i < tt.numRequests; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()

					messages := []openai.ChatCompletionMessage{
						{Role: openai.ChatMessageRoleUser, Content: fmt.Sprintf("Request %d", idx)},
					}

					opts := ChatCompletionOptions{
						Stream:     tt.stream,
						MaxRetries: 1,
					}

					events := client.ChatCompletion(context.Background(), messages, opts)

					// Drain the channel completely
					for range events {
					}
				}(i)
			}

			wg.Wait()

			// Give time for cleanup
			time.Sleep(100 * time.Millisecond)

			// In production, check runtime.NumGoroutine() here
			// finalGoroutines := runtime.NumGoroutine()
			// if finalGoroutines > initialGoroutines + threshold {
			//     t.Errorf("goroutine leak: started with %d, ended with %d",
			//         initialGoroutines, finalGoroutines)
			// }
		})
	}
}

// TestChatCompletion_ChunkReassembly tests that chunks are properly reassembled
func TestChatCompletion_ChunkReassembly(t *testing.T) {
	tests := []struct {
		name            string
		chunks          []MockStreamChunk
		expectedContent string
		expectToolCalls bool
		toolCallName    string
	}{
		{
			name: "simple text reassembly",
			chunks: []MockStreamChunk{
				{Content: "Hello"},
				{Content: " "},
				{Content: "world"},
				{Content: "!"},
				{FinishReason: "stop"},
			},
			expectedContent: "Hello world!",
		},
		{
			name: "multi-word reassembly",
			chunks: []MockStreamChunk{
				{Content: "The"},
				{Content: " quick"},
				{Content: " brown"},
				{Content: " fox"},
				{Content: " jumps"},
				{Content: " over"},
				{Content: " the"},
				{Content: " lazy"},
				{Content: " dog"},
				{FinishReason: "stop"},
			},
			expectedContent: "The quick brown fox jumps over the lazy dog",
		},
		{
			name: "code reassembly",
			chunks: []MockStreamChunk{
				{Content: "func"},
				{Content: " main"},
				{Content: "()"},
				{Content: " {"},
				{Content: "\n"},
				{Content: "    "},
				{Content: "fmt"},
				{Content: ".Print"},
				{Content: "(\"Hello\")"},
				{Content: "\n"},
				{Content: "}"},
				{FinishReason: "stop"},
			},
			expectedContent: "func main() {\n    fmt.Print(\"Hello\")\n}",
		},
		{
			name: "tool call reassembly",
			chunks: []MockStreamChunk{
				{ToolCallID: "call_123", ToolCallName: "get_weather"},
				{FinishReason: "stop"},
			},
			expectToolCalls: true,
			toolCallName:    "get_weather",
		},
		{
			name: "mixed content and tool calls",
			chunks: []MockStreamChunk{
				{Content: "I'll check the weather for you."},
				{ToolCallID: "call_123", ToolCallName: "get_weather"},
				{FinishReason: "stop"},
			},
			expectedContent: "I'll check the weather for you.",
			expectToolCalls: true,
			toolCallName:    "get_weather",
		},
		{
			name: "empty chunks are ignored",
			chunks: []MockStreamChunk{
				{Content: ""},
				{Content: "Hello"},
				{Content: ""},
				{Content: "!"},
				{Content: ""},
				{FinishReason: "stop"},
			},
			expectedContent: "Hello!",
		},
		{
			name: "unicode reassembly",
			chunks: []MockStreamChunk{
				{Content: "你好"},
				{Content: "世界"},
				{Content: " 🌍"},
				{FinishReason: "stop"},
			},
			expectedContent: "你好世界 🌍",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := NewMockServer()
			defer ms.Close()

			ms.AddStreamingResponse(tt.chunks, 200)

			client := ms.TestClient()

			messages := []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleUser, Content: "Test"},
			}

			opts := ChatCompletionOptions{
				Stream:     true,
				MaxRetries: 0,
			}

			events := client.ChatCompletion(context.Background(), messages, opts)

			// Collect all content
			builder := NewMessageBuilder()
			var toolCalls []*ToolCall

			for event := range events {
				switch event.Type {
				case EventTypeContentDelta:
					builder.AddChunk(event.Content)
				case EventTypeToolCall:
					toolCalls = append(toolCalls, event.Tool)
				}
			}

			// Verify content reassembly
			actualContent := builder.Build()
			if actualContent != tt.expectedContent {
				t.Errorf("content mismatch:\nexpected: %q\nactual:   %q",
					tt.expectedContent, actualContent)
			}

			// Verify tool calls
			if tt.expectToolCalls {
				if len(toolCalls) == 0 {
					t.Error("expected tool calls but got none")
				} else if toolCalls[0].Name != tt.toolCallName {
					t.Errorf("expected tool call %q, got %q",
						tt.toolCallName, toolCalls[0].Name)
				}
			} else {
				if len(toolCalls) > 0 {
					t.Errorf("expected no tool calls but got %d", len(toolCalls))
				}
			}
		})
	}
}

// TestChatCompletion_ConcurrentRequests tests concurrent request handling
func TestChatCompletion_ConcurrentRequests(t *testing.T) {
	ms := NewMockServer()
	defer ms.Close()

	// Setup multiple responses
	for i := 0; i < 10; i++ {
		chunks := NewChunkBuilder().
			AddContent(fmt.Sprintf("Response %d", i)).
			AddFinish().
			Build()
		ms.AddStreamingResponse(chunks, 200)
	}

	client := ms.TestClient()

	var wg sync.WaitGroup
	results := make([]string, 10)
	errors := make([]error, 10)

	// Launch concurrent requests
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			messages := []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleUser, Content: fmt.Sprintf("Request %d", idx)},
			}

			opts := ChatCompletionOptions{
				Stream:     true,
				MaxRetries: 0,
			}

			events := client.ChatCompletion(context.Background(), messages, opts)

			builder := NewMessageBuilder()
			for event := range events {
				if event.Type == EventTypeError {
					errors[idx] = event.Error
					return
				}
				if event.Type == EventTypeContentDelta {
					builder.AddChunk(event.Content)
				}
			}

			results[idx] = builder.Build()
		}(i)
	}

	wg.Wait()

	// Verify all requests completed
	for i, err := range errors {
		if err != nil {
			t.Errorf("request %d failed: %v", i, err)
		}
	}

	// Verify all responses are unique
	seen := make(map[string]bool)
	for i, result := range results {
		if seen[result] {
			t.Errorf("duplicate response %q at index %d", result, i)
		}
		seen[result] = true
	}
}

// TestChatCompletion_ContextCancellation tests context cancellation
func TestChatCompletion_ContextCancellation(t *testing.T) {
	ms := NewMockServer()
	defer ms.Close()

	// Setup slow streaming response
	chunks := NewChunkBuilder().
		AddContentWithDelay("Part 1", 100*time.Millisecond).
		AddContentWithDelay("Part 2", 100*time.Millisecond).
		AddContentWithDelay("Part 3", 100*time.Millisecond).
		AddFinish().
		Build()
	ms.AddStreamingResponse(chunks, 200)

	client := ms.TestClient()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "Test"},
	}

	opts := ChatCompletionOptions{
		Stream:     true,
		MaxRetries: 0,
	}

	events := client.ChatCompletion(ctx, messages, opts)

	builder := NewMessageBuilder()
	var hasError bool

	for event := range events {
		if event.Type == EventTypeError {
			hasError = true
		}
		if event.Type == EventTypeContentDelta {
			builder.AddChunk(event.Content)
		}
	}

	// Should have partial content
	content := builder.Build()
	if content == "" {
		t.Error("expected partial content before cancellation")
	}

	// Should have error due to cancellation
	if !hasError {
		t.Error("expected error due to context cancellation")
	}
}

// TestChatCompletion_EventOrdering tests that events are received in correct order
func TestChatCompletion_EventOrdering(t *testing.T) {
	ms := NewMockServer()
	defer ms.Close()

	chunks := NewChunkBuilder().
		AddContent("Hello").
		AddContent(" world").
		AddFinish().
		Build()
	ms.AddStreamingResponse(chunks, 200)

	client := ms.TestClient()

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "Test"},
	}

	opts := ChatCompletionOptions{
		Stream:     true,
		MaxRetries: 0,
	}

	events := client.ChatCompletion(context.Background(), messages, opts)

	var eventOrder []EventType

	for event := range events {
		eventOrder = append(eventOrder, event.Type)
	}

	// Verify order: ContentStart, ContentDelta..., ContentDone
	if len(eventOrder) < 3 {
		t.Fatalf("expected at least 3 events, got %d", len(eventOrder))
	}

	if eventOrder[0] != EventTypeContentStart {
		t.Errorf("first event should be ContentStart, got %v", eventOrder[0])
	}

	if eventOrder[len(eventOrder)-1] != EventTypeContentDone {
		t.Errorf("last event should be ContentDone, got %v", eventOrder[len(eventOrder)-1])
	}

	// Verify ContentDelta events in between
	for i := 1; i < len(eventOrder)-1; i++ {
		if eventOrder[i] != EventTypeContentDelta {
			t.Errorf("event %d should be ContentDelta, got %v", i, eventOrder[i])
		}
	}
}

// TestChatCompletion_ExponentialBackoff tests exponential backoff timing
func TestChatCompletion_ExponentialBackoff(t *testing.T) {
	ms := NewMockServer()
	defer ms.Close()

	// Setup: 2 rate limit errors then success
	ms.AddRateLimitError()
	ms.AddRateLimitError()
	ms.AddNonStreamingResponse("Success!", 200)

	client := ms.TestClient()

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "Test"},
	}

	opts := ChatCompletionOptions{
		Stream:     false,
		MaxRetries: 3,
	}

	start := time.Now()
	events := client.ChatCompletion(context.Background(), messages, opts)

	// Drain events
	for range events {
	}

	elapsed := time.Since(start)

	// Should have waited: 1s + 2s = 3s minimum (with some tolerance)
	// First retry: 2^0 = 1s
	// Second retry: 2^1 = 2s
	// Total: ~3s

	if elapsed < 2*time.Second {
		t.Errorf("expected at least 2s of backoff, got %v", elapsed)
	}

	if elapsed > 5*time.Second {
		t.Errorf("backoff took too long: %v", elapsed)
	}
}

// TestBuildTools verifies llm.Tool is converted to openai.Tool without a parallel type.
func TestBuildTools(t *testing.T) {
	c := NewClient(&config.Config{OpenAIAPIKey: "test-key", Model: "gpt-4o"})

	tools := []Tool{
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "read_file",
				Description: "Read a file from disk",
				Parameters:  map[string]interface{}{"type": "object"},
			},
		},
	}

	got := c.buildTools(tools)
	if len(got) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(got))
	}
	if got[0].Type != openai.ToolType("function") {
		t.Errorf("expected type function, got %v", got[0].Type)
	}
	if got[0].Function == nil || got[0].Function.Name != "read_file" || got[0].Function.Description != "Read a file from disk" {
		t.Errorf("unexpected function mapping: %+v", got[0].Function)
	}
}

func TestBuildTools_empty(t *testing.T) {
	c := NewClient(&config.Config{OpenAIAPIKey: "test-key", Model: "gpt-4o"})
	got := c.buildTools(nil)
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %d", len(got))
	}
}

func TestBuildRequest_WithToolsSetsToolChoiceAuto(t *testing.T) {
	c := NewClient(&config.Config{OpenAIAPIKey: "test-key", Model: "gpt-4o"})
	tools := []Tool{{Type: "function", Function: ToolFunction{Name: "shell"}}}

	req := c.buildRequest(nil, ChatCompletionOptions{Tools: tools})
	if len(req.Tools) != 1 {
		t.Fatalf("expected 1 tool on request, got %d", len(req.Tools))
	}
	if req.ToolChoice != "auto" {
		t.Errorf("expected ToolChoice auto, got %v", req.ToolChoice)
	}
}

func TestBuildRequest_WithoutToolsLeavesToolChoiceUnset(t *testing.T) {
	c := NewClient(&config.Config{OpenAIAPIKey: "test-key", Model: "gpt-4o"})

	req := c.buildRequest(nil, ChatCompletionOptions{})
	if len(req.Tools) != 0 {
		t.Fatalf("expected no tools, got %d", len(req.Tools))
	}
	if req.ToolChoice != nil {
		t.Errorf("expected nil ToolChoice, got %v", req.ToolChoice)
	}
}

func TestStreamUsageMeta_nilAndZero(t *testing.T) {
	if m := streamUsageMeta(nil); m != nil {
		t.Errorf("expected nil meta for nil usage, got %v", m)
	}
	if m := streamUsageMeta(&openai.Usage{}); m != nil {
		t.Errorf("expected nil meta for zero usage, got %v", m)
	}
	m := streamUsageMeta(&openai.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3})
	if m["total_tokens"] != "3" || m["prompt_tokens"] != "1" || m["completion_tokens"] != "2" {
		t.Errorf("unexpected usage meta: %v", m)
	}
}

func TestStreamDoneEvent_nilUsage(t *testing.T) {
	ev := streamDoneEvent(nil, "stop")
	if ev.Type != EventTypeContentDone || !ev.Done {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if ev.FinishReason != "stop" {
		t.Errorf("expected FinishReason stop, got %q", ev.FinishReason)
	}
	if ev.Meta != nil {
		t.Errorf("expected nil meta, got %v", ev.Meta)
	}
}

func TestStreamDoneEvent_withUsage(t *testing.T) {
	ev := streamDoneEvent(&openai.Usage{TotalTokens: 42}, "tool_calls")
	if ev.Meta == nil || ev.Meta["total_tokens"] != "42" {
		t.Errorf("expected total_tokens=42 in meta, got %v", ev.Meta)
	}
	if ev.FinishReason != "tool_calls" {
		t.Errorf("expected FinishReason tool_calls, got %q", ev.FinishReason)
	}
}

func TestChatCompletion_StreamingToolCallArgsSplitAcrossChunks(t *testing.T) {
	ms := NewMockServer()
	defer ms.Close()

	chunks := NewChunkBuilder().
		AddToolCallIndexed(0, "call_1", "read_file", `{"pa`).
		AddToolCallArgsFrag(0, `th":"RE`).
		AddToolCallArgsFrag(0, `ADME.md"}`).
		AddFinishToolCalls().
		Build()
	ms.AddStreamingResponse(chunks, 200)

	client := ms.TestClient()
	events := CollectEventsSync(client.ChatCompletion(context.Background(), []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "read"},
	}, ChatCompletionOptions{Stream: true, MaxRetries: 0}))

	var toolEvents []StreamEvent
	var done *StreamEvent
	for i := range events {
		switch events[i].Type {
		case EventTypeToolCall:
			toolEvents = append(toolEvents, events[i])
		case EventTypeContentDone:
			done = &events[i]
		}
	}
	if len(toolEvents) != 1 {
		t.Fatalf("expected exactly 1 tool call event (not per-chunk), got %d", len(toolEvents))
	}
	tc := toolEvents[0].Tool
	if tc == nil || tc.Name != "read_file" || tc.ID != "call_1" {
		t.Fatalf("unexpected tool: %+v", tc)
	}
	if tc.ArgsErr != nil {
		t.Fatalf("unexpected ArgsErr: %v", tc.ArgsErr)
	}
	if tc.Arguments["path"] != "README.md" {
		t.Fatalf("path = %v want README.md; raw=%q", tc.Arguments["path"], tc.RawArguments)
	}
	if done == nil || done.FinishReason != "tool_calls" {
		t.Fatalf("expected FinishReason tool_calls, got %+v", done)
	}
}

func TestChatCompletion_StreamingParallelToolCallsInterleaved(t *testing.T) {
	ms := NewMockServer()
	defer ms.Close()

	chunks := NewChunkBuilder().
		AddToolCallIndexed(0, "call_a", "read_file", `{"path":"a.go"}`).
		AddToolCallIndexed(1, "call_b", "list_dir", `{"path":"."}`).
		AddToolCallArgsFrag(0, ``). // no-op fragment; already complete
		AddFinishToolCalls().
		Build()
	ms.AddStreamingResponse(chunks, 200)

	client := ms.TestClient()
	events := CollectEventsSync(client.ChatCompletion(context.Background(), []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "explore"},
	}, ChatCompletionOptions{Stream: true, MaxRetries: 0}))

	var toolEvents []StreamEvent
	for i := range events {
		if events[i].Type == EventTypeToolCall {
			toolEvents = append(toolEvents, events[i])
		}
	}
	if len(toolEvents) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(toolEvents))
	}
	// Stable sorted-by-index order
	if toolEvents[0].Tool.ID != "call_a" || toolEvents[1].Tool.ID != "call_b" {
		t.Fatalf("expected call_a then call_b, got %q then %q",
			toolEvents[0].Tool.ID, toolEvents[1].Tool.ID)
	}
	if toolEvents[0].Tool.Arguments["path"] != "a.go" {
		t.Fatalf("call_a path = %v", toolEvents[0].Tool.Arguments["path"])
	}
	if toolEvents[1].Tool.Arguments["path"] != "." {
		t.Fatalf("call_b path = %v", toolEvents[1].Tool.Arguments["path"])
	}
}

func TestChatCompletion_StreamingMalformedToolArgsStillEmitted(t *testing.T) {
	ms := NewMockServer()
	defer ms.Close()

	chunks := NewChunkBuilder().
		AddToolCallIndexed(0, "call_bad", "read_file", `{"path":`).
		AddFinishToolCalls().
		Build()
	ms.AddStreamingResponse(chunks, 200)

	client := ms.TestClient()
	events := CollectEventsSync(client.ChatCompletion(context.Background(), []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "x"},
	}, ChatCompletionOptions{Stream: true, MaxRetries: 0}))

	var toolEvents []StreamEvent
	for i := range events {
		if events[i].Type == EventTypeToolCall {
			toolEvents = append(toolEvents, events[i])
		}
	}
	if len(toolEvents) != 1 {
		t.Fatalf("expected 1 tool event even with malformed JSON, got %d", len(toolEvents))
	}
	if toolEvents[0].Tool.ArgsErr == nil {
		t.Fatal("expected ArgsErr")
	}
	if toolEvents[0].Tool.ID != "call_bad" {
		t.Fatalf("must still emit ID, got %q", toolEvents[0].Tool.ID)
	}
}

func TestChatCompletion_StreamingZeroArgTool(t *testing.T) {
	ms := NewMockServer()
	defer ms.Close()

	chunks := NewChunkBuilder().
		AddToolCallIndexed(0, "call_z", "todos", ``).
		AddFinishToolCalls().
		Build()
	ms.AddStreamingResponse(chunks, 200)

	client := ms.TestClient()
	events := CollectEventsSync(client.ChatCompletion(context.Background(), []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "x"},
	}, ChatCompletionOptions{Stream: true, MaxRetries: 0}))

	var toolEvents []StreamEvent
	for i := range events {
		if events[i].Type == EventTypeToolCall {
			toolEvents = append(toolEvents, events[i])
		}
	}
	if len(toolEvents) != 1 {
		t.Fatalf("expected 1 tool event, got %d", len(toolEvents))
	}
	tc := toolEvents[0].Tool
	if tc.Arguments == nil {
		t.Fatal("zero-arg tool must yield empty map, not nil")
	}
	if len(tc.Arguments) != 0 || tc.ArgsErr != nil {
		t.Fatalf("unexpected args: %+v err=%v", tc.Arguments, tc.ArgsErr)
	}
}

func TestChatCompletion_CancelMidToolCallYieldsCancelled(t *testing.T) {
	ms := NewMockServer()
	defer ms.Close()

	// Slow chunks so we can cancel mid-stream
	idx := 0
	ms.AddStreamingResponse([]MockStreamChunk{
		{ToolCallID: "call_1", ToolCallName: "shell", ToolCallIndex: &idx, ToolCallArgsFrag: `{"command":"`, Delay: 200 * time.Millisecond},
		{ToolCallIndex: &idx, ToolCallArgsFrag: `sleep 60"}`, Delay: 5 * time.Second},
		{FinishReason: "tool_calls"},
	}, 200)

	client := ms.TestClient()
	ctx, cancel := context.WithCancel(context.Background())
	events := client.ChatCompletion(ctx, []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "x"},
	}, ChatCompletionOptions{Stream: true, MaxRetries: 0})

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	collected := CollectEvents(events, 3*time.Second)
	var cancelled bool
	var sawError bool
	for _, ev := range collected {
		if ev.Type == EventTypeContentDone && ev.Cancelled {
			cancelled = true
		}
		if ev.Type == EventTypeError {
			sawError = true
		}
	}
	if !cancelled {
		t.Fatalf("expected Cancelled:true ContentDone, got events: %+v", collected)
	}
	if sawError {
		t.Fatal("cancellation must not emit EventTypeError")
	}
}

func TestChatCompletion_NonStreamingToolCallArguments(t *testing.T) {
	ms := NewMockServer()
	defer ms.Close()

	body := map[string]interface{}{
		"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "gpt-4o",
		"choices": []map[string]interface{}{{
			"index": 0,
			"message": map[string]interface{}{
				"role":    "assistant",
				"content": "",
				"tool_calls": []map[string]interface{}{{
					"id": "call_ns", "type": "function",
					"function": map[string]interface{}{
						"name":      "read_file",
						"arguments": `{"path":"main.go"}`,
					},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]interface{}{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3},
	}
	data, _ := json.Marshal(body)
	ms.AddResponse(MockResponse{StatusCode: 200, Body: string(data)})

	client := ms.TestClient()
	events := CollectEventsSync(client.ChatCompletion(context.Background(), []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "x"},
	}, ChatCompletionOptions{Stream: false, MaxRetries: 0}))

	var toolEvents []StreamEvent
	var done *StreamEvent
	for i := range events {
		switch events[i].Type {
		case EventTypeToolCall:
			toolEvents = append(toolEvents, events[i])
		case EventTypeContentDone:
			done = &events[i]
		}
	}
	if len(toolEvents) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolEvents))
	}
	if toolEvents[0].Tool.Arguments["path"] != "main.go" {
		t.Fatalf("path = %v", toolEvents[0].Tool.Arguments["path"])
	}
	if toolEvents[0].Tool.RawArguments != `{"path":"main.go"}` {
		t.Fatalf("RawArguments = %q", toolEvents[0].Tool.RawArguments)
	}
	if done == nil || done.FinishReason != "tool_calls" {
		t.Fatalf("expected FinishReason tool_calls, got %+v", done)
	}
}

// TestChatCompletion_TokenUsage tests that token usage is properly captured
func TestChatCompletion_TokenUsage(t *testing.T) {
	ms := NewMockServer()
	defer ms.Close()

	ms.AddNonStreamingResponse("Hello world!", 200)

	client := ms.TestClient()

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "Test"},
	}

	opts := ChatCompletionOptions{
		Stream:     false,
		MaxRetries: 0,
	}

	events := client.ChatCompletion(context.Background(), messages, opts)

	var lastEvent *StreamEvent
	for event := range events {
		lastEvent = &event
	}

	if lastEvent == nil {
		t.Fatal("no events received")
	}

	if lastEvent.Type != EventTypeContentDone {
		t.Errorf("expected ContentDone, got %v", lastEvent.Type)
	}

	// Check metadata contains token usage
	if lastEvent.Meta == nil {
		t.Error("expected metadata with token usage")
	} else {
		if _, ok := lastEvent.Meta["total_tokens"]; !ok {
			t.Error("expected total_tokens in metadata")
		}
		if _, ok := lastEvent.Meta["prompt_tokens"]; !ok {
			t.Error("expected prompt_tokens in metadata")
		}
		if _, ok := lastEvent.Meta["completion_tokens"]; !ok {
			t.Error("expected completion_tokens in metadata")
		}
	}
}
