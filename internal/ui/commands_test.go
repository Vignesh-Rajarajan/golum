package ui

import (
	"fmt"
	"strings"
	"testing"
)

func TestHandleSlashCommand_PassesThroughOrdinaryPrompts(t *testing.T) {
	m := newTestModel(nil, false)
	for _, text := range []string{"hello", "what is /etc/passwd?", "  fix the bug  "} {
		if _, handled := m.handleSlashCommand(text); handled {
			t.Fatalf("%q must not be treated as a command", text)
		}
	}
}

func TestHandleSlashCommand_UnknownReportsError(t *testing.T) {
	m := newTestModel(nil, false)
	_, handled := m.handleSlashCommand("/nope")
	if !handled {
		t.Fatal("expected slash input to be handled")
	}
	if len(m.messages) != 1 || m.messages[0].Role != RoleError {
		t.Fatalf("expected an error message, got %+v", m.messages)
	}
	if !strings.Contains(m.messages[0].Content, "/nope") {
		t.Fatalf("error should name the command: %q", m.messages[0].Content)
	}
}

func TestHandleSlashCommand_Help(t *testing.T) {
	m := newTestModel(nil, false)
	_, handled := m.handleSlashCommand("/help")
	if !handled {
		t.Fatal("expected /help to be handled")
	}
	if len(m.messages) != 1 || m.messages[0].Role != RoleSystem {
		t.Fatalf("expected a system message, got %+v", m.messages)
	}
	if !strings.Contains(m.messages[0].Content, "/compact") {
		t.Fatal("help should mention /compact")
	}
}

func TestHandleSlashCommand_ContextWithoutHarness(t *testing.T) {
	m := newTestModel(nil, false)
	// No harness is configured in the test model; the command must degrade
	// gracefully rather than panic.
	_, handled := m.handleSlashCommand("/context")
	if !handled {
		t.Fatal("expected /context to be handled")
	}
	if len(m.messages) != 1 {
		t.Fatalf("expected one message, got %d", len(m.messages))
	}
}

func TestUpdate_CompactDoneMsg_RendersSummary(t *testing.T) {
	m := newTestModel(nil, true)
	updated, _ := m.Update(CompactDoneMsg{Summary: "Compacted (summary): 900 → 300 tokens."})
	got := asModel(t, updated)

	if got.streaming {
		t.Fatal("expected streaming=false after compaction finishes")
	}
	if len(got.messages) != 1 || got.messages[0].Role != RoleSystem {
		t.Fatalf("expected a system message, got %+v", got.messages)
	}
	if !strings.Contains(got.messages[0].Content, "300 tokens") {
		t.Fatalf("summary not rendered: %q", got.messages[0].Content)
	}
}

func TestUpdate_CompactDoneMsg_RendersError(t *testing.T) {
	m := newTestModel(nil, true)
	updated, _ := m.Update(CompactDoneMsg{Err: fmt.Errorf("boom")})
	got := asModel(t, updated)

	if got.streaming {
		t.Fatal("expected streaming=false after a failed compaction")
	}
	if len(got.messages) != 1 || got.messages[0].Role != RoleError {
		t.Fatalf("expected an error message, got %+v", got.messages)
	}
}

func TestHandleSlashCommand_ReindexWithoutMemory(t *testing.T) {
	m := newTestModel(nil, false)
	// The test model has no store; the command must report that, not panic.
	cmd, handled := m.handleSlashCommand("/reindex")
	if !handled {
		t.Fatal("expected /reindex to be handled")
	}
	if cmd != nil {
		t.Fatal("no work should be scheduled without a memory store")
	}
	if len(m.messages) != 1 || m.messages[0].Role != RoleError {
		t.Fatalf("expected an error message, got %+v", m.messages)
	}
}

func TestHandleSlashCommand_MemoryWithoutStore(t *testing.T) {
	m := newTestModel(nil, false)
	cmd, handled := m.handleSlashCommand("/memory something")
	if !handled {
		t.Fatal("expected /memory to be handled")
	}
	if cmd != nil {
		t.Fatal("no work should be scheduled without a memory store")
	}
	if len(m.messages) != 1 || m.messages[0].Role != RoleError {
		t.Fatalf("expected an error message, got %+v", m.messages)
	}
}

func TestHandleSlashCommand_HelpListsEveryCommand(t *testing.T) {
	m := newTestModel(nil, false)
	m.handleSlashCommand("/help")
	help := m.messages[0].Content
	// A command that exists but is not discoverable may as well not exist.
	for _, want := range []string{"/compact", "/context", "/sessions", "/reindex", "/memory"} {
		if !strings.Contains(help, want) {
			t.Errorf("help does not mention %s:\n%s", want, help)
		}
	}
}

func TestUpdate_ReindexDoneMsg(t *testing.T) {
	m := newTestModel(nil, false)
	updated, _ := m.Update(ReindexDoneMsg{Count: 42})
	got := asModel(t, updated)
	if len(got.messages) != 1 || got.messages[0].Role != RoleSystem {
		t.Fatalf("expected a system message, got %+v", got.messages)
	}
	if !strings.Contains(got.messages[0].Content, "42") {
		t.Fatalf("count not rendered: %q", got.messages[0].Content)
	}
}

func TestUpdate_ReindexDoneMsg_Error(t *testing.T) {
	m := newTestModel(nil, false)
	updated, _ := m.Update(ReindexDoneMsg{Err: fmt.Errorf("disk on fire")})
	got := asModel(t, updated)
	if len(got.messages) != 1 || got.messages[0].Role != RoleError {
		t.Fatalf("expected an error message, got %+v", got.messages)
	}
}

func TestUpdate_MemoryReportMsg(t *testing.T) {
	m := newTestModel(nil, false)
	updated, _ := m.Update(MemoryReportMsg{Report: "Memories (2 shown...)"})
	got := asModel(t, updated)
	if len(got.messages) != 1 || got.messages[0].Role != RoleSystem {
		t.Fatalf("expected a system message, got %+v", got.messages)
	}
}
