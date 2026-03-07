# Golum

A Claude Code clone built with Go, inspired by [charmbracelet/crush](https://github.com/charmbracelet/crush).

## Features

- OpenAI and OpenRouter API support
- Streaming responses
- Async client implementation
- Environment-based configuration

## Project Structure

```
.
├── cmd/
│   └── golum/
│       └── main.go          # Application entry point
├── pkg/
│   ├── config/
│   │   └── config.go        # Configuration management
│   └── llm/
│       ├── client.go        # LLM client implementation
│       └── stream.go        # Streaming response handler
├── .env.example             # Example environment variables
├── go.mod                   # Go module file
└── README.md                # This file
```

## Setup

1. Copy `.env.example` to `.env`:
   ```bash
   cp .env.example .env
   ```

2. Add your API keys to `.env`:
   ```bash
   # For OpenAI
   OPENAI_API_KEY=your_openai_api_key_here
   
   # For OpenRouter (alternative)
   OPENROUTER_API_KEY=your_openrouter_api_key_here
   ```

3. Install dependencies:
   ```bash
   go mod download
   ```

4. Run the application:
   ```bash
   go run cmd/golum/main.go
   ```

## Configuration

Environment variables:

- `OPENAI_API_KEY`: Your OpenAI API key
- `OPENROUTER_API_KEY`: Your OpenRouter API key (takes precedence over OpenAI key)
- `OPENAI_BASE_URL`: Base URL for OpenAI API (default: https://api.openai.com/v1)
- `OPENAI_MODEL`: Model to use (default: gpt-4o)

## Dependencies

- [charm.land/bubbles/v2](https://charm.land) - TUI components
- [charm.land/bubbletea/v2](https://charm.land) - TUI framework
- [charm.land/lipgloss/v2](https://charm.land) - Styling
- [github.com/sashabaranov/go-openai](https://github.com/sashabaranov/go-openai) - OpenAI client
- [github.com/joho/godotenv](https://github.com/joho/godotenv) - Environment management

## Usage Example

### Basic Chat Completion

```go
package main

import (
    "context"
    "fmt"
    
    "github.com/Vignesh-Rajarajan/golum/pkg/config"
    "github.com/Vignesh-Rajarajan/golum/pkg/llm"
    "github.com/sashabaranov/go-openai"
)

func main() {
    cfg, _ := config.Load()
    client := llm.NewClient(cfg)
    
    messages := []openai.ChatCompletionMessage{
        {
            Role:    openai.ChatMessageRoleUser,
            Content: "Hello!",
        },
    }
    
    // Streaming response (recommended)
    opts := llm.ChatCompletionOptions{
        Stream:     true,
        MaxRetries: 3,
    }
    
    events := client.ChatCompletion(context.Background(), messages, opts)
    for event := range events {
        switch event.Type {
        case llm.EventTypeContentDelta:
            fmt.Print(event.Content)
        case llam.EventTypeContentDone:
            fmt.Println("\nDone!")
        case llm.EventTypeError:
            fmt.Printf("Error: %v", event.Error)
        }
    }
}
```

### With Tool Calling

```go
tools := []llm.Tool{
    {
        Type: "function",
        Function: llm.ToolFunction{
            Name:        "get_weather",
            Description: "Get weather for a location",
            Parameters: map[string]interface{}{
                "type": "object",
                "properties": map[string]interface{}{
                    "location": map[string]interface{}{
                        "type": "string",
                    },
                },
            },
        },
    },
}

opts := llm.ChatCompletionOptions{
    Stream: false,
    Tools:  tools,
}

events := client.ChatCompletion(ctx, messages, opts)
for event := range events {
    if event.Type == llm.EventTypeToolCall {
        fmt.Printf("Tool: %s\n", event.Tool.Name)
        // Execute tool and continue conversation
    }
}
```

### Non-Streaming Response

```go
opts := llm.ChatCompletionOptions{
    Stream: false, // Get complete response at once
}

events := client.ChatCompletion(ctx, messages, opts)
for event := range events {
    if event.Type == llm.EventTypeContentDelta {
        fmt.Println(event.Content) // Complete response
    }
}
```

## Features

- **Streaming Support**: Real-time response streaming via Server-Sent Events
- **Automatic Retries**: Exponential backoff for rate limits and connection errors
- **Tool Calling**: Full support for OpenAI function calling
- **Error Handling**: Comprehensive error types and handling
- **Dual Provider Support**: Works with OpenAI and OpenRouter APIs

## Documentation

- [HTTP Streaming Explained (ELI5)](./docs/STREAMING.md) - How streaming works under the hood
- [Examples](./examples/) - Complete usage examples

## License

MIT
