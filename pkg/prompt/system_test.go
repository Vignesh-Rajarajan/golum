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

func TestGetSystemPrompt_NoTools_ExcludesToolLanguage(t *testing.T) {
	out := GetSystemPrompt(PromptConfig{CWD: "/tmp/proj"}, nil, nil)

	if !strings.Contains(out, "no tool or shell execution") {
		t.Fatal("expected no-tools identity section")
	}
	if !strings.Contains(out, "does not execute tools or shell commands") {
		t.Fatal("expected no-tools environment tail")
	}
	if !strings.Contains(out, "You cannot read files from disk in this client") {
		t.Fatal("expected no-tools AGENTS.md section")
	}
	if strings.Contains(out, "Emit function calls to run terminal commands") {
		t.Fatal("did not expect tools-enabled identity language when tools are nil")
	}
	if strings.Contains(out, "# Tool Usage Guidelines") {
		t.Fatal("did not expect tool guidelines section when tools are nil")
	}
}

func TestGetSystemPrompt_WithTools_IncludesToolLanguage(t *testing.T) {
	tools := []llm.Tool{
		{Type: "function", Function: llm.ToolFunction{Name: "shell", Description: "Run a shell command"}},
	}
	out := GetSystemPrompt(PromptConfig{CWD: "/tmp/proj"}, nil, tools)

	if !strings.Contains(out, "Emit function calls to run terminal commands") {
		t.Fatal("expected tools-enabled identity section")
	}
	if !strings.Contains(out, "The user has granted you access to run tools") {
		t.Fatal("expected tools-enabled environment tail")
	}
	if !strings.Contains(out, "# AGENTS.md Specification") {
		t.Fatal("expected full AGENTS.md section when tools are enabled")
	}
	if strings.Contains(out, "no tool or shell execution") {
		t.Fatal("did not expect no-tools identity language when tools are provided")
	}
}

func TestGetSystemPrompt_IncludesDeveloperUserAndMemorySections(t *testing.T) {
	memory := "User prefers tabs over spaces."
	out := GetSystemPrompt(PromptConfig{
		CWD:                   "/tmp/proj",
		DeveloperInstructions: "Always run `go vet` before finishing.",
		UserInstructions:      "Keep responses under 5 lines.",
	}, &memory, nil)

	if !strings.Contains(out, "# Project Instructions") || !strings.Contains(out, "go vet") {
		t.Fatal("expected developer instructions section")
	}
	if !strings.Contains(out, "# User Instructions") || !strings.Contains(out, "under 5 lines") {
		t.Fatal("expected user instructions section")
	}
	if !strings.Contains(out, "# Remembered Context") || !strings.Contains(out, "tabs over spaces") {
		t.Fatal("expected memory section")
	}
}

func TestGetSystemPrompt_BlankInstructionsAndMemoryOmitted(t *testing.T) {
	blank := "   "
	out := GetSystemPrompt(PromptConfig{
		CWD:                   "/tmp/proj",
		DeveloperInstructions: "   ",
		UserInstructions:      "",
	}, &blank, nil)

	if strings.Contains(out, "# Project Instructions") {
		t.Fatal("blank developer instructions should be omitted")
	}
	if strings.Contains(out, "# User Instructions") {
		t.Fatal("empty user instructions should be omitted")
	}
	if strings.Contains(out, "# Remembered Context") {
		t.Fatal("blank memory should be omitted")
	}
}

func TestGetIdentitySection_toolsEnabledVsDisabled(t *testing.T) {
	if got := getIdentitySection(false); !strings.Contains(got, "no tool or shell execution") {
		t.Fatalf("expected no-tools identity text, got %q", got)
	}
	if got := getIdentitySection(true); !strings.Contains(got, "Emit function calls") {
		t.Fatalf("expected tools-enabled identity text, got %q", got)
	}
}

func TestGetEnvironmentSection_toolsEnabledVsDisabled(t *testing.T) {
	cfg := PromptConfig{CWD: "/tmp/proj"}
	if got := getEnvironmentSection(cfg, false); !strings.Contains(got, "does not execute tools") {
		t.Fatalf("expected no-tools environment tail, got %q", got)
	}
	if got := getEnvironmentSection(cfg, true); !strings.Contains(got, "granted you access to run tools") {
		t.Fatalf("expected tools-enabled environment tail, got %q", got)
	}
}

func TestGetEnvironmentSection_defaultsCWDWhenEmpty(t *testing.T) {
	got := getEnvironmentSection(PromptConfig{}, false)
	if !strings.Contains(got, "(unknown)") {
		t.Fatalf("expected (unknown) placeholder for empty CWD, got %q", got)
	}
}

func TestGetAgentsMDSection_toolsEnabledVsDisabled(t *testing.T) {
	if got := getAgentsMDSection(false); !strings.Contains(got, "cannot read files from disk") {
		t.Fatalf("expected no-tools AGENTS.md text, got %q", got)
	}
	if got := getAgentsMDSection(true); !strings.Contains(got, "# AGENTS.md Specification") {
		t.Fatalf("expected full AGENTS.md spec text, got %q", got)
	}
}

func TestGetOperationalSection_toolsEnabledVsDisabled(t *testing.T) {
	if got := getOperationalSection(false); !strings.Contains(got, "Answer first") {
		t.Fatalf("expected no-tools operational text, got %q", got)
	}
	if got := getOperationalSection(true); !strings.Contains(got, "Primary Workflows") {
		t.Fatalf("expected tools-enabled operational text, got %q", got)
	}
}

func TestTruncateDesc_truncatesLongDescription(t *testing.T) {
	long := strings.Repeat("a", 150)
	got := truncateDesc(long, 100)
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected truncated description to end with ..., got %q", got)
	}
	if len([]rune(got)) != 103 {
		t.Fatalf("expected 100 runes + ellipsis, got %d runes", len([]rune(got)))
	}
}

func TestTruncateDesc_keepsShortDescriptionUnchanged(t *testing.T) {
	short := "Read a file"
	if got := truncateDesc(short, 100); got != short {
		t.Fatalf("expected short description unchanged, got %q", got)
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
