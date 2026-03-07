package llm

import (
	"time"
)

// EventType represents the type of stream event
type EventType int

const (
	EventTypeContentDelta EventType = iota // Incremental content chunk
	EventTypeContentStart                  // Content generation started
	EventTypeContentDone                   // Content generation completed
	EventTypeToolCall                      // Tool/function call
	EventTypeError                         // Error occurred
)

// StreamEvent represents a single event in the stream
type StreamEvent struct {
	Type    EventType
	Content string            // For content delta events
	Error   error             // For error events
	Tool    *ToolCall         // For tool call events
	Done    bool              // For completion events
	Meta    map[string]string // Additional metadata
}

// ToolCall represents a tool/function call from the LLM
type ToolCall struct {
	ID        string                 // Unique identifier for the tool call
	Name      string                 // Name of the tool/function
	Arguments map[string]interface{} // Arguments passed to the tool
}

// ChatCompletionOptions configures the chat completion behavior
type ChatCompletionOptions struct {
	Stream     bool                   // Enable streaming response (default: true)
	Tools      []Tool                 // Available tools/functions for the LLM
	MaxRetries int                    // Maximum number of retries on rate limit/connection errors
	Timeout    time.Duration          // Request timeout
	Metadata   map[string]interface{} // Additional request metadata
}

// Tool represents a tool/function that can be called by the LLM
type Tool struct {
	Type     string       // Tool type (e.g., "function")
	Function ToolFunction // Function definition
}

// ToolFunction defines a function tool
type ToolFunction struct {
	Name        string                 // Function name
	Description string                 // Function description
	Parameters  map[string]interface{} // JSON Schema for parameters
}

// ChatCompletionResult contains the final result of chat completion
type ChatCompletionResult struct {
	Content   string      // Generated content
	ToolCalls []*ToolCall // Tool calls made during completion
	Usage     *Usage      // Token usage statistics
}

// Usage represents token usage statistics
type Usage struct {
	PromptTokens     int // Tokens in the prompt
	CompletionTokens int // Tokens in the completion
	TotalTokens      int // Total tokens used
}
