package session

import (
	"github.com/google/uuid"
)

// ID prefixes. The prefix is constant per kind, so lexicographic ordering
// within a kind is preserved by the UUIDv7 suffix.
const (
	sessionIDPrefix = "sess_"
	entryIDPrefix   = "e_"
	memoryIDPrefix  = "mem_"
)

// newID returns a time-ordered identifier. UUIDv7 encodes a millisecond
// timestamp in its leading bits, so sorting ids as strings sorts them
// chronologically — which is what lets entries be ordered without a clock
// column and what keeps session listings stable.
//
// NewV7 only fails if the system entropy source fails; fall back to v4, which
// is still unique but not sortable (callers order by seq/created_at anyway).
func newID(prefix string) string {
	v, err := uuid.NewV7()
	if err != nil {
		return prefix + uuid.NewString()
	}
	return prefix + v.String()
}

// NewSessionID returns a fresh sortable session id.
func NewSessionID() string { return newID(sessionIDPrefix) }

// NewEntryID returns a fresh sortable entry id.
func NewEntryID() string { return newID(entryIDPrefix) }

// NewMemoryID returns a fresh sortable memory id.
func NewMemoryID() string { return newID(memoryIDPrefix) }
