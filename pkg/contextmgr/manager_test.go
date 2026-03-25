package contextmgr

import (
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/sashabaranov/go-openai"
)

func TestContextManagerGetMessages(t *testing.T) {
	cfg := &config.Config{Model: "gpt-4", ContextWindow: 100_000}
	pc := prompt.PromptConfig{CWD: "/tmp"}
	m := NewContextManager(cfg, pc, nil, nil)
	m.AddUserMessage("hi")
	msgs := m.GetMessages()
	if len(msgs) < 2 {
		t.Fatalf("expected system + user, got %d", len(msgs))
	}
	if msgs[0]["role"] != openai.ChatMessageRoleSystem {
		t.Fatalf("first message should be system")
	}
	if msgs[1]["role"] != openai.ChatMessageRoleUser {
		t.Fatalf("second message should be user")
	}
}

func TestNeedsCompression(t *testing.T) {
	cfg := &config.Config{Model: "gpt-4", ContextWindow: 100}
	m := NewContextManager(cfg, prompt.PromptConfig{}, nil, nil)
	m.SetLatestUsage(TokenUsage{TotalTokens: 81})
	if !m.NeedsCompression() {
		t.Fatal("81 should exceed 80% of 100")
	}
	m.SetLatestUsage(TokenUsage{TotalTokens: 80})
	if m.NeedsCompression() {
		t.Fatal("80 should not exceed 80% of 100")
	}
}

func TestTokenUsageAdd(t *testing.T) {
	var u TokenUsage
	u.Add(TokenUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3})
	u.Add(TokenUsage{TotalTokens: 7})
	if u.TotalTokens != 10 {
		t.Fatalf("got %d", u.TotalTokens)
	}
}

func TestPruneToolOutputsTooFewUsers(t *testing.T) {
	cfg := &config.Config{Model: "gpt-4"}
	m := NewContextManager(cfg, prompt.PromptConfig{}, nil, nil)
	m.AddUserMessage("only one user")
	m.AddToolResult("call-1", strings.Repeat("x", 1000))
	if n := m.PruneToolOutputs(); n != 0 {
		t.Fatalf("expected 0 pruned, got %d", n)
	}
}

func TestReplaceWithSummary(t *testing.T) {
	cfg := &config.Config{Model: "gpt-4"}
	m := NewContextManager(cfg, prompt.PromptConfig{CWD: "."}, nil, nil)
	m.AddUserMessage("before")
	m.ReplaceWithSummary("did the thing")
	if m.MessageCount() != 3 {
		t.Fatalf("expected 3 messages after summary, got %d", m.MessageCount())
	}
}

func TestTokenUsageFromMeta(t *testing.T) {
	u := TokenUsageFromMeta(map[string]string{
		"prompt_tokens":     "10",
		"completion_tokens": "20",
		"total_tokens":      "30",
	})
	if u.PromptTokens != 10 || u.CompletionTokens != 20 || u.TotalTokens != 30 {
		t.Fatalf("got %+v", u)
	}
}

func TestClear(t *testing.T) {
	cfg := &config.Config{Model: "gpt-4"}
	m := NewContextManager(cfg, prompt.PromptConfig{}, nil, nil)
	m.AddUserMessage("x")
	m.Clear()
	if m.MessageCount() != 0 {
		t.Fatal("expected empty")
	}
}
