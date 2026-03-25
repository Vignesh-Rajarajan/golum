package llm

import (
	"context"

	"github.com/sashabaranov/go-openai"
)

type StreamChunk struct {
	Content string
	Done    bool
	Error   error
}

func (c *Client) Stream(ctx context.Context, messages []openai.ChatCompletionMessage) <-chan StreamChunk {
	ch := make(chan StreamChunk)

	go func() {
		defer close(ch)

		req := openai.ChatCompletionRequest{
			Model:    c.config.Model,
			Messages: messages,
			Stream:   true,
		}

		stream, err := c.client.CreateChatCompletionStream(ctx, req)
		if err != nil {
			ch <- StreamChunk{Error: err}
			return
		}
		defer stream.Close()

		for {
			response, err := stream.Recv()
			if err != nil {
				ch <- StreamChunk{Error: err}
				return
			}

			if len(response.Choices) == 0 {
				continue
			}
			choice := response.Choices[0]
			if choice.Delta.Content != "" {
				ch <- StreamChunk{Content: choice.Delta.Content}
			}
			if choice.FinishReason != "" {
				ch <- StreamChunk{Done: true}
				return
			}
		}
	}()

	return ch
}
