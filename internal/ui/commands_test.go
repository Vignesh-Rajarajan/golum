package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/memory"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
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
	for _, want := range []string{"/clear", "/compact", "/context", "/sessions", "/reindex", "/memory"} {
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

// ---- /memory listing ----------------------------------------------------

func memoryTestModel(t *testing.T) Model {
	t.Helper()
	dir := t.TempDir()
	store, err := session.OpenSQLiteStore(filepath.Join(dir, "g.db"),
		&config.Config{Model: "gpt-4o"}, prompt.PromptConfig{CWD: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	m := newTestModel(nil, false)
	m.memory = memory.NewStore(store.DB())
	m.ctx = context.Background()
	return m
}

// Regression: auto-indexing writes dozens of semantic "package:foo" records.
// A bare /memory listed everything by recency, so the derived index buried the
// handful of things actually remembered.
func TestMemoryReport_BareListingExcludesSemanticIndex(t *testing.T) {
	m := memoryTestModel(t)
	ctx := context.Background()

	for i := 0; i < 25; i++ {
		if _, err := m.memory.Put(ctx, memory.Record{
			Tier: memory.TierSemantic, Scope: memory.ScopeProject,
			Key: fmt.Sprintf("package:pkg%d", i), Content: "indexed structure",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.memory.Put(ctx, memory.Record{
		Tier: memory.TierProcedural, Scope: memory.ScopeUser,
		Key: "style", Content: "prefers table-driven tests",
	}); err != nil {
		t.Fatal(err)
	}

	cmd := m.memoryReportCmd("")
	if cmd == nil {
		t.Fatal("expected a command")
	}
	report := cmd().(MemoryReportMsg)
	if report.Err != nil {
		t.Fatal(report.Err)
	}
	if strings.Contains(report.Report, "package:pkg") {
		t.Fatalf("bare /memory must not dump the semantic index:\n%s", report.Report)
	}
	if !strings.Contains(report.Report, "prefers table-driven tests") {
		t.Fatalf("real memories must still be listed:\n%s", report.Report)
	}
	// It should still say the index exists, so it stays discoverable.
	if !strings.Contains(report.Report, "25 semantic entries") {
		t.Fatalf("expected a semantic index count:\n%s", report.Report)
	}
}

func TestMemoryReport_TierArgumentBrowsesThatTier(t *testing.T) {
	m := memoryTestModel(t)
	ctx := context.Background()
	if _, err := m.memory.Put(ctx, memory.Record{
		Tier: memory.TierSemantic, Scope: memory.ScopeProject,
		Key: "package:widget", Content: "widget structure",
	}); err != nil {
		t.Fatal(err)
	}

	report := m.memoryReportCmd("semantic")().(MemoryReportMsg)
	if report.Err != nil {
		t.Fatal(report.Err)
	}
	if !strings.Contains(report.Report, "package:widget") {
		t.Fatalf("/memory semantic should browse the index:\n%s", report.Report)
	}
}

func TestMemoryReport_QuerySearchesAllTiers(t *testing.T) {
	m := memoryTestModel(t)
	ctx := context.Background()
	if _, err := m.memory.Put(ctx, memory.Record{
		Tier: memory.TierSemantic, Scope: memory.ScopeProject,
		Content: "the retry backoff lives in chat_completion.go",
	}); err != nil {
		t.Fatal(err)
	}

	report := m.memoryReportCmd("backoff")().(MemoryReportMsg)
	if report.Err != nil {
		t.Fatal(report.Err)
	}
	if !strings.Contains(report.Report, "backoff") {
		t.Fatalf("an explicit query should still reach semantic memory:\n%s", report.Report)
	}
}

func TestMemoryReport_EmptyStoreIsFriendly(t *testing.T) {
	m := memoryTestModel(t)
	report := m.memoryReportCmd("")().(MemoryReportMsg)
	if report.Err != nil {
		t.Fatal(report.Err)
	}
	if !strings.Contains(report.Report, "Nothing remembered yet") {
		t.Fatalf("expected a friendly empty message, got:\n%s", report.Report)
	}
}

// ---- /clear -------------------------------------------------------------

func TestHandleSlashCommand_ClearEmptiesTheTranscript(t *testing.T) {
	m := newTestModel([]Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi there"},
		{Role: RoleToolResult, Content: "ran something"},
	}, false)
	m.todosPanel = "Todos:\n  [ ] something"
	m.copyStatus = "Copied selection"

	cmd, handled := m.handleSlashCommand("/clear")
	if !handled {
		t.Fatal("expected /clear to be handled")
	}
	if cmd != nil {
		t.Fatal("/clear should not schedule any work")
	}
	if len(m.messages) != 0 {
		t.Fatalf("expected an empty transcript, got %d messages", len(m.messages))
	}
	if m.todosPanel != "" || m.copyStatus != "" {
		t.Fatalf("expected panels cleared, todos=%q copy=%q", m.todosPanel, m.copyStatus)
	}
}

// clearTestModel builds a Model backed by a real store and harness, which is
// what /clear needs in order to swap in a new session.
func clearTestModel(t *testing.T) (Model, *session.SQLiteStore) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{Model: "gpt-4o"}
	store, err := session.OpenSQLiteStore(filepath.Join(dir, "g.db"),
		cfg, prompt.PromptConfig{CWD: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	env, err := execenv.NewOsExecutionEnv(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg, todos := tool.DefaultRegistry(nil)
	memStore := memory.NewStore(store.DB())
	active := &sessionRef{}
	tool.RegisterMemory(reg, memStore, active.id)

	sess, err := store.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	active.set(sess)

	h, err := harness.NewAgentHarness(harness.HarnessConfig{
		Config: cfg, Env: env, Registry: reg, Todos: todos,
		Session: sess, Memory: memStore, PromptCfg: prompt.PromptConfig{CWD: dir},
	})
	if err != nil {
		t.Fatal(err)
	}

	m := newTestModel(nil, false)
	m.cfg = cfg
	m.store = store
	m.memory = memStore
	m.active = active
	m.harness = h
	m.ctx = context.Background()
	return m, store
}

// /clear starts a fresh conversation, so the agent must no longer see the
// previous turns.
func TestHandleSlashCommand_ClearStartsNewSession(t *testing.T) {
	m, _ := clearTestModel(t)
	oldID := m.harness.Session().ID()

	if _, err := m.harness.Session().AppendUserMessage("remember this"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.harness.Session().AppendAssistantMessage("noted", nil); err != nil {
		t.Fatal(err)
	}
	m.messages = []Message{{Role: RoleUser, Content: "remember this"}}

	m.handleSlashCommand("/clear")

	if m.harness.Session().ID() == oldID {
		t.Fatal("/clear should swap in a new session")
	}
	if len(m.messages) != 0 {
		t.Fatalf("expected the transcript cleared, got %+v", m.messages)
	}
	msgs, err := m.harness.Session().BuildContext()
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "remember this") {
			t.Fatal("the new session must not carry the old conversation")
		}
	}
}

// Starting fresh must not destroy history — the old session stays resumable
// through /sessions.
func TestHandleSlashCommand_ClearPreservesOldSessionOnDisk(t *testing.T) {
	m, store := clearTestModel(t)
	oldID := m.harness.Session().ID()
	if _, err := m.harness.Session().AppendUserMessage("earlier work"); err != nil {
		t.Fatal(err)
	}

	m.handleSlashCommand("/clear")

	reopened, err := store.Open(context.Background(), oldID)
	if err != nil {
		t.Fatalf("previous session should still be resumable: %v", err)
	}
	if len(reopened.Entries()) != 1 {
		t.Fatalf("previous session lost entries: %d", len(reopened.Entries()))
	}
}

// Regression: the replacement harness was built without the memory store, so
// episodic digests silently stopped being recorded after the first /clear.
func TestStartNewSession_CarriesMemoryStoreForward(t *testing.T) {
	m, _ := clearTestModel(t)
	m.startNewSession()

	if _, err := m.harness.Session().AppendUserMessage("x"); err != nil {
		t.Fatal(err)
	}
	if m.memory == nil {
		t.Fatal("model lost its memory store")
	}
	// The memory tool must still be able to attribute writes to a session.
	if got := m.active.id(); got != m.harness.Session().ID() {
		t.Fatalf("memory tool would attribute to the wrong session: %q vs %q",
			got, m.harness.Session().ID())
	}
}

// Regression: the shared session handle was not repointed, so memories stored
// after a new session began were attributed to the previous one.
func TestStartNewSession_RepointsActiveSessionRef(t *testing.T) {
	m, _ := clearTestModel(t)
	oldID := m.active.id()

	m.startNewSession()

	if m.active.id() == oldID {
		t.Fatal("active session ref still points at the old session")
	}
	if m.active.id() != m.harness.Session().ID() {
		t.Fatal("active session ref does not match the harness session")
	}
}

func TestHandleSlashCommand_ClearOnEmptyScreenIsSafe(t *testing.T) {
	m := newTestModel(nil, false)
	if _, handled := m.handleSlashCommand("/clear"); !handled {
		t.Fatal("expected /clear to be handled")
	}
	if len(m.messages) != 0 {
		t.Fatal("expected the transcript to stay empty")
	}
}

func TestUpdate_ClearViaSubmitRedrawsEmpty(t *testing.T) {
	m := newTestModel([]Message{{Role: RoleAssistant, Content: "old output"}}, false)
	m.width, m.height, m.ready = 80, 24, true

	updated, _ := m.Update(InputSubmitMsg{Text: "/clear"})
	got := asModel(t, updated)

	if len(got.messages) != 0 {
		t.Fatalf("expected transcript cleared, got %+v", got.messages)
	}
	// The user's "/clear" itself must not be echoed as a chat message.
	for _, msg := range got.messages {
		if strings.Contains(msg.Content, "/clear") {
			t.Fatal("/clear should not appear in the transcript")
		}
	}
}
