package prompt

import (
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
)

func TestGetSystemPrompt(t *testing.T) {
	cfg := PromptConfig{CWD: "/tmp/proj"}
	out := GetSystemPrompt(cfg, nil, nil)
	if !strings.Contains(out, "# Identity") {
		t.Fatal("expected Identity section")
	}
	if !strings.Contains(out, "/tmp/proj") {
		t.Fatal("expected CWD in environment section")
	}
}

func TestGetSystemPrompt_WithTools(t *testing.T) {
	tools := []llm.Tool{
		{Type: "function", Function: llm.ToolFunction{Name: "read_file", Description: "Read a file from disk"}},
		{Type: "function", Function: llm.ToolFunction{Name: "subagent_explore", Description: "Explore"}},
	}
	out := GetSystemPrompt(PromptConfig{CWD: "."}, nil, tools)
	if !strings.Contains(out, "read_file") || !strings.Contains(out, "Sub-Agents") {
		t.Fatal("expected tool guidelines for regular and subagent tools")
	}
}

func TestGetCompressionPrompt(t *testing.T) {
	if !strings.Contains(GetCompressionPrompt(), "ORIGINAL GOAL") {
		t.Fatal("expected compression template sections")
	}
}

func TestCreateLoopBreakerPrompt(t *testing.T) {
	s := CreateLoopBreakerPrompt("repeated grep")
	if !strings.Contains(s, "Loop Detected") || !strings.Contains(s, "repeated grep") {
		t.Fatal("expected loop notice content")
	}
}
