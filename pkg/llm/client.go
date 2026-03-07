package llm

import (
	"context"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/sashabaranov/go-openai"
)

type Client struct {
	client *openai.Client
	config *config.Config
}

func NewClient(cfg *config.Config) *Client {
	var client *openai.Client

	switch {
	case cfg.OpenRouterAPIKey != "":
		clientConfig := openai.DefaultConfig(cfg.OpenRouterAPIKey)
		clientConfig.BaseURL = "https://openrouter.ai/api/v1"
		client = openai.NewClientWithConfig(clientConfig)
	case cfg.OpenAIAPIKey != "":
		clientConfig := openai.DefaultConfig(cfg.OpenAIAPIKey)
		if cfg.BaseURL != "" {
			clientConfig.BaseURL = cfg.BaseURL
		}
		client = openai.NewClientWithConfig(clientConfig)
	default:
		client = openai.NewClient("")
	}

	return &Client{
		client: client,
		config: cfg,
	}
}

func (c *Client) Complete(ctx context.Context, messages []openai.ChatCompletionMessage) (string, error) {
	resp, err := c.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:    c.config.Model,
		Messages: messages,
	})
	if err != nil {
		return "", err
	}

	return resp.Choices[0].Message.Content, nil
}
