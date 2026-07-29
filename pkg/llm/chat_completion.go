package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/applog"
	"github.com/sashabaranov/go-openai"
)

const defaultStreamTimeout = 10 * time.Minute

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
//   - ThinkingDelta: Reasoning chunks (when GOLUM_STREAM_REASONING=1)
//   - ContentStart: Content generation started
//   - ContentDone: Content generation completed (FinishReason set when known)
//   - ToolCall: Completed tool/function call (one per call, after accumulation)
//   - Error: Error occurred
//
// Retry Behavior:
//   - Rate limit errors: Retries with exponential backoff (2^attempt seconds)
//   - Connection errors: Retries with exponential backoff (2^attempt seconds)
//   - API errors: No retry, returns error immediately
func (c *Client) ChatCompletion(
	ctx context.Context,
	messages []openai.ChatCompletionMessage,
	opts ChatCompletionOptions,
) <-chan StreamEvent {
	ch := make(chan StreamEvent, 100) // Buffered channel for better performance

	go func() {
		defer close(ch)
		defer func() {
			if r := recover(); r != nil {
				applog.Init()
				applog.LogPanic("llm.ChatCompletion", r)
				select {
				case ch <- StreamEvent{Type: EventTypeError, Error: fmt.Errorf("internal error: %v", r)}:
				default:
				}
			}
		}()

		// Set defaults
		if opts.MaxRetries == 0 {
			opts.MaxRetries = DefaultMaxRetries
		}

		// Build the request
		req := c.buildRequest(messages, opts)

		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = defaultStreamTimeout
		}

		// Retry loop
		for attempt := 0; attempt <= opts.MaxRetries; attempt++ {
			reqCtx, cancel := context.WithTimeout(ctx, timeout)
			err := c.executeRequest(reqCtx, req, opts.Stream, ch)
			cancel()

			if err == nil {
				// Success, we're done
				return
			}

			// Check if we should retry
			if !c.shouldRetry(err, attempt, opts.MaxRetries, ch) {
				return
			}

			// Exponential backoff — respect cancellation
			waitTime := time.Duration(1<<uint(attempt)) * time.Second
			t := time.NewTimer(waitTime)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
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

	if opts.Stream {
		req.StreamOptions = &openai.StreamOptions{
			IncludeUsage: true,
		}
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

func streamUsageMeta(u *openai.Usage) map[string]string {
	if u == nil || u.TotalTokens <= 0 {
		return nil
	}
	return map[string]string{
		"prompt_tokens":     fmt.Sprintf("%d", u.PromptTokens),
		"completion_tokens": fmt.Sprintf("%d", u.CompletionTokens),
		"total_tokens":      fmt.Sprintf("%d", u.TotalTokens),
	}
}

func streamDoneEvent(usage *openai.Usage, finishReason string) StreamEvent {
	ev := StreamEvent{Type: EventTypeContentDone, Done: true, FinishReason: finishReason}
	if m := streamUsageMeta(usage); m != nil {
		ev.Meta = m
	}
	return ev
}

// toolCallBuilder accumulates streamed tool-call fragments keyed by index.
type toolCallBuilder struct {
	id   string
	name string
	args strings.Builder
}

func buildToolCall(b *toolCallBuilder) *ToolCall {
	tc := &ToolCall{
		ID:           b.id,
		Name:         b.name,
		RawArguments: b.args.String(),
	}
	s := strings.TrimSpace(tc.RawArguments)
	if s == "" {
		tc.Arguments = map[string]interface{}{}
	} else if err := json.Unmarshal([]byte(s), &tc.Arguments); err != nil {
		tc.ArgsErr = err
	}
	return tc
}

func sortedBuilderKeys(builders map[int]*toolCallBuilder) []int {
	keys := make([]int, 0, len(builders))
	for k := range builders {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

func toolCallFromOpenAI(toolCall openai.ToolCall) *ToolCall {
	tc := &ToolCall{
		ID:           toolCall.ID,
		Name:         toolCall.Function.Name,
		RawArguments: toolCall.Function.Arguments,
	}
	s := strings.TrimSpace(tc.RawArguments)
	if s == "" {
		tc.Arguments = map[string]interface{}{}
	} else if err := json.Unmarshal([]byte(s), &tc.Arguments); err != nil {
		tc.ArgsErr = err
	}
	return tc
}

// executeStreamRequest handles streaming chat completion
func (c *Client) executeStreamRequest(
	ctx context.Context,
	req openai.ChatCompletionRequest,
	ch chan<- StreamEvent,
) error {
	ch <- StreamEvent{Type: EventTypeContentStart}

	applog.Printf("stream: CreateChatCompletionStream model=%q msgs=%d", c.config.Model, len(req.Messages))
	stream, err := c.client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			applog.Printf("stream: CreateChatCompletionStream cancelled")
			ch <- StreamEvent{Type: EventTypeContentDone, Done: true, Cancelled: true}
			return nil
		}
		applog.Printf("stream: CreateChatCompletionStream error: %v", err)
		return err
	}
	defer stream.Close()

	builders := map[int]*toolCallBuilder{}
	var lastFinishReason string
	var lastUsage *openai.Usage

	flush := func() {
		if builders == nil {
			return
		}
		for _, idx := range sortedBuilderKeys(builders) {
			b := builders[idx]
			tc := buildToolCall(b)
			applog.Printf("stream: EventTypeToolCall id=%q name=%q args_len=%d args_err=%v",
				tc.ID, tc.Name, len(tc.RawArguments), tc.ArgsErr != nil)
			ch <- StreamEvent{Type: EventTypeToolCall, Tool: tc}
		}
		builders = nil
	}

	accumulateToolCalls := func(toolCalls []openai.ToolCall) {
		for i, toolCall := range toolCalls {
			idx := i
			if toolCall.Index != nil {
				idx = *toolCall.Index
			}
			b, ok := builders[idx]
			if !ok {
				b = &toolCallBuilder{}
				builders[idx] = b
			}
			if toolCall.ID != "" {
				b.id = toolCall.ID
			}
			if toolCall.Function.Name != "" {
				b.name = toolCall.Function.Name
			}
			if toolCall.Function.Arguments != "" {
				b.args.WriteString(toolCall.Function.Arguments)
			}
		}
	}

	first := true
	for {
		raw, err := stream.RecvRaw()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				applog.Printf("stream: recv cancelled")
				flush()
				ch <- StreamEvent{Type: EventTypeContentDone, Done: true, Cancelled: true}
				return nil
			}
			if errors.Is(err, io.EOF) || err.Error() == "EOF" {
				applog.Printf("stream: recv EOF (done=%v)", err)
				flush()
				ch <- streamDoneEvent(lastUsage, lastFinishReason)
				return nil
			}
			applog.Printf("stream: recv error: %v", err)
			return err
		}
		if first {
			applog.Printf("stream: first chunk raw_bytes=%d", len(raw))
			first = false
		}

		content, thinking, finishReason, usage, toolCalls, hasChoices, err := parseStreamingChunk(raw)
		if err != nil {
			applog.Printf("stream: chunk json: %v", err)
			continue
		}
		if usage != nil {
			lastUsage = usage
		}

		// Usage-only final chunk (OpenAI: choices may be empty when using stream_options include_usage).
		if !hasChoices {
			if usage != nil && usage.TotalTokens > 0 {
				flush()
				ch <- streamDoneEvent(usage, lastFinishReason)
				return nil
			}
			continue
		}

		if content != "" {
			ch <- StreamEvent{
				Type:    EventTypeContentDelta,
				Content: content,
			}
		}
		if thinking != "" {
			ch <- StreamEvent{
				Type:    EventTypeThinkingDelta,
				Content: thinking,
			}
		}

		if len(toolCalls) > 0 {
			accumulateToolCalls(toolCalls)
		}

		if finishReason != "" {
			lastFinishReason = finishReason
			applog.Printf("stream: finish_reason=%q has_usage=%v", finishReason, usage != nil)
			flush()
			ch <- streamDoneEvent(usage, finishReason)
			return nil
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

	finishReason := ""
	// Process response
	if len(response.Choices) > 0 {
		choice := response.Choices[0]
		finishReason = string(choice.FinishReason)

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
				tc := toolCallFromOpenAI(toolCall)
				applog.Printf("nonstream: EventTypeToolCall id=%q name=%q args_len=%d args_err=%v",
					tc.ID, tc.Name, len(tc.RawArguments), tc.ArgsErr != nil)
				ch <- StreamEvent{
					Type: EventTypeToolCall,
					Tool: tc,
				}
			}
		}
	}

	// Send usage statistics if available
	if response.Usage.TotalTokens > 0 {
		ch <- StreamEvent{
			Type:         EventTypeContentDone,
			Done:         true,
			FinishReason: finishReason,
			Meta: map[string]string{
				"prompt_tokens":     fmt.Sprintf("%d", response.Usage.PromptTokens),
				"completion_tokens": fmt.Sprintf("%d", response.Usage.CompletionTokens),
				"total_tokens":      fmt.Sprintf("%d", response.Usage.TotalTokens),
			},
		}
	} else {
		ch <- StreamEvent{Type: EventTypeContentDone, Done: true, FinishReason: finishReason}
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
	if errors.Is(err, context.Canceled) {
		return false
	}

	if errors.Is(err, context.DeadlineExceeded) {
		ch <- StreamEvent{
			Type:  EventTypeError,
			Error: fmt.Errorf("request timed out (stream stalled or server too slow); increase GOLUM_STREAM_TIMEOUT: %w", err),
		}
		return false
	}

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
