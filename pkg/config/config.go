package config

import (
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	OpenAIAPIKey     string
	OpenRouterAPIKey string
	BaseURL          string
	Model            string
	// ContextWindow is the model context size in tokens (for compression heuristics).
	// Zero means unset; use ContextWindowOrDefault().
	ContextWindow int
	// StreamTimeout bounds how long a single chat completion stream may run (HTTP + first token + body).
	// Zero means unset; use StreamTimeoutOrDefault(). Override with GOLUM_STREAM_TIMEOUT (e.g. 10m, 300s).
	StreamTimeout time.Duration
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
		ContextWindow:    envInt("GOLUM_CONTEXT_WINDOW", 0),
		StreamTimeout:    envDuration("GOLUM_STREAM_TIMEOUT", 0),
	}

	return cfg, nil
}

// StreamTimeoutOrDefault returns StreamTimeout when set, otherwise 10 minutes.
func (c *Config) StreamTimeoutOrDefault() time.Duration {
	if c != nil && c.StreamTimeout > 0 {
		return c.StreamTimeout
	}
	return 10 * time.Minute
}

func envDuration(key string, defaultValue time.Duration) time.Duration {
	s := os.Getenv(key)
	if s == "" {
		return defaultValue
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return defaultValue
	}
	return d
}

// ContextWindowOrDefault returns ContextWindow when set, otherwise a sensible default
// for compression checks (e.g. NeedsCompression).
func (c *Config) ContextWindowOrDefault() int {
	if c != nil && c.ContextWindow > 0 {
		return c.ContextWindow
	}
	return 128_000
}

func envInt(key string, defaultValue int) int {
	s := os.Getenv(key)
	if s == "" {
		return defaultValue
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultValue
	}
	return v
}

func getEnvWithDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
