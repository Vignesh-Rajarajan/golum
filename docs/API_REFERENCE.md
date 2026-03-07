# API Reference

## Client

### `NewClient(cfg *config.Config) *Client`

Creates a new LLM client with the given configuration.

**Parameters:**
- `cfg`: Configuration object containing API keys and settings

**Returns:**
- `*Client`: Initialized client ready to make requests

**Example:**
```go
cfg := &config.Config{
    OpenAIAPIKey: "sk-...",
    Model:        "gpt-4o",
}
client := llm.NewClient(cfg)
```

---

## ChatCompletion

### `ChatCompletion(ctx context.Context, messages []openai.ChatCompletionMessage, opts ChatCompletionOptions) <-chan StreamEvent`

Performs a chat completion with support for streaming, tool calling, and automatic retries.

**Parameters:**
- `ctx`: Context for cancellation and timeout
- `messages`: Conversation history
- `opts`: Configuration options (streaming, tools, retries, etc.)

**Returns:**
- `<-chan StreamEvent`: Channel of stream events

**Event Types:**
- `EventTypeContentStart`: Content generation started
- `EventTypeContentDelta`: Incremental content chunk
- `EventTypeContentDone`: Content generation completed
- `EventTypeToolCall`: Tool/function call requested
- `EventTypeError`: Error occurred

**Example:**
```go
messages := []openai.ChatCompletionMessage{
    {Role: openai.ChatMessageRoleUser, Content: "Hello!"},
}

opts := ChatCompletionOptions{
    Stream:     true,
    MaxRetries: 3,
    Timeout:    30 * time.Second,
}

events := client.ChatCompletion(ctx, messages, opts)
for event := range events {
    switch event.Type {
    case EventTypeContentDelta:
        fmt.Print(event.Content)
    case EventTypeContentDone:
        fmt.Println("\nDone!")
    case EventTypeError:
        log.Printf("Error: %v", event.Error)
    }
}
```

---

## Types

### ChatCompletionOptions

```go
type ChatCompletionOptions struct {
    Stream     bool                   // Enable streaming (default: true)
    Tools      []Tool                 // Available tools/functions
    MaxRetries int                    // Max retries for rate/connection errors
    Timeout    time.Duration          // Request timeout
    Metadata   map[string]interface{} // Additional metadata
}
```

**Fields:**
- `Stream`: Whether to stream the response (recommended: true)
- `Tools`: List of tools/functions the LLM can call
- `MaxRetries`: Number of retries for transient errors (default: 3)
- `Timeout`: Request timeout duration
- `Metadata`: Custom metadata to include with the request

---

### StreamEvent

```go
type StreamEvent struct {
    Type    EventType              // Type of event
    Content string                 // Content for ContentDelta events
    Error   error                  // Error for Error events
    Tool    *ToolCall              // Tool call for ToolCall events
    Done    bool                   // Completion flag
    Meta    map[string]string      // Additional metadata
}
```

**Fields:**
- `Type`: The type of event (see EventType constants)
- `Content`: Text content (for ContentDelta events)
- `Error`: Error object (for Error events)
- `Tool`: Tool call details (for ToolCall events)
- `Done`: Whether generation is complete
- `Meta`: Token usage and other metadata

---

### EventType

```go
type EventType int

const (
    EventTypeContentDelta EventType = iota // Incremental content chunk
    EventTypeContentStart                  // Content generation started
    EventTypeContentDone                   // Content generation completed
    EventTypeToolCall                      // Tool/function call
    EventTypeError                         // Error occurred
)
```

---

### Tool

```go
type Tool struct {
    Type     string       // Tool type (e.g., "function")
    Function ToolFunction // Function definition
}
```

---

### ToolFunction

```go
type ToolFunction struct {
    Name        string                 // Function name
    Description string                 // Function description
    Parameters  map[string]interface{} // JSON Schema for parameters
}
```

---

### ToolCall

```go
type ToolCall struct {
    ID        string                 // Unique identifier
    Name      string                 // Function name
    Arguments map[string]interface{} // Function arguments
}
```

---

## Retry Behavior

The client automatically retries requests in the following scenarios:

### Rate Limit Errors (HTTP 429)
- **Behavior**: Retries with exponential backoff
- **Backoff**: `2^attempt` seconds (1s, 2s, 4s, 8s, ...)
- **Max Attempts**: Configurable via `MaxRetries` (default: 3)

### Connection Errors
- **Behavior**: Retries with exponential backoff
- **Includes**: Timeouts, network errors, DNS failures
- **Max Attempts**: Configurable via `MaxRetries` (default: 3)

### API Errors (HTTP 500, etc.)
- **Behavior**: No automatic retry
- **Reason**: Likely a server bug, retrying won't help
- **Action**: Returns error immediately

**Example:**
```go
opts := ChatCompletionOptions{
    Stream:     true,
    MaxRetries: 5, // Will retry up to 5 times
}
```

---

## Error Handling

### Checking for Errors

```go
events := client.ChatCompletion(ctx, messages, opts)
for event := range events {
    if event.Type == llm.EventTypeError {
        // Handle different error types
        errStr := event.Error.Error()
        
        if strings.Contains(errStr, "rate limit") {
            fmt.Println("Rate limited - client will retry automatically")
        } else if strings.Contains(errStr, "connection") {
            fmt.Println("Connection error - client will retry automatically")
        } else {
            fmt.Printf("API error (no retry): %v\n", event.Error)
        }
    }
}
```

### Context Cancellation

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

events := client.ChatCompletion(ctx, messages, opts)
for event := range events {
    if event.Type == llm.EventTypeError {
        if errors.Is(event.Error, context.DeadlineExceeded) {
            fmt.Println("Request timed out")
        }
    }
}
```

---

## Best Practices

### 1. Always Use Streaming

```go
// Good - streaming for better UX
opts := ChatCompletionOptions{Stream: true}

// Avoid - non-streaming unless necessary
opts := ChatCompletionOptions{Stream: false}
```

### 2. Set Appropriate Timeouts

```go
// For quick queries
opts := ChatCompletionOptions{
    Stream:  true,
    Timeout: 10 * time.Second,
}

// For complex tasks
opts := ChatCompletionOptions{
    Stream:  true,
    Timeout: 60 * time.Second,
}
```

### 3. Handle All Event Types

```go
for event := range events {
    switch event.Type {
    case llm.EventTypeContentStart:
        // Show loading indicator
    case llam.EventTypeContentDelta:
        // Display content
    case llm.EventTypeContentDone:
        // Hide loading, show token usage
    case llm.EventTypeToolCall:
        // Execute tool
    case llm.EventTypeError:
        // Show error to user
    }
}
```

### 4. Use Context for Cancellation

```go
ctx, cancel := context.WithCancel(context.Background())

// Cancel on user interrupt
go func() {
    sigint := make(chan os.Signal, 1)
    signal.Notify(sigint, os.Interrupt)
    <-sigint
    cancel()
}()

events := client.ChatCompletion(ctx, messages, opts)
```

### 5. Buffer Channels for High-Throughput

```go
// The client uses buffered channels (100 events)
// For very high throughput, you might want your own buffer:

var buffer strings.Builder
for event := range events {
    if event.Type == llam.EventTypeContentDelta {
        buffer.WriteString(event.Content)
    }
}
```

---

## Migration from Stream() to ChatCompletion()

If you were using the old `Stream()` method:

```go
// Old way
stream := client.Stream(ctx, messages)
for chunk := range stream {
    if chunk.Error != nil {
        log.Fatal(chunk.Error)
    }
    if chunk.Done {
        break
    }
    fmt.Print(chunk.Content)
}

// New way (recommended)
opts := ChatCompletionOptions{Stream: true}
events := client.ChatCompletion(ctx, messages, opts)
for event := range events {
    switch event.Type {
    case EventTypeContentDelta:
        fmt.Print(event.Content)
    case EventTypeContentDone:
        break
    case EventTypeError:
        log.Fatal(event.Error)
    }
}
```

**Benefits of ChatCompletion():**
- ✅ Automatic retries with exponential backoff
- ✅ Tool/function calling support
- ✅ Better error handling
- ✅ Consistent API for streaming and non-streaming
- ✅ Token usage statistics
