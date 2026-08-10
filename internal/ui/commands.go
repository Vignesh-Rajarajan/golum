package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/Vignesh-Rajarajan/golum/pkg/applog"
	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/memory"
)

// CompactDoneMsg reports the outcome of a manual /compact.
type CompactDoneMsg struct {
	Summary string
	Err     error
}

// handleSlashCommand intercepts local commands that never reach the model.
// Returns handled=false for ordinary prompts.
func (m *Model) handleSlashCommand(text string) (tea.Cmd, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return nil, false
	}
	cmd, rest, _ := strings.Cut(trimmed[1:], " ")
	rest = strings.TrimSpace(rest)

	switch cmd {
	case "clear":
		// Clears the screen and starts a fresh conversation, same as Ctrl+L.
		// The previous session is not deleted — it stays on disk and remains
		// resumable through /sessions.
		m.copyStatus = ""
		m.selectionMode = false
		m.selectableText = ""
		m.selectableLines = nil
		m.selectionAnchor = selectionPos{}
		m.selectionCursor = selectionPos{}
		if m.harness != nil {
			m.startNewSession()
		} else {
			m.messages = nil
			m.todosPanel = ""
		}
		return nil, true
	case "compact":
		return m.compactCmd(), true
	case "sessions", "resume":
		return m.openPicker(), true
	case "reindex":
		return m.reindexCmd(), true
	case "memory":
		return m.memoryReportCmd(rest), true
	case "context":
		m.messages = append(m.messages, Message{
			Role:    RoleSystem,
			Content: m.contextReport(),
		})
		return nil, true
	case "help":
		m.messages = append(m.messages, Message{
			Role:    RoleSystem,
			Content: slashHelp(),
		})
		return nil, true
	default:
		m.messages = append(m.messages, Message{
			Role:    RoleError,
			Content: fmt.Sprintf("Unknown command %q. Try /help.", "/"+cmd+ifRest(rest)),
		})
		return nil, true
	}
}

func ifRest(rest string) string {
	if rest == "" {
		return ""
	}
	return " " + rest
}

func (m *Model) contextReport() string {
	if m.harness == nil {
		return "No harness."
	}
	sess := m.harness.Session()
	if sess == nil || sess.ContextManager() == nil {
		return "No session."
	}
	cm := sess.ContextManager()
	used := cm.EstimateContextTokens()
	limit := cm.ContextLimit()
	pct := 0.0
	if limit > 0 {
		pct = float64(used) / float64(limit) * 100
	}
	return fmt.Sprintf(
		"Context: ~%d / %d tokens (%.0f%%)\nMessages: %d\nSession: %s\nCompaction triggers above %.0f%%.",
		used, limit, pct, cm.MessageCount(), sess.ID(), contextmgr.CompactionThreshold*100)
}

// ReindexDoneMsg reports the outcome of a manual /reindex.
type ReindexDoneMsg struct {
	Count int
	Err   error
}

// MemoryReportMsg carries the result of /memory.
type MemoryReportMsg struct {
	Report string
	Err    error
}

// reindexCmd rebuilds semantic memory off the UI goroutine — walking and
// parsing the tree is far too slow to do inside Update().
func (m *Model) reindexCmd() tea.Cmd {
	if m.memory == nil || m.harness == nil {
		m.messages = append(m.messages, Message{
			Role:    RoleError,
			Content: "Memory is unavailable (no session database).",
		})
		return nil
	}
	m.messages = append(m.messages, Message{
		Role:    RoleSystem,
		Content: "Indexing project structure…",
	})
	store := m.memory
	root := m.harness.Env().CWD()
	ctx := m.ctx
	return func() tea.Msg {
		n, err := memory.NewGoRepoIndex(store).Reindex(ctx, root)
		return ReindexDoneMsg{Count: n, Err: err}
	}
}

// autoIndexCmd builds the project map on first run.
//
// Without this, semantic memory stays empty until someone happens to discover
// /reindex — so the tier would exist but never actually help. Indexing is pure
// parsing (no model calls), runs off the UI goroutine as a Bubble Tea command,
// and skips itself once records exist.
func (m *Model) autoIndexCmd() tea.Cmd {
	if m.memory == nil || m.harness == nil {
		return nil
	}
	store := m.memory
	root := m.harness.Env().CWD()
	ctx := m.ctx
	return func() tea.Msg {
		existing, err := store.List(ctx, memory.TierSemantic, 1)
		if err != nil || len(existing) > 0 {
			return nil // already indexed, or the store is unavailable
		}
		n, err := memory.NewGoRepoIndex(store).Reindex(ctx, root)
		if err != nil {
			applog.Printf("ui: auto-index failed: %v", err)
			return nil // silent: never let indexing derail startup
		}
		applog.Printf("ui: auto-indexed %d project entries", n)
		return nil
	}
}

// memoryReportCmd lists or searches stored memories.
//
// A bare /memory deliberately excludes the semantic tier. Semantic memory is a
// derived index of the codebase rebuilt by /reindex — dozens of
// "package:foo"/"file:bar.go" rows that would bury the handful of things the
// agent actually remembered. It stays reachable via `/memory semantic` and is
// always searched by `/memory <query>`.
func (m *Model) memoryReportCmd(arg string) tea.Cmd {
	if m.memory == nil {
		m.messages = append(m.messages, Message{
			Role:    RoleError,
			Content: "Memory is unavailable (no session database).",
		})
		return nil
	}
	store := m.memory
	ctx := m.ctx
	arg = strings.TrimSpace(arg)

	return func() tea.Msg {
		// "/memory <tier>" browses one tier; anything else is a search.
		if arg != "" && memory.ValidTier(arg) {
			recs, err := store.List(ctx, memory.Tier(arg), 40)
			if err != nil {
				return MemoryReportMsg{Err: err}
			}
			return MemoryReportMsg{Report: formatMemoryList(
				fmt.Sprintf("%s memories", arg), recs, "")}
		}
		if arg != "" {
			recs, err := store.Search(ctx, "", arg, 20)
			if err != nil {
				return MemoryReportMsg{Err: err}
			}
			return MemoryReportMsg{Report: formatMemoryList(
				fmt.Sprintf("Search %q", arg), recs, "")}
		}

		var recalled []memory.Record
		for _, tier := range []memory.Tier{memory.TierProcedural, memory.TierEpisodic} {
			recs, err := store.List(ctx, tier, 15)
			if err != nil {
				return MemoryReportMsg{Err: err}
			}
			recalled = append(recalled, recs...)
		}
		// Report the index size rather than listing it.
		semantic, err := store.List(ctx, memory.TierSemantic, 1000)
		if err != nil {
			return MemoryReportMsg{Err: err}
		}
		footer := fmt.Sprintf(
			"\n\n%d semantic entries indexed from the project — /memory semantic to browse, /reindex to rebuild.",
			len(semantic))
		return MemoryReportMsg{Report: formatMemoryList("Memories", recalled, footer)}
	}
}

// formatMemoryList renders records one per line, newest first.
func formatMemoryList(title string, recs []memory.Record, footer string) string {
	if len(recs) == 0 {
		if footer != "" {
			return "Nothing remembered yet." + footer
		}
		return "No matching memories."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d):\n", title, len(recs))
	for _, r := range recs {
		// Show the content, not just the key — a key like "style" says nothing
		// about what was actually remembered.
		label := firstLine(r.Content)
		if r.Key != "" {
			label = r.Key + ": " + label
		}
		fmt.Fprintf(&b, "  [%s/%s] %s\n", r.Tier, r.Scope, truncateRunes(label, 100))
	}
	return strings.TrimRight(b.String(), "\n") + footer
}

// compactCmd runs compaction off the UI goroutine; Bubble Tea must never block.
func (m *Model) compactCmd() tea.Cmd {
	if m.harness == nil {
		return nil
	}
	m.messages = append(m.messages, Message{
		Role:    RoleSystem,
		Content: "Compacting context…",
	})
	m.streaming = true
	h := m.harness
	ctx := m.ctx
	return func() tea.Msg {
		res, err := h.Compact(ctx, nil)
		if err != nil {
			return CompactDoneMsg{Err: err}
		}
		return CompactDoneMsg{Summary: fmt.Sprintf(
			"Compacted (%s): %d → %d tokens, %d entries folded, %d tool results pruned.",
			res.Tier, res.BeforeTokens, res.AfterTokens, res.FoldedEntries, res.PrunedResults)}
	}
}
