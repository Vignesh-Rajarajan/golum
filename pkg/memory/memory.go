// Package memory implements the agent's three memory tiers.
//
//	procedural — how to work here: project conventions, tool protocols, style
//	             rules. Mostly authored by humans (AGENTS.md) and remembered
//	             user preferences.
//	episodic   — what happened: per-session digests, file changes, failures.
//	semantic   — what the system is: repo structure, dependencies, domain rules.
//
// All three share one SQLite table plus an FTS5 index, so a single query can
// span tiers when the agent asks "what do I know about X".
package memory

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Tier classifies a memory by the kind of knowledge it holds.
type Tier string

const (
	TierProcedural Tier = "procedural"
	TierEpisodic   Tier = "episodic"
	TierSemantic   Tier = "semantic"
)

// ValidTier reports whether s names a known tier.
func ValidTier(s string) bool {
	switch Tier(s) {
	case TierProcedural, TierEpisodic, TierSemantic:
		return true
	}
	return false
}

// Scope separates knowledge about this project from knowledge about the user.
type Scope string

const (
	ScopeProject Scope = "project"
	ScopeUser    Scope = "user"
)

// ValidScope reports whether s names a known scope.
func ValidScope(s string) bool {
	return Scope(s) == ScopeProject || Scope(s) == ScopeUser
}

// Record is one stored memory.
type Record struct {
	ID        string
	Tier      Tier
	Scope     Scope
	Key       string
	Content   string
	SessionID string
	FilePath  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Store persists and searches memories.
type Store struct {
	db *sql.DB
}

// NewStore wraps an already-open database (shared with session storage).
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Put inserts or updates a memory. When Key is set, it upserts on
// (tier, scope, key) so re-remembering the same fact revises it rather than
// accumulating near-duplicates.
func (s *Store) Put(ctx context.Context, r Record) (Record, error) {
	if s == nil || s.db == nil {
		return r, fmt.Errorf("memory store unavailable")
	}
	if strings.TrimSpace(r.Content) == "" {
		return r, fmt.Errorf("memory content must not be empty")
	}
	if r.Tier == "" {
		r.Tier = TierProcedural
	}
	if r.Scope == "" {
		r.Scope = ScopeProject
	}
	now := time.Now().UTC()
	r.UpdatedAt = now

	if r.Key != "" {
		var existingID string
		var createdAt int64
		err := s.db.QueryRowContext(ctx,
			`SELECT id, created_at FROM memories WHERE tier = ? AND scope = ? AND key = ?`,
			string(r.Tier), string(r.Scope), r.Key).Scan(&existingID, &createdAt)
		if err == nil {
			if _, err := s.db.ExecContext(ctx,
				`UPDATE memories SET content = ?, session_id = ?, file_path = ?, updated_at = ?
				 WHERE id = ?`,
				r.Content, nullIfEmpty(r.SessionID), nullIfEmpty(r.FilePath),
				now.UnixMilli(), existingID); err != nil {
				return r, err
			}
			r.ID = existingID
			r.CreatedAt = time.UnixMilli(createdAt).UTC()
			return r, nil
		}
		if err != sql.ErrNoRows {
			return r, err
		}
	}

	if r.ID == "" {
		r.ID = "mem_" + uuid.NewString()
	}
	r.CreatedAt = now
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO memories(id, tier, scope, key, content, session_id, file_path, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		r.ID, string(r.Tier), string(r.Scope), nullIfEmpty(r.Key), r.Content,
		nullIfEmpty(r.SessionID), nullIfEmpty(r.FilePath),
		now.UnixMilli(), now.UnixMilli())
	return r, err
}

// Search runs a full-text query, optionally restricted to one tier.
//
// FTS5 gives ranked substring/prefix matching over all stored memories. That
// covers most of what an embedding index would buy here, without an embedding
// endpoint, chunking, or re-indexing on every edit — and the exact-lookup cases
// are already served by the grep and glob tools.
func (s *Store) Search(ctx context.Context, tier Tier, query string, limit int) ([]Record, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("memory store unavailable")
	}
	if limit <= 0 {
		limit = 10
	}
	match := ftsQuery(query)
	if match == "" {
		return s.List(ctx, tier, limit)
	}

	args := []any{match}
	q := `SELECT m.id, m.tier, m.scope, COALESCE(m.key,''), m.content,
	             COALESCE(m.session_id,''), COALESCE(m.file_path,''), m.created_at, m.updated_at
	        FROM memories_fts f JOIN memories m ON m.rowid = f.rowid
	       WHERE memories_fts MATCH ?`
	if tier != "" {
		q += ` AND m.tier = ?`
		args = append(args, string(tier))
	}
	q += ` ORDER BY rank LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		// A malformed FTS expression should degrade to a listing, not fail the tool.
		return s.List(ctx, tier, limit)
	}
	defer rows.Close()
	return scanRecords(rows)
}

// List returns the most recently updated memories, optionally by tier.
func (s *Store) List(ctx context.Context, tier Tier, limit int) ([]Record, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("memory store unavailable")
	}
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT id, tier, scope, COALESCE(key,''), content,
	             COALESCE(session_id,''), COALESCE(file_path,''), created_at, updated_at
	        FROM memories`
	var args []any
	if tier != "" {
		q += ` WHERE tier = ?`
		args = append(args, string(tier))
	}
	q += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecords(rows)
}

// ListScoped returns memories for one tier and scope, oldest first, for
// rendering into the system prompt.
func (s *Store) ListScoped(ctx context.Context, tier Tier, scope Scope, limit int) ([]Record, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tier, scope, COALESCE(key,''), content,
		        COALESCE(session_id,''), COALESCE(file_path,''), created_at, updated_at
		   FROM memories WHERE tier = ? AND scope = ?
		  ORDER BY updated_at ASC LIMIT ?`,
		string(tier), string(scope), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecords(rows)
}

// Forget deletes a memory by id or by (tier, scope, key).
func (s *Store) Forget(ctx context.Context, idOrKey string) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("memory store unavailable")
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM memories WHERE id = ? OR key = ?`, idOrKey, idOrKey)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// DeleteTier removes every memory in a tier (used when reindexing semantics).
func (s *Store) DeleteTier(ctx context.Context, tier Tier, scope Scope) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("memory store unavailable")
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM memories WHERE tier = ? AND scope = ?`, string(tier), string(scope))
	return err
}

func scanRecords(rows *sql.Rows) ([]Record, error) {
	var out []Record
	for rows.Next() {
		var (
			r                    Record
			tier, scope          string
			createdAt, updatedAt int64
		)
		if err := rows.Scan(&r.ID, &tier, &scope, &r.Key, &r.Content,
			&r.SessionID, &r.FilePath, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		r.Tier = Tier(tier)
		r.Scope = Scope(scope)
		r.CreatedAt = time.UnixMilli(createdAt).UTC()
		r.UpdatedAt = time.UnixMilli(updatedAt).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// ftsQuery turns free text into a safe FTS5 MATCH expression: each word becomes
// a quoted prefix term, so user input can never be read as FTS syntax.
func ftsQuery(q string) string {
	fields := strings.Fields(q)
	if len(fields) == 0 {
		return ""
	}
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		cleaned := strings.Map(func(r rune) rune {
			if r == '"' || r == '*' || r == '(' || r == ')' || r == ':' || r == '^' {
				return -1
			}
			return r
		}, f)
		if cleaned == "" {
			continue
		}
		terms = append(terms, `"`+cleaned+`"*`)
	}
	if len(terms) == 0 {
		return ""
	}
	return strings.Join(terms, " OR ")
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// FormatForPrompt renders records as a compact markdown list for the system prompt.
func FormatForPrompt(records []Record) string {
	if len(records) == 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range records {
		if r.Key != "" {
			fmt.Fprintf(&b, "- **%s**: %s\n", r.Key, r.Content)
		} else {
			fmt.Fprintf(&b, "- %s\n", r.Content)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
