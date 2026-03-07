package main

import (
	"context"
	"fmt"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/sashabaranov/go-openai"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}

	// Create client
	client := llm.NewClient(cfg)

	ctx := context.Background()

	// Example 1: Streaming chat completion
	fmt.Println("=== Example 1: Streaming Chat Completion ===")
	exampleStreaming(client, ctx)

	// Example 2: Non-streaming chat completion
	fmt.Println("\n=== Example 2: Non-Streaming Chat Completion ===")
	exampleNonStreaming(client, ctx)

	// Example 3: Chat completion with tools
	fmt.Println("\n=== Example 3: Chat Completion with Tools ===")
	exampleWithTools(client, ctx)

	// Example 4: Error handling and retries
	fmt.Println("\n=== Example 4: Error Handling ===")
	exampleErrorHandling(client, ctx)
}

func exampleStreaming(client *llm.Client, ctx context.Context) {
	messages := []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleUser,
			Content: "Tell me a short story about a robot learning to paint.",
		},
	}

	opts := llm.ChatCompletionOptions{
		Stream:     true,
		MaxRetries: 3,
		Timeout:    30 * time.Second,
	}

	start := time.Now()
	fmt.Print("Response: ")

	events := client.ChatCompletion(ctx, messages, opts)
	for event := range events {
		switch event.Type {
		case llm.EventTypeContentStart:
			// Content generation started
			fmt.Print("[START] ")

		case llm.EventTypeContentDelta:
			// Incremental content chunk
			fmt.Print(event.Content)

		case llm.EventTypeContentDone:
			// Content generation completed
			fmt.Println(" [DONE]")
			if event.Meta != nil {
				fmt.Printf("Tokens - Prompt: %s, Completion: %s, Total: %s\n",
					event.Meta["prompt_tokens"],
					event.Meta["completion_tokens"],
					event.Meta["total_tokens"])
			}

		case llm.EventTypeError:
			// Error occurred
			fmt.Printf("\nError: %v\n", event.Error)

		case llm.EventTypeToolCall:
			// Tool call requested
			fmt.Printf("\nTool Call: %s (ID: %s)\n", event.Tool.Name, event.Tool.ID)
		}
	}

	elapsed := time.Since(start)
	fmt.Printf("Time: %v\n", elapsed)
}

func exampleNonStreaming(client *llm.Client, ctx context.Context) {
	messages := []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleUser,
			Content: "What is the capital of France? Answer in one word.",
		},
	}

	opts := llm.ChatCompletionOptions{
		Stream:     false, // Non-streaming
		MaxRetries: 3,
		Timeout:    10 * time.Second,
	}

	start := time.Now()
	fmt.Print("Response: ")

	events := client.ChatCompletion(ctx, messages, opts)
	for event := range events {
		switch event.Type {
		case llm.EventTypeContentDelta:
			fmt.Print(event.Content)

		case llm.EventTypeContentDone:
			fmt.Println()
			if event.Meta != nil {
				fmt.Printf("Tokens - Prompt: %s, Completion: %s, Total: %s\n",
					event.Meta["prompt_tokens"],
					event.Meta["completion_tokens"],
					event.Meta["total_tokens"])
			}

		case llm.EventTypeError:
			fmt.Printf("Error: %v\n", event.Error)
		}
	}

	elapsed := time.Since(start)
	fmt.Printf("Time: %v\n", elapsed)
}

func exampleWithTools(client *llm.Client, ctx context.Context) {
	// Define available tools
	tools := []llm.Tool{
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "get_weather",
				Description: "Get the current weather for a location",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"location": map[string]interface{}{
							"type":        "string",
							"description": "The city and state, e.g. San Francisco, CA",
						},
						"unit": map[string]interface{}{
							"type": "string",
							"enum": []string{"celsius", "fahrenheit"},
						},
					},
					"required": []string{"location"},
				},
			},
		},
	}

	messages := []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleUser,
			Content: "What's the weather like in San Francisco?",
		},
	}

	opts := llm.ChatCompletionOptions{
		Stream:     false,
		Tools:      tools,
		MaxRetries: 3,
		Timeout:    10 * time.Second,
	}

	fmt.Print("Response: ")

	events := client.ChatCompletion(ctx, messages, opts)
	for event := range events {
		switch event.Type {
		case llm.EventTypeContentDelta:
			fmt.Println(event.Content)

		case llm.EventTypeToolCall:
			fmt.Printf("Tool Call Requested:\n")
			fmt.Printf("  Tool: %s\n", event.Tool.Name)
			fmt.Printf("  ID: %s\n", event.Tool.ID)
			if event.Tool.Arguments != nil {
				fmt.Printf("  Arguments: %v\n", event.Tool.Arguments)
			}
			// In a real application, you would:
			// 1. Execute the tool (e.g., call weather API)
			// 2. Add the result to messages
			// 3. Call ChatCompletion again with the tool result

		case llm.EventTypeContentDone:
			fmt.Println("[Complete]")

		case llm.EventTypeError:
			fmt.Printf("Error: %v\n", event.Error)
		}
	}
}

func exampleErrorHandling(client *llm.Client, ctx context.Context) {
	// Intentionally use an invalid API key to trigger an error
	badCfg := &config.Config{
		OpenAIAPIKey: "invalid-key",
		Model:        "gpt-4o",
	}
	badClient := llm.NewClient(badCfg)

	messages := []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleUser,
			Content: "Hello!",
		},
	}

	opts := llm.ChatCompletionOptions{
		Stream:     true,
		MaxRetries: 2, // Will retry twice before giving up
		Timeout:    5 * time.Second,
	}

	fmt.Println("Testing error handling with invalid API key...")
	fmt.Println("Max retries: 2 (with exponential backoff)")
	fmt.Println("Expected behavior: Should retry twice, then return error")

	events := badClient.ChatCompletion(ctx, messages, opts)
	retryCount := 0

	for event := range events {
		switch event.Type {
		case llm.EventTypeError:
			retryCount++
			fmt.Printf("Error (attempt %d): %v\n", retryCount, event.Error)
		}
	}

	fmt.Println("Error handling test complete!")
}
