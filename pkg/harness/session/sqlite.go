package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/sashabaranov/go-openai"

	_ "modernc.org/sqlite"
)

// DefaultDBPath returns ~/.golum/golum.db.
func DefaultDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".golum", "golum.db"), nil
}

// SQLiteStore is a SessionRepo backed by a single SQLite database.
//
// One database, WAL mode, with a busy timeout: WAL gives concurrent readers
// alongside a single writer, and the busy timeout serializes the writers we do
// have. The schema carries root_id/parent_session_id so a future subagent tree
// can be split into per-tree databases without a migration if contention ever
// justifies it.
type SQLiteStore struct {
	db         *sql.DB
	cfg        *config.Config
	promptCfg  prompt.PromptConfig
	tools      []llm.Tool
	userMemory *string
}

// OpenSQLiteStore opens (creating if needed) the database at path and applies the schema.
func OpenSQLiteStore(path string, cfg *config.Config, promptCfg prompt.PromptConfig, tools []llm.Tool) (*SQLiteStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(10000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	// Transcripts can contain source, secrets read by tools, and command output;
	// match the 0600 the legacy JSONL files used rather than the default 0644.
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(p); err == nil {
			_ = os.Chmod(p, 0o600)
		}
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if _, err := db.Exec(
		`INSERT INTO schema_meta(key, value) VALUES('version','1')
		 ON CONFLICT(key) DO NOTHING`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("record schema version: %w", err)
	}
	return &SQLiteStore{db: db, cfg: cfg, promptCfg: promptCfg, tools: tools}, nil
}

// DB exposes the handle for the memory subsystem, which shares this database.
func (s *SQLiteStore) DB() *sql.DB { return s.db }

// SetTools sets the tool list baked into new sessions' system prompts.
//
// Tools are set after construction because the memory tool can only be built
// once this store's database is open — and the prompt's tool guidance is
// derived from the registry, so the list must be complete before the first
// ContextManager freezes it.
func (s *SQLiteStore) SetTools(tools []llm.Tool) { s.tools = tools }

// SetUserMemory sets the remembered user facts folded into new sessions'
// system prompts.
func (s *SQLiteStore) SetUserMemory(m string) {
	if m == "" {
		s.userMemory = nil
		return
	}
	s.userMemory = &m
}

// Close closes the underlying database.
func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) newContextManager() *contextmgr.ContextManager {
	return contextmgr.NewContextManager(s.cfg, s.promptCfg, s.userMemory, s.tools)
}

func nowMillis() int64 { return time.Now().UTC().UnixMilli() }

// Create starts a new root session.
func (s *SQLiteStore) Create(ctx context.Context) (Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := NewSessionID()
	now := nowMillis()
	model := ""
	if s.cfg != nil {
		model = s.cfg.Model
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions(id, root_id, cwd, model, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		id, id, s.promptCfg.CWD, model, now, now)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return &SQLiteSession{
		InMemorySession: NewInMemorySession(id, s.newContextManager()),
		store:           s,
	}, nil
}

// Open loads a session and replays its entries into a fresh ContextManager.
func (s *SQLiteStore) Open(ctx context.Context, id string) (Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var found string
	var leaf sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, leaf_entry_id FROM sessions WHERE id = ?`, id).Scan(&found, &leaf)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session %q not found", id)
	}
	if err != nil {
		return nil, err
	}

	sess := &SQLiteSession{
		InMemorySession: NewInMemorySession(id, s.newContextManager()),
		store:           s,
	}
	entries, err := s.loadEntries(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		sess.InMemorySession.LoadEntry(e)
	}
	// Restore the head so a session that was moved onto a branch resumes there
	// rather than on whichever entry happened to be written last.
	if leaf.Valid && leaf.String != "" {
		if _, ok := sess.InMemorySession.GetEntry(leaf.String); ok {
			sess.InMemorySession.parentID = leaf.String
		}
	}
	// Derive rather than replay: see LoadEntry.
	if err := sess.InMemorySession.RebuildContext(); err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *SQLiteStore) loadEntries(ctx context.Context, sessionID string) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, parent_id, seq, kind, role, content, tool_call_id, meta, created_at
		   FROM entries WHERE session_id = ? ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var (
			e          Entry
			parentID   sql.NullString
			role       sql.NullString
			content    sql.NullString
			toolCallID sql.NullString
			metaJSON   sql.NullString
			createdAt  int64
		)
		if err := rows.Scan(&e.ID, &parentID, &e.Seq, &e.Kind, &role, &content,
			&toolCallID, &metaJSON, &createdAt); err != nil {
			return nil, err
		}
		e.ParentID = parentID.String
		e.Role = role.String
		e.Content = content.String
		e.Time = time.UnixMilli(createdAt).UTC()
		if metaJSON.Valid && metaJSON.String != "" {
			var meta map[string]any
			if json.Unmarshal([]byte(metaJSON.String), &meta) == nil {
				e.Meta = meta
			}
		}
		// tool_call_id is denormalized for querying; meta remains the source of truth.
		if toolCallID.Valid && toolCallID.String != "" {
			if e.Meta == nil {
				e.Meta = map[string]any{}
			}
			e.Meta["tool_call_id"] = toolCallID.String
		}
		RehydrateEntry(&e)
		out = append(out, e)
	}
	return out, rows.Err()
}

// List returns session metadata, newest first.
func (s *SQLiteStore) List(ctx context.Context) ([]SessionMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT s.id, COALESCE(s.label,''), COALESCE(s.cwd,''), s.created_at, s.updated_at,
		        (SELECT COUNT(*) FROM entries e WHERE e.session_id = s.id)
		   FROM sessions s ORDER BY s.updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SessionMeta
	for rows.Next() {
		var (
			m                    SessionMeta
			createdAt, updatedAt int64
		)
		if err := rows.Scan(&m.ID, &m.Label, &m.CWD, &createdAt, &updatedAt, &m.EntryCount); err != nil {
			return nil, err
		}
		m.CreatedAt = time.UnixMilli(createdAt).UTC()
		m.UpdatedAt = time.UnixMilli(updatedAt).UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

// Delete removes a session and (via ON DELETE CASCADE) its entries.
func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("session %q not found", id)
	}
	return nil
}

// Fork creates a new session seeded with src's entries up to and including atEntryID.
// An empty atEntryID copies the whole history.
func (s *SQLiteStore) Fork(ctx context.Context, id, atEntryID string) (Session, error) {
	entries, err := s.loadEntries(ctx, id)
	if err != nil {
		return nil, err
	}
	dst, err := s.Create(ctx)
	if err != nil {
		return nil, err
	}
	forked, ok := dst.(*SQLiteSession)
	if !ok {
		return nil, fmt.Errorf("unexpected session type %T", dst)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET parent_session_id = ?, forked_from_entry_id = ?,
		        root_id = COALESCE((SELECT root_id FROM sessions WHERE id = ?), ?)
		  WHERE id = ?`,
		id, atEntryID, id, id, forked.ID()); err != nil {
		return nil, err
	}

	// entries.id is a global primary key, so the copies need fresh ids — which
	// means any compaction entry's recorded cut point must be remapped to the
	// new id, or the fork's derived context would silently lose its boundary.
	idMap := map[string]string{}
	for _, e := range entries {
		copied := e
		copied.ID = NewEntryID()
		idMap[e.ID] = copied.ID
		if copied.Kind == EntryCompaction && copied.Meta != nil {
			if old, ok := copied.Meta[MetaCutEntryID].(string); ok {
				remapped := copied.Meta
				cloned := make(map[string]any, len(remapped))
				for k, v := range remapped {
					cloned[k] = v
				}
				if mapped, ok := idMap[old]; ok {
					cloned[MetaCutEntryID] = mapped
				}
				copied.Meta = cloned
			}
		}
		if err := forked.loadAndPersist(ctx, copied); err != nil {
			return nil, err
		}
		if atEntryID != "" && e.ID == atEntryID {
			break
		}
	}
	if err := forked.InMemorySession.RebuildContext(); err != nil {
		return nil, err
	}
	return forked, nil
}

// SQLiteSession is an InMemorySession whose appends are also written to SQLite.
//
// The embedded InMemorySession remains the runtime cache (it owns the
// ContextManager); this type only adds durability, so every append must go
// through both.
type SQLiteSession struct {
	*InMemorySession
	store *SQLiteStore
	mu    sync.Mutex
}

func (s *SQLiteSession) AppendUserMessage(content string) (Entry, error) {
	return s.appendBoth(func() (Entry, error) { return s.InMemorySession.AppendUserMessage(content) })
}

func (s *SQLiteSession) AppendAssistantMessage(content string, toolCalls []openai.ToolCall) (Entry, error) {
	return s.appendBoth(func() (Entry, error) {
		return s.InMemorySession.AppendAssistantMessage(content, toolCalls)
	})
}

func (s *SQLiteSession) AppendToolResult(toolCallID, content string) (Entry, error) {
	return s.appendBoth(func() (Entry, error) {
		return s.InMemorySession.AppendToolResult(toolCallID, content)
	})
}

func (s *SQLiteSession) AppendSystemNotice(content string) (Entry, error) {
	return s.appendBoth(func() (Entry, error) { return s.InMemorySession.AppendSystemNotice(content) })
}

func (s *SQLiteSession) AppendTodos(items any) (Entry, error) {
	return s.appendBoth(func() (Entry, error) { return s.InMemorySession.AppendTodos(items) })
}

func (s *SQLiteSession) AppendApprovalAlways(toolName string) (Entry, error) {
	return s.appendBoth(func() (Entry, error) {
		return s.InMemorySession.AppendApprovalAlways(toolName)
	})
}

func (s *SQLiteSession) AppendCompactionAt(summary, cutEntryID string) (Entry, error) {
	return s.appendBoth(func() (Entry, error) {
		return s.InMemorySession.AppendCompactionAt(summary, cutEntryID)
	})
}

func (s *SQLiteSession) AppendCompaction(summary string) error {
	_, err := s.appendBoth(func() (Entry, error) {
		if err := s.InMemorySession.AppendCompaction(summary); err != nil {
			return Entry{}, err
		}
		entries := s.InMemorySession.entries
		if len(entries) == 0 {
			return Entry{}, fmt.Errorf("compaction produced no entry")
		}
		return entries[len(entries)-1], nil
	})
	return err
}

// MoveTo repoints the head and persists the new leaf so a reload resumes from
// the same branch.
func (s *SQLiteSession) MoveTo(entryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.InMemorySession.MoveTo(entryID); err != nil {
		return err
	}
	_, err := s.store.db.Exec(
		`UPDATE sessions SET leaf_entry_id = ?, updated_at = ? WHERE id = ?`,
		entryID, nowMillis(), s.ID())
	return err
}

// SetLabel persists the label on the session row as well as recording the entry.
// (The JSONL implementation only recorded it in memory, so labels never survived
// a restart — do not reintroduce that gap here.)
func (s *SQLiteSession) SetLabel(label string) error {
	if _, err := s.appendBoth(func() (Entry, error) {
		if err := s.InMemorySession.SetLabel(label); err != nil {
			return Entry{}, err
		}
		entries := s.InMemorySession.entries
		return entries[len(entries)-1], nil
	}); err != nil {
		return err
	}
	_, err := s.store.db.Exec(
		`UPDATE sessions SET label = ?, updated_at = ? WHERE id = ?`,
		label, nowMillis(), s.ID())
	return err
}

// appendBoth runs the in-memory append then persists the resulting entry.
func (s *SQLiteSession) appendBoth(fn func() (Entry, error)) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, err := fn()
	if err != nil {
		return e, err
	}
	if err := s.persist(context.Background(), e); err != nil {
		return e, err
	}
	return e, nil
}

// loadAndPersist seeds a forked session from an existing entry: it records the
// entry under a new id and writes it. The caller rebuilds context afterwards.
func (s *SQLiteSession) loadAndPersist(ctx context.Context, e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.InMemorySession.LoadEntry(e)
	e.Seq = s.InMemorySession.seq
	return s.persist(ctx, e)
}

func (s *SQLiteSession) persist(ctx context.Context, e Entry) error {
	var metaJSON string
	if len(e.Meta) > 0 {
		b, err := json.Marshal(e.Meta)
		if err != nil {
			return fmt.Errorf("marshal entry meta: %w", err)
		}
		metaJSON = string(b)
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO entries(id, session_id, parent_id, seq, kind, role, content,
		                     tool_call_id, meta, token_count, created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, s.ID(), nullable(e.ParentID), e.Seq, string(e.Kind), nullable(e.Role),
		e.Content, nullable(e.ToolCallID()), nullable(metaJSON), nil,
		e.Time.UTC().UnixMilli(),
	); err != nil {
		return fmt.Errorf("insert entry: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions SET updated_at = ?, leaf_entry_id = ? WHERE id = ?`,
		nowMillis(), e.ID, s.ID(),
	); err != nil {
		return fmt.Errorf("update session head: %w", err)
	}
	return tx.Commit()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
