package config

import (
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	OpenAIAPIKey     string
	OpenRouterAPIKey string
	BaseURL          string
	Model            string
}

func Load() (*Config, error) {
	if err := godotenv.Load(); err != nil {
		return nil, err
	}

	cfg := &Config{
		OpenAIAPIKey:     os.Getenv("OPENAI_API_KEY"),
		OpenRouterAPIKey: os.Getenv("OPENROUTER_API_KEY"),
		BaseURL:          getEnvWithDefault("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		Model:            getEnvWithDefault("OPENAI_MODEL", "gpt-4o"),
	}

	return cfg, nil
}

func getEnvWithDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
