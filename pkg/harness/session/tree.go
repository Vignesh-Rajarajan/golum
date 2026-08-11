package session

import "fmt"

// GetEntry returns the entry with the given id.
func (s *InMemorySession) GetEntry(id string) (Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getEntryLocked(id)
}

func (s *InMemorySession) getEntryLocked(id string) (Entry, bool) {
	for i := range s.entries {
		if s.entries[i].ID == id {
			return s.entries[i], true
		}
	}
	return Entry{}, false
}

// Leaf returns the id of the current head of the session tree.
func (s *InMemorySession) Leaf() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.parentID
}

// GetPathToRoot returns the ancestor chain ending at id, ordered root-first.
//
// The log is append-only and entries carry a ParentID, so a session is a tree:
// forking or moving the leaf creates siblings rather than rewriting history.
// The "live" conversation is always one root-to-leaf path through it.
func (s *InMemorySession) GetPathToRoot(id string) ([]Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getPathToRootLocked(id)
}

func (s *InMemorySession) getPathToRootLocked(id string) ([]Entry, error) {
	if id == "" {
		return nil, nil
	}
	byID := make(map[string]Entry, len(s.entries))
	for i := range s.entries {
		byID[s.entries[i].ID] = s.entries[i]
	}

	var reversed []Entry
	seen := map[string]bool{}
	cur := id
	for cur != "" {
		if seen[cur] {
			return nil, fmt.Errorf("cycle in session tree at %q", cur)
		}
		seen[cur] = true
		e, ok := byID[cur]
		if !ok {
			return nil, fmt.Errorf("entry %q not found", cur)
		}
		reversed = append(reversed, e)
		cur = e.ParentID
	}

	out := make([]Entry, len(reversed))
	for i, e := range reversed {
		out[len(reversed)-1-i] = e
	}
	return out, nil
}

// GetBranch returns fromID and every entry descended from it, in log order.
func (s *InMemorySession) GetBranch(fromID string) ([]Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.getEntryLocked(fromID); !ok {
		return nil, fmt.Errorf("entry %q not found", fromID)
	}
	inBranch := map[string]bool{fromID: true}
	var out []Entry
	for i := range s.entries {
		e := s.entries[i]
		if inBranch[e.ID] || inBranch[e.ParentID] {
			inBranch[e.ID] = true
			out = append(out, e)
		}
	}
	return out, nil
}

// activeEntries returns the root-to-leaf path that forms the live conversation.
// For a session that has never branched this is simply the whole log.
func (s *InMemorySession) activeEntriesLocked() []Entry {
	if s.parentID == "" {
		return s.entries
	}
	path, err := s.getPathToRootLocked(s.parentID)
	if err != nil || len(path) == 0 {
		// A broken chain must not silently truncate the conversation.
		return s.entries
	}
	return path
}

// MoveTo repoints the session head at entryID and rebuilds context from the
// resulting path. Subsequent appends branch from there, leaving the abandoned
// entries in the log (and forkable).
func (s *InMemorySession) MoveTo(entryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.getEntryLocked(entryID); !ok {
		return fmt.Errorf("entry %q not found", entryID)
	}
	s.parentID = entryID
	return s.rebuildContextLocked()
}
