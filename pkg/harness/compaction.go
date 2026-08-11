package harness

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/applog"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/Vignesh-Rajarajan/golum/pkg/tokenizer"
	"github.com/sashabaranov/go-openai"
)

// CompactionCooldown is the minimum gap between automatic compactions. Without
// it a session that stays above the threshold after compacting would compact
// again on every turn, burning a summarization call each time.
const CompactionCooldown = 30 * time.Second

// KeepFraction is the share of the context window that surviving recent history
// is allowed to occupy after a compaction.
const KeepFraction = 0.35

// CompactionTier identifies how much work a compaction had to do.
type CompactionTier int

const (
	// TierNone means nothing needed doing.
	TierNone CompactionTier = iota
	// TierPrune reclaimed enough by clearing old tool output alone — no LLM call.
	TierPrune
	// TierSummary folded a span of history into a new summary.
	TierSummary
	// TierIncremental merged a span into an existing summary.
	TierIncremental
)

func (t CompactionTier) String() string {
	switch t {
	case TierPrune:
		return "prune"
	case TierSummary:
		return "summary"
	case TierIncremental:
		return "incremental"
	default:
		return "none"
	}
}

// CompactionResult describes what a compaction did.
type CompactionResult struct {
	Tier          CompactionTier
	BeforeTokens  int
	AfterTokens   int
	PrunedResults int
	FoldedEntries int
}

// Compactor runs tiered context compaction for a session.
//
// Tier 0 clears stale tool output (cheap, no model call). Only if that is not
// enough does it summarize a prefix of history, choosing the cut point so that
// no tool call is ever separated from its result.
type Compactor struct {
	client *llm.Client

	mu     sync.Mutex
	lastAt time.Time
}

// NewCompactor creates a Compactor using client for summarization.
func NewCompactor(client *llm.Client) *Compactor {
	return &Compactor{client: client}
}

// ShouldCompact reports whether sess has crossed the threshold and the cooldown
// has elapsed.
func (c *Compactor) ShouldCompact(sess session.Session) bool {
	if c == nil || sess == nil {
		return false
	}
	cm := sess.ContextManager()
	if cm == nil || !cm.ShouldCompact() {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Since(c.lastAt) >= CompactionCooldown
}

// MaybeCompact compacts only when ShouldCompact allows it.
func (c *Compactor) MaybeCompact(ctx context.Context, sess session.Session, emit func(AgentEvent)) (CompactionResult, error) {
	if !c.ShouldCompact(sess) {
		return CompactionResult{Tier: TierNone}, nil
	}
	return c.Compact(ctx, sess, emit)
}

// Compact runs compaction unconditionally (the manual path, and the recovery
// path after a context-overflow error).
func (c *Compactor) Compact(ctx context.Context, sess session.Session, emit func(AgentEvent)) (CompactionResult, error) {
	return c.compact(ctx, sess, emit, "")
}

func (c *Compactor) compact(ctx context.Context, sess session.Session, emit func(AgentEvent), resultEntryID string) (CompactionResult, error) {
	if sess == nil {
		return CompactionResult{}, fmt.Errorf("no session")
	}
	cm := sess.ContextManager()
	if cm == nil {
		return CompactionResult{}, fmt.Errorf("no context manager")
	}
	if emit == nil {
		emit = func(AgentEvent) {}
	}

	c.mu.Lock()
	c.lastAt = time.Now()
	c.mu.Unlock()

	res := CompactionResult{BeforeTokens: cm.EstimateContextTokens()}
	emit(AgentEvent{Type: EventCompactionStart})

	// Tier 0 — reclaim old tool output without a model call.
	res.PrunedResults = cm.PruneToolOutputs()
	if res.PrunedResults > 0 && !cm.ShouldCompact() {
		res.Tier = TierPrune
		res.AfterTokens = cm.EstimateContextTokens()
		applog.Printf("compaction: tier=prune pruned=%d before=%d after=%d",
			res.PrunedResults, res.BeforeTokens, res.AfterTokens)
		emit(AgentEvent{Type: EventCompactionDone, Compaction: &res})
		return res, nil
	}

	// Tiers 1/2 — summarize a prefix of history.
	derived := sess.ContextEntries()
	cuts := session.FindValidEntryCutPoints(derived)
	if len(cuts) == 0 {
		// Nothing can be folded without orphaning a tool result. Pruning is all
		// we can safely do; report it rather than corrupting the context.
		res.Tier = TierPrune
		res.AfterTokens = cm.EstimateContextTokens()
		applog.Printf("compaction: no valid cut point; pruned=%d", res.PrunedResults)
		emit(AgentEvent{Type: EventCompactionDone, Compaction: &res})
		return res, nil
	}

	keepBudget := int(float64(cm.ContextLimit()) * KeepFraction)
	cut := chooseEntryCut(derived, cuts, keepBudget)
	if cut <= 0 {
		res.Tier = TierPrune
		res.AfterTokens = cm.EstimateContextTokens()
		emit(AgentEvent{Type: EventCompactionDone, Compaction: &res})
		return res, nil
	}

	previous := cm.LastSummary()
	summary, err := c.summarize(ctx, derived[:cut], previous)
	if err != nil {
		emit(AgentEvent{Type: EventCompactionDone, Compaction: &res, Err: err})
		return res, err
	}

	// Snapshot so a failed rebuild can be rolled back rather than sending a
	// context we know is malformed.
	var appendErr error
	if resultEntryID != "" {
		_, appendErr = sess.AppendProvisioned(session.ProvisionedEntry{
			ID: resultEntryID, Kind: session.EntryCompaction, Content: summary,
			Meta: map[string]any{session.MetaCutEntryID: derived[cut-1].ID},
		})
	} else {
		_, appendErr = sess.AppendCompactionAt(summary, derived[cut-1].ID)
	}
	if appendErr != nil {
		emit(AgentEvent{Type: EventCompactionDone, Compaction: &res, Err: appendErr})
		return res, appendErr
	}
	if err := sess.RebuildContext(); err != nil {
		emit(AgentEvent{Type: EventCompactionDone, Compaction: &res, Err: err})
		return res, err
	}
	if !cm.HasBalancedToolCalls() {
		err := fmt.Errorf("compaction produced an unbalanced context; aborting")
		applog.Printf("compaction: %v (cut=%d)", err, cut)
		emit(AgentEvent{Type: EventCompactionDone, Compaction: &res, Err: err})
		return res, err
	}

	res.Tier = TierSummary
	if previous != "" {
		res.Tier = TierIncremental
	}
	res.FoldedEntries = cut
	res.AfterTokens = cm.EstimateContextTokens()
	applog.Printf("compaction: tier=%s folded=%d before=%d after=%d",
		res.Tier, res.FoldedEntries, res.BeforeTokens, res.AfterTokens)
	emit(AgentEvent{Type: EventCompactionDone, Compaction: &res})
	return res, nil
}

// chooseEntryCut picks the cut that keeps as much recent history as fits in
// keepBudget. cuts is ascending, so the tail shrinks as the index grows and the
// first fitting cut preserves the most context.
func chooseEntryCut(entries []Entry, cuts []int, keepBudget int) int {
	for _, c := range cuts {
		if entryTokens(entries[c:]) <= keepBudget {
			return c
		}
	}
	return cuts[len(cuts)-1]
}

// Entry is re-exported locally for readability in this file.
type Entry = session.Entry

func entryTokens(entries []Entry) int {
	total := 0
	for i := range entries {
		total += tokenizer.EstimateTokens(entries[i].Content)
	}
	return total
}

// summarize asks the model to condense the given entries. When a previous
// summary exists it is folded in rather than discarded — the oldest history is
// no longer in context to re-read.
func (c *Compactor) summarize(ctx context.Context, entries []Entry, previous string) (string, error) {
	if c.client == nil {
		return "", fmt.Errorf("no llm client for summarization")
	}
	msgs := make([]openai.ChatCompletionMessage, 0, len(entries)+1)
	for i := range entries {
		if m, ok := entryToMessage(entries[i]); ok {
			msgs = append(msgs, m)
		}
	}
	instruction := prompt.GetCompressionPrompt()
	if strings.TrimSpace(previous) != "" {
		instruction = prompt.GetUpdateCompressionPrompt(previous)
	}
	msgs = append(msgs, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: instruction,
	})

	events := c.client.ChatCompletion(ctx, msgs, llm.ChatCompletionOptions{
		Stream:     false,
		MaxRetries: 2,
	})
	var b strings.Builder
	for ev := range events {
		switch ev.Type {
		case llm.EventTypeContentDelta:
			b.WriteString(ev.Content)
		case llm.EventTypeError:
			return "", ev.Error
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", fmt.Errorf("empty compaction summary")
	}
	return out, nil
}

// entryToMessage converts a session entry into an API message for summarization.
// Tool calls are flattened to text: the summarizer only needs to read what
// happened, and echoing tool_calls into a standalone request would require
// matching tool results that are not being sent.
func entryToMessage(e Entry) (openai.ChatCompletionMessage, bool) {
	switch e.Kind {
	case session.EntryUserMessage:
		return openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleUser, Content: e.Content}, true
	case session.EntryAssistantMessage:
		content := e.Content
		if calls := assistantCallSummary(e); calls != "" {
			content = strings.TrimSpace(content + "\n" + calls)
		}
		if strings.TrimSpace(content) == "" {
			return openai.ChatCompletionMessage{}, false
		}
		return openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleAssistant, Content: content}, true
	case session.EntryToolResult:
		return openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: "[tool result] " + e.Content}, true
	case session.EntryCompaction:
		return openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: "[earlier summary]\n" + e.Content}, true
	default:
		return openai.ChatCompletionMessage{}, false
	}
}

func assistantCallSummary(e Entry) string {
	if e.Meta == nil {
		return ""
	}
	raw, ok := e.Meta["tool_calls"]
	if !ok {
		return ""
	}
	calls, ok := raw.([]openai.ToolCall)
	if !ok || len(calls) == 0 {
		return ""
	}
	var b strings.Builder
	for _, tc := range calls {
		fmt.Fprintf(&b, "[called %s %s]\n", tc.Function.Name, tc.Function.Arguments)
	}
	return strings.TrimSpace(b.String())
}
