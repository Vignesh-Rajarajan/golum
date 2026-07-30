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

func slashHelp() string {
	return "Commands:\n" +
		"  /compact       — summarize older turns to reclaim context now\n" +
		"  /context       — show context usage\n" +
		"  /sessions      — browse, resume, fork or delete saved sessions\n" +
		"  /reindex       — rebuild the project map used by semantic memory\n" +
		"  /memory [text] — list memories, or search them\n" +
		"  /help          — this list"
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
func (m *Model) memoryReportCmd(query string) tea.Cmd {
	if m.memory == nil {
		m.messages = append(m.messages, Message{
			Role:    RoleError,
			Content: "Memory is unavailable (no session database).",
		})
		return nil
	}
	store := m.memory
	ctx := m.ctx
	return func() tea.Msg {
		var (
			recs []memory.Record
			err  error
		)
		if strings.TrimSpace(query) == "" {
			recs, err = store.List(ctx, "", 20)
		} else {
			recs, err = store.Search(ctx, "", query, 20)
		}
		if err != nil {
			return MemoryReportMsg{Err: err}
		}
		if len(recs) == 0 {
			return MemoryReportMsg{Report: "No memories stored yet. Try /reindex, or ask me to remember something."}
		}
		var b strings.Builder
		byTier := map[memory.Tier]int{}
		for _, r := range recs {
			byTier[r.Tier]++
		}
		fmt.Fprintf(&b, "Memories (%d shown — procedural %d, episodic %d, semantic %d):\n",
			len(recs), byTier[memory.TierProcedural], byTier[memory.TierEpisodic], byTier[memory.TierSemantic])
		for _, r := range recs {
			label := r.Key
			if label == "" {
				label = firstLine(r.Content)
			}
			fmt.Fprintf(&b, "  [%s/%s] %s\n", r.Tier, r.Scope, truncateRunes(label, 90))
		}
		return MemoryReportMsg{Report: strings.TrimRight(b.String(), "\n")}
	}
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
