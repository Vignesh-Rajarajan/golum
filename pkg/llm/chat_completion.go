package llm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sashabaranov/go-openai"
)

// DefaultMaxRetries is the default number of retries for rate limit and connection errors
const DefaultMaxRetries = 3

// ChatCompletion performs a chat completion with the LLM, supporting both streaming and non-streaming modes.
//
// This method provides a unified interface for chat completions with the following features:
//   - Automatic retry with exponential backoff for rate limits and connection errors
//   - Support for tool/function calling
//   - Both streaming and non-streaming responses
//   - Comprehensive error handling
//
// The method returns a channel of StreamEvent objects that the caller can iterate over.
// Events include:
//   - ContentDelta: Incremental content chunks (streaming only)
//   - ContentStart: Content generation started
//   - ContentDone: Content generation completed
//   - ToolCall: Tool/function call request
//   - Error: Error occurred
//
// Retry Behavior:
//   - Rate limit errors: Retries with exponential backoff (2^attempt seconds)
//   - Connection errors: Retries with exponential backoff (2^attempt seconds)
//   - API errors: No retry, returns error immediately
//
// Example usage:
//
//	messages := []openai.ChatCompletionMessage{
//	    {Role: openai.ChatMessageRoleUser, Content: "Hello!"},
//	}
//	opts := ChatCompletionOptions{
//	    Stream:     true,
//	    MaxRetries: 3,
//	}
//	events := client.ChatCompletion(ctx, messages, opts)
//	for event := range events {
//	    switch event.Type {
//	    case EventTypeContentDelta:
//	        fmt.Print(event.Content)
//	    case EventTypeContentDone:
//	        fmt.Println("\nDone!")
//	    case EventTypeError:
//	        log.Printf("Error: %v", event.Error)
//	    }
//	}
func (c *Client) ChatCompletion(
	ctx context.Context,
	messages []openai.ChatCompletionMessage,
	opts ChatCompletionOptions,
) <-chan StreamEvent {
	ch := make(chan StreamEvent, 100) // Buffered channel for better performance

	go func() {
		defer close(ch)

		// Set defaults
		if opts.MaxRetries == 0 {
			opts.MaxRetries = DefaultMaxRetries
		}

		// Build the request
		req := c.buildRequest(messages, opts)

		// Retry loop
		for attempt := 0; attempt <= opts.MaxRetries; attempt++ {
			err := c.executeRequest(ctx, req, opts.Stream, ch)

			if err == nil {
				// Success, we're done
				return
			}

			// Check if we should retry
			if !c.shouldRetry(err, attempt, opts.MaxRetries, ch) {
				return
			}

			// Exponential backoff
			waitTime := time.Duration(1<<uint(attempt)) * time.Second
			time.Sleep(waitTime)
		}
	}()

	return ch
}

// buildRequest constructs the chat completion request
func (c *Client) buildRequest(
	messages []openai.ChatCompletionMessage,
	opts ChatCompletionOptions,
) openai.ChatCompletionRequest {
	req := openai.ChatCompletionRequest{
		Model:    c.config.Model,
		Messages: messages,
		Stream:   opts.Stream,
	}

	// Add tools if provided
	if len(opts.Tools) > 0 {
		req.Tools = c.buildTools(opts.Tools)
		req.ToolChoice = "auto"
	}

	return req
}

// buildTools converts our Tool type to OpenAI's format
func (c *Client) buildTools(tools []Tool) []openai.Tool {
	result := make([]openai.Tool, len(tools))
	for i, tool := range tools {
		result[i] = openai.Tool{
			Type: openai.ToolType(tool.Type),
			Function: &openai.FunctionDefinition{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  tool.Function.Parameters,
			},
		}
	}
	return result
}

// executeRequest executes the chat completion request (streaming or non-streaming)
func (c *Client) executeRequest(
	ctx context.Context,
	req openai.ChatCompletionRequest,
	stream bool,
	ch chan<- StreamEvent,
) error {
	if stream {
		return c.executeStreamRequest(ctx, req, ch)
	}
	return c.executeNonStreamRequest(ctx, req, ch)
}

// executeStreamRequest handles streaming chat completion
func (c *Client) executeStreamRequest(
	ctx context.Context,
	req openai.ChatCompletionRequest,
	ch chan<- StreamEvent,
) error {
	// Send content start event
	ch <- StreamEvent{Type: EventTypeContentStart}

	// Create stream
	stream, err := c.client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return err
	}
	defer stream.Close()

	// Process stream
	for {
		response, err := stream.Recv()
		if err != nil {
			// Check if it's EOF (stream finished)
			if errors.Is(err, context.Canceled) || err.Error() == "EOF" {
				ch <- StreamEvent{Type: EventTypeContentDone, Done: true}
				return nil
			}
			return err
		}

		// Process choices
		if len(response.Choices) > 0 {
			choice := response.Choices[0]

			// Send content delta
			if choice.Delta.Content != "" {
				ch <- StreamEvent{
					Type:    EventTypeContentDelta,
					Content: choice.Delta.Content,
				}
			}

			// Handle tool calls
			if len(choice.Delta.ToolCalls) > 0 {
				for _, toolCall := range choice.Delta.ToolCalls {
					ch <- StreamEvent{
						Type: EventTypeToolCall,
						Tool: &ToolCall{
							ID:   toolCall.ID,
							Name: toolCall.Function.Name,
						},
					}
				}
			}

			// Check for completion
			if choice.FinishReason == "stop" {
				ch <- StreamEvent{Type: EventTypeContentDone, Done: true}
				return nil
			}
		}
	}
}

// executeNonStreamRequest handles non-streaming chat completion
func (c *Client) executeNonStreamRequest(
	ctx context.Context,
	req openai.ChatCompletionRequest,
	ch chan<- StreamEvent,
) error {
	// Send content start event
	ch <- StreamEvent{Type: EventTypeContentStart}

	// Make the request
	response, err := c.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return err
	}

	// Process response
	if len(response.Choices) > 0 {
		choice := response.Choices[0]

		// Send the complete content as a single event
		if choice.Message.Content != "" {
			ch <- StreamEvent{
				Type:    EventTypeContentDelta,
				Content: choice.Message.Content,
			}
		}

		// Handle tool calls
		if len(choice.Message.ToolCalls) > 0 {
			for _, toolCall := range choice.Message.ToolCalls {
				ch <- StreamEvent{
					Type: EventTypeToolCall,
					Tool: &ToolCall{
						ID:   toolCall.ID,
						Name: toolCall.Function.Name,
					},
				}
			}
		}
	}

	// Send usage statistics if available
	if response.Usage.TotalTokens > 0 {
		ch <- StreamEvent{
			Type: EventTypeContentDone,
			Done: true,
			Meta: map[string]string{
				"prompt_tokens":     fmt.Sprintf("%d", response.Usage.PromptTokens),
				"completion_tokens": fmt.Sprintf("%d", response.Usage.CompletionTokens),
				"total_tokens":      fmt.Sprintf("%d", response.Usage.TotalTokens),
			},
		}
	} else {
		ch <- StreamEvent{Type: EventTypeContentDone, Done: true}
	}

	return nil
}

// shouldRetry determines if we should retry the request based on the error
func (c *Client) shouldRetry(
	err error,
	attempt int,
	maxRetries int,
	ch chan<- StreamEvent,
) bool {
	// Check error type

	// Rate limit error
	if isRateLimitError(err) {
		if attempt < maxRetries {
			return true // Will retry with backoff
		}
		ch <- StreamEvent{
			Type:  EventTypeError,
			Error: fmt.Errorf("rate limit exceeded after %d retries: %w", maxRetries, err),
		}
		return false
	}

	// Connection error
	if isConnectionError(err) {
		if attempt < maxRetries {
			return true // Will retry with backoff
		}
		ch <- StreamEvent{
			Type:  EventTypeError,
			Error: fmt.Errorf("connection error after %d retries: %w", maxRetries, err),
		}
		return false
	}

	// Other API errors - don't retry
	ch <- StreamEvent{
		Type:  EventTypeError,
		Error: fmt.Errorf("API error: %w", err),
	}
	return false
}

// isRateLimitError checks if the error is a rate limit error
func isRateLimitError(err error) bool {
	// Check for common rate limit error patterns
	errStr := err.Error()
	return contains(errStr, "rate limit") ||
		contains(errStr, "429") ||
		contains(errStr, "too many requests")
}

// isConnectionError checks if the error is a connection error
func isConnectionError(err error) bool {
	// Check for common connection error patterns
	errStr := err.Error()
	return contains(errStr, "connection") ||
		contains(errStr, "timeout") ||
		contains(errStr, "network") ||
		contains(errStr, "ECONNREFUSED") ||
		contains(errStr, "ENOTFOUND")
}

// contains checks if a string contains a substring (case-insensitive)
func contains(str, substr string) bool {
	return len(str) >= len(substr) && (str == substr ||
		(len(str) > len(substr) && containsSubstring(str, substr)))
}

func containsSubstring(str, substr string) bool {
	for i := 0; i <= len(str)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			sc := str[i+j]
			subc := substr[j]
			// Simple lowercase comparison
			if sc >= 'A' && sc <= 'Z' {
				sc += 32
			}
			if subc >= 'A' && subc <= 'Z' {
				subc += 32
			}
			if sc != subc {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
