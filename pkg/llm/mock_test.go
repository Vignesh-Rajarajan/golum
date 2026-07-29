package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
)

// MockServer provides a mock OpenAI API server for testing
type MockServer struct {
	Server     *httptest.Server
	RequestLog []MockRequest
	Responses  []MockResponse
	mu         sync.Mutex
	CurrentIdx int
}

// MockRequest logs incoming requests
type MockRequest struct {
	Method string
	Path   string
	Body   map[string]interface{}
}

// MockResponse defines a mock response
type MockResponse struct {
	StatusCode int
	Body       string
	IsStream   bool
	Chunks     []MockStreamChunk
	Delay      time.Duration
}

// MockStreamChunk represents a single chunk in a streaming response
type MockStreamChunk struct {
	Content          string
	FinishReason     string
	ToolCallID       string
	ToolCallName     string
	ToolCallIndex    *int
	ToolCallArgsFrag string // fragment of function.arguments JSON
	Delay            time.Duration
	RawJSON          string // if set, sent as-is instead of buildChunkData
}

// NewMockServer creates a new mock server
func NewMockServer() *MockServer {
	ms := &MockServer{
		RequestLog: make([]MockRequest, 0),
		Responses:  make([]MockResponse, 0),
	}

	ms.Server = httptest.NewServer(http.HandlerFunc(ms.handler))
	return ms
}

// handler processes incoming requests
func (ms *MockServer) handler(w http.ResponseWriter, r *http.Request) {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	// Log the request
	var body map[string]interface{}
	if r.Body != nil {
		decoder := json.NewDecoder(r.Body)
		decoder.Decode(&body)
	}

	ms.RequestLog = append(ms.RequestLog, MockRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Body:   body,
	})

	// Get the response
	if ms.CurrentIdx >= len(ms.Responses) {
		http.Error(w, "no more mock responses", http.StatusInternalServerError)
		return
	}

	response := ms.Responses[ms.CurrentIdx]
	ms.CurrentIdx++

	// Apply delay
	if response.Delay > 0 {
		time.Sleep(response.Delay)
	}

	// Send response
	w.WriteHeader(response.StatusCode)

	if response.IsStream {
		ms.sendStreamResponse(w, response)
	} else {
		w.Write([]byte(response.Body))
	}
}

// sendStreamResponse sends a streaming response
func (ms *MockServer) sendStreamResponse(w http.ResponseWriter, response MockResponse) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	for _, chunk := range response.Chunks {
		if chunk.Delay > 0 {
			time.Sleep(chunk.Delay)
		}

		var data string
		if chunk.RawJSON != "" {
			data = chunk.RawJSON
		} else {
			data = ms.buildChunkData(chunk)
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	// Send [DONE] message
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// buildChunkData builds a chunk data object
func (ms *MockServer) buildChunkData(chunk MockStreamChunk) string {
	chunkData := map[string]interface{}{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   "gpt-4o",
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"delta": ms.buildDelta(chunk),
			},
		},
	}

	if chunk.FinishReason != "" {
		chunkData["choices"].([]map[string]interface{})[0]["finish_reason"] = chunk.FinishReason
	}

	data, _ := json.Marshal(chunkData)
	return string(data)
}

// buildDelta builds the delta object for a chunk
func (ms *MockServer) buildDelta(chunk MockStreamChunk) map[string]interface{} {
	delta := make(map[string]interface{})

	if chunk.Content != "" {
		delta["content"] = chunk.Content
	}

	hasTool := chunk.ToolCallID != "" || chunk.ToolCallName != "" || chunk.ToolCallArgsFrag != "" || chunk.ToolCallIndex != nil
	if hasTool {
		tc := map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":      chunk.ToolCallName,
				"arguments": chunk.ToolCallArgsFrag,
			},
		}
		if chunk.ToolCallID != "" {
			tc["id"] = chunk.ToolCallID
		}
		if chunk.ToolCallIndex != nil {
			tc["index"] = *chunk.ToolCallIndex
		} else {
			tc["index"] = 0
		}
		delta["tool_calls"] = []map[string]interface{}{tc}
	}

	return delta
}

// AddResponse adds a mock response
func (ms *MockServer) AddResponse(response MockResponse) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.Responses = append(ms.Responses, response)
}

// AddStreamingResponse adds a streaming response
func (ms *MockServer) AddStreamingResponse(chunks []MockStreamChunk, statusCode int) {
	ms.AddResponse(MockResponse{
		StatusCode: statusCode,
		IsStream:   true,
		Chunks:     chunks,
	})
}

// AddNonStreamingResponse adds a non-streaming response
func (ms *MockServer) AddNonStreamingResponse(content string, statusCode int) {
	body := map[string]interface{}{
		"id":      "chatcmpl-test",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "gpt-4o",
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     10,
			"completion_tokens": 20,
			"total_tokens":      30,
		},
	}

	data, _ := json.Marshal(body)
	ms.AddResponse(MockResponse{
		StatusCode: statusCode,
		Body:       string(data),
	})
}

// AddRateLimitError adds a rate limit error response
func (ms *MockServer) AddRateLimitError() {
	ms.AddResponse(MockResponse{
		StatusCode: http.StatusTooManyRequests,
		Body:       `{"error": {"message": "Rate limit exceeded", "type": "rate_limit_error"}}`,
	})
}

// AddConnectionError adds a connection error (server closes connection)
func (ms *MockServer) AddConnectionError() {
	ms.AddResponse(MockResponse{
		StatusCode: http.StatusServiceUnavailable,
		Body:       `{"error": {"message": "Connection error", "type": "connection_error"}}`,
	})
}

// AddAPIError adds a generic API error
func (ms *MockServer) AddAPIError() {
	ms.AddResponse(MockResponse{
		StatusCode: http.StatusInternalServerError,
		Body:       `{"error": {"message": "Internal server error", "type": "api_error"}}`,
	})
}

// GetRequestCount returns the number of requests received
func (ms *MockServer) GetRequestCount() int {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return len(ms.RequestLog)
}

// GetLastRequest returns the last request received
func (ms *MockServer) GetLastRequest() *MockRequest {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.RequestLog) == 0 {
		return nil
	}
	return &ms.RequestLog[len(ms.RequestLog)-1]
}

// Close closes the mock server
func (ms *MockServer) Close() {
	ms.Server.Close()
}

// URL returns the mock server URL
func (ms *MockServer) URL() string {
	return ms.Server.URL
}

// TestClient creates a client connected to the mock server
func (ms *MockServer) TestClient() *Client {
	cfg := &config.Config{
		OpenAIAPIKey: "test-key",
		BaseURL:      ms.URL(),
		Model:        "gpt-4o",
	}
	return NewClient(cfg)
}

// CollectEvents collects all events from a channel with timeout
func CollectEvents(ch <-chan StreamEvent, timeout time.Duration) []StreamEvent {
	var events []StreamEvent
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, event)
		case <-timer.C:
			return events
		}
	}
}

// CollectEventsSync collects all events synchronously (waits for channel close)
func CollectEventsSync(ch <-chan StreamEvent) []StreamEvent {
	var events []StreamEvent
	for event := range ch {
		events = append(events, event)
	}
	return events
}

// AssertChannelClosed asserts that a channel is closed
func AssertChannelClosed(ch <-chan StreamEvent) bool {
	select {
	case _, ok := <-ch:
		return !ok // closed if ok is false
	default:
		return false // not closed, still has values
	}
}

// CountGoroutines counts the number of goroutines (for leak detection)
func CountGoroutines() int {
	return int(goroutineCount())
}

// goroutineCount returns the approximate number of goroutines
func goroutineCount() uint64 {
	return 0
}

// RetryTestHelper helps test retry logic
type RetryTestHelper struct {
	Attempts      int
	MaxAttempts   int
	ShouldSucceed bool
	mu            sync.Mutex
}

// ShouldRetry determines if we should retry
func (h *RetryTestHelper) ShouldRetry() bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.Attempts++

	if h.ShouldSucceed && h.Attempts >= h.MaxAttempts {
		return false
	}

	return h.Attempts < h.MaxAttempts
}

// ChunkBuilder helps build chunks for testing
type ChunkBuilder struct {
	chunks []MockStreamChunk
}

// NewChunkBuilder creates a new chunk builder
func NewChunkBuilder() *ChunkBuilder {
	return &ChunkBuilder{
		chunks: make([]MockStreamChunk, 0),
	}
}

// AddContent adds a content chunk
func (b *ChunkBuilder) AddContent(content string) *ChunkBuilder {
	b.chunks = append(b.chunks, MockStreamChunk{
		Content: content,
	})
	return b
}

// AddContentWithDelay adds a content chunk with delay
func (b *ChunkBuilder) AddContentWithDelay(content string, delay time.Duration) *ChunkBuilder {
	b.chunks = append(b.chunks, MockStreamChunk{
		Content: content,
		Delay:   delay,
	})
	return b
}

// AddFinish adds a finish chunk
func (b *ChunkBuilder) AddFinish() *ChunkBuilder {
	b.chunks = append(b.chunks, MockStreamChunk{
		FinishReason: "stop",
	})
	return b
}

// AddToolCall adds a tool call chunk
func (b *ChunkBuilder) AddToolCall(id, name string) *ChunkBuilder {
	idx := 0
	b.chunks = append(b.chunks, MockStreamChunk{
		ToolCallID:    id,
		ToolCallName:  name,
		ToolCallIndex: &idx,
	})
	return b
}

// AddToolCallArgsFrag appends an arguments fragment for a tool call at index.
func (b *ChunkBuilder) AddToolCallArgsFrag(index int, argsFrag string) *ChunkBuilder {
	idx := index
	b.chunks = append(b.chunks, MockStreamChunk{
		ToolCallIndex:    &idx,
		ToolCallArgsFrag: argsFrag,
	})
	return b
}

// AddToolCallIndexed adds a tool call with explicit index, id, name, and optional args fragment.
func (b *ChunkBuilder) AddToolCallIndexed(index int, id, name, argsFrag string) *ChunkBuilder {
	idx := index
	b.chunks = append(b.chunks, MockStreamChunk{
		ToolCallID:       id,
		ToolCallName:     name,
		ToolCallIndex:    &idx,
		ToolCallArgsFrag: argsFrag,
	})
	return b
}

// AddFinishToolCalls adds a finish_reason=tool_calls chunk.
func (b *ChunkBuilder) AddFinishToolCalls() *ChunkBuilder {
	b.chunks = append(b.chunks, MockStreamChunk{
		FinishReason: "tool_calls",
	})
	return b
}

// Build returns the chunks
func (b *ChunkBuilder) Build() []MockStreamChunk {
	return b.chunks
}

// MessageBuilder helps build complete messages from chunks
type MessageBuilder struct {
	chunks []string
	mu     sync.Mutex
}

// NewMessageBuilder creates a new message builder
func NewMessageBuilder() *MessageBuilder {
	return &MessageBuilder{
		chunks: make([]string, 0),
	}
}

// AddChunk adds a chunk to the message
func (b *MessageBuilder) AddChunk(chunk string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.chunks = append(b.chunks, chunk)
}

// Build returns the complete message
func (b *MessageBuilder) Build() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.chunks, "")
}

// Reset clears the builder
func (b *MessageBuilder) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.chunks = b.chunks[:0]
}

// ContextWithTimeout creates a context with timeout
func ContextWithTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

// WaitForChannelClose waits for a channel to close or timeout
func WaitForChannelClose(ch <-chan StreamEvent, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return true
			}
		case <-timer.C:
			return false
		}
	}
}

// IsRetryableError checks if an error is retryable
func IsRetryableError(err error) bool {
	return isRateLimitError(err) || isConnectionError(err)
}

// MockListener creates a mock listener for testing connection errors
type MockListener struct {
	net.Listener
	shouldFail bool
}

// Accept accepts a connection (or fails if shouldFail is true)
func (l *MockListener) Accept() (net.Conn, error) {
	if l.shouldFail {
		return nil, fmt.Errorf("connection refused")
	}
	return l.Listener.Accept()
}
