package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/Vignesh-Rajarajan/golum/internal/ui"
	"github.com/Vignesh-Rajarajan/golum/pkg/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{
			OpenAIAPIKey:     os.Getenv("OPENAI_API_KEY"),
			OpenRouterAPIKey: os.Getenv("OPENROUTER_API_KEY"),
			BaseURL:          os.Getenv("OPENAI_BASE_URL"),
			Model:            getEnvWithDefault("OPENAI_MODEL", "gpt-4o"),
		}
	}

	if cfg.OpenAIAPIKey == "" && cfg.OpenRouterAPIKey == "" {
		fmt.Println("Error: No API key found. Set OPENAI_API_KEY or OPENROUTER_API_KEY environment variable.")
		os.Exit(1)
	}

	m := ui.NewModel(cfg)
	p := tea.NewProgram(m)

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
}

func getEnvWithDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
