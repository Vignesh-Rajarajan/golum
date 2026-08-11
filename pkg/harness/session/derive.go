package session

import "github.com/Vignesh-Rajarajan/golum/pkg/prompt"

// MetaCutEntryID is the key under which a compaction entry records the last
// entry it folded into its summary.
const MetaCutEntryID = "cut_entry_id"

// DeriveContextEntries returns the entries that make up the live context.
//
// The entry log is append-only and never loses history, so a compaction entry
// does not delete anything — it marks a boundary. The live context is therefore
// the most recent compaction (which carries the summary) followed by every
// entry that was not folded into it. With no compaction, the whole log is the
// context.
//
// This is what makes compaction non-destructive: forking to a point *before* a
// compaction still sees the original, unsummarized history.
func DeriveContextEntries(entries []Entry) []Entry {
	lastIdx := -1
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == EntryCompaction {
			lastIdx = i
			break
		}
	}
	if lastIdx < 0 {
		return entries
	}

	compaction := entries[lastIdx]
	out := []Entry{compaction}

	cutID := ""
	if compaction.Meta != nil {
		cutID, _ = compaction.Meta[MetaCutEntryID].(string)
	}

	// No recorded cut point means the whole prior history was folded away.
	cutPos := -1
	if cutID != "" {
		for i := range entries {
			if entries[i].ID == cutID {
				cutPos = i
				break
			}
		}
	}

	for i := cutPos + 1; i < len(entries); i++ {
		if i == lastIdx {
			continue // the compaction entry itself is already first
		}
		out = append(out, entries[i])
	}
	return out
}

// FindValidEntryCutPoints returns indices i such that entries[:i] may be folded
// into a summary while entries[i:] are kept verbatim.
//
// This is the entry-space twin of contextmgr.FindValidCutPoints: a cut is only
// valid at the start of a user turn with every preceding tool call already
// resolved. Selecting the cut here rather than over the built message list means
// the compaction boundary is expressed as an entry id, so rebuilding context
// after a reload reproduces exactly the same messages.
func FindValidEntryCutPoints(entries []Entry) []int {
	var cuts []int
	pending := 0
	for i := range entries {
		if i > 0 && pending == 0 && entries[i].Kind == EntryUserMessage {
			cuts = append(cuts, i)
		}
		switch entries[i].Kind {
		case EntryAssistantMessage:
			pending += len(toolCallsFromMeta(entries[i].Meta))
		case EntryToolResult:
			if pending > 0 {
				pending--
			}
		}
	}
	return cuts
}

// ContextEntries returns the derived context entries for this session: the
// live root-to-leaf path with the most recent compaction boundary applied.
func (s *InMemorySession) ContextEntries() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return DeriveContextEntries(s.activeEntriesLocked())
}

// RebuildContext discards the ContextManager's cached messages and replays the
// derived context entries onto it. The ContextManager is a cache of the log,
// so it can always be reconstructed from entries.
func (s *InMemorySession) RebuildContext() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rebuildContextLocked()
}

func (s *InMemorySession) rebuildContextLocked() error {
	derived := DeriveContextEntries(s.activeEntriesLocked())
	s.ctxMgr.Clear()
	for _, e := range derived {
		if err := s.replayIntoContext(e); err != nil {
			return err
		}
	}
	return nil
}

// replayIntoContext applies one entry to the ContextManager only — it does not
// touch the entry list (unlike ReplayEntry, which is for loading from storage).
func (s *InMemorySession) replayIntoContext(e Entry) error {
	RehydrateEntry(&e)
	switch e.Kind {
	case EntryUserMessage:
		s.ctxMgr.AddUserMessage(e.Content)
	case EntryAssistantMessage:
		s.ctxMgr.AddAssistantMessage(e.Content, toolCallsFromMeta(e.Meta))
	case EntryToolResult:
		s.ctxMgr.AddToolResult(e.ToolCallID(), e.Content)
	case EntrySystemNotice:
		s.ctxMgr.AddSystemNotice(e.Content)
	case EntryCompaction:
		// The boundary rebuilds the summary preamble at the head of context;
		// surviving turns are replayed after it by RebuildContext.
		s.ctxMgr.ApplySummaryPreamble(e.Content)
	case EntryBranchSummary:
		s.ctxMgr.AddSystemNotice(prompt.WrapBranchSummary(e.Content))
	}
	return nil
}
