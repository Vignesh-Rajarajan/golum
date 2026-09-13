package harnesstest

import (
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/sashabaranov/go-openai"
)

// AssertBalanced fails if any assistant tool call lacks a matching result.
func AssertBalanced(t *testing.T, sess session.Session) {
	t.Helper()
	msgs, err := sess.BuildContext()
	if err != nil {
		t.Fatal(err)
	}
	pending := map[string]bool{}
	for _, m := range msgs {
		if m.Role == openai.ChatMessageRoleAssistant {
			for _, tc := range m.ToolCalls {
				pending[tc.ID] = true
			}
		}
		if m.Role == openai.ChatMessageRoleTool {
			if !pending[m.ToolCallID] {
				t.Fatalf("tool result for unknown id %q", m.ToolCallID)
			}
			delete(pending, m.ToolCallID)
		}
	}
	if len(pending) > 0 {
		t.Fatalf("unbalanced tool_calls without results: %v", pending)
	}
}

// DuplicateMessages reports entry IDs that appear more than once.
func DuplicateMessages(entries []session.Entry) []string {
	seen := map[string]int{}
	var dups []string
	for _, e := range entries {
		seen[e.ID]++
		if seen[e.ID] == 2 {
			dups = append(dups, e.ID)
		}
	}
	return dups
}
