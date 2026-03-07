package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/sashabaranov/go-openai"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Printf("Warning: .env file not found, using environment variables")
		cfg = &config.Config{
			OpenAIAPIKey:     os.Getenv("OPENAI_API_KEY"),
			OpenRouterAPIKey: os.Getenv("OPENROUTER_API_KEY"),
			BaseURL:          "https://api.openai.com/v1",
			Model:            "gpt-4o",
		}
	}

	client := llm.NewClient(cfg)

	ctx := context.Background()

	messages := []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleUser,
			Content: "Hello! Can you help me with something?",
		},
	}

	fmt.Println("Streaming response:")
	stream := client.Stream(ctx, messages)

	for chunk := range stream {
		if chunk.Error != nil {
			log.Printf("Error: %v", chunk.Error)
			break
		}
		if chunk.Done {
			fmt.Println("\n[Done]")
			break
		}
		fmt.Print(chunk.Content)
	}
}
