package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	if err := migrateSchemaV2(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	if _, err := db.Exec(
		`INSERT INTO schema_meta(key, value) VALUES('version','2')
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("record schema version: %w", err)
	}
	return &SQLiteStore{db: db, cfg: cfg, promptCfg: promptCfg, tools: tools}, nil
}

func migrateSchemaV2(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(sessions)`)
	if err != nil {
		return err
	}
	hasNextSeq := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		if name == "next_seq" {
			hasNextSeq = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if hasNextSeq {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`ALTER TABLE sessions ADD COLUMN next_seq INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE sessions
		   SET next_seq = COALESCE(
		       (SELECT MAX(seq) + 1 FROM entries WHERE entries.session_id = sessions.id), 0)`); err != nil {
		return err
	}
	return tx.Commit()
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
			sess.InMemorySession.mu.Lock()
			sess.InMemorySession.parentID = leaf.String
			sess.InMemorySession.mu.Unlock()
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
	records, err := s.loadRecords(ctx, id)
	if err != nil {
		return nil, err
	}
	cutoff := int64(^uint64(0) >> 1)
	if atEntryID != "" {
		found := false
		for _, e := range entries {
			if e.ID == atEntryID {
				cutoff, found = int64(e.Seq), true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("entry %q not found", atEntryID)
		}
	}
	var includedEntries []Entry
	for _, e := range entries {
		if int64(e.Seq) <= cutoff {
			includedEntries = append(includedEntries, e)
		}
	}
	// Only copy complete operations. A fork at an entry in the middle of a run
	// must start idle rather than inheriting an apparently crashed operation.
	started, finished := map[string]bool{}, map[string]bool{}
	for _, r := range records {
		if r.Seq > cutoff {
			continue
		}
		if r.Type == RecordOperationStarted {
			started[r.RunID] = true
		}
		if r.Type == RecordOperationFinished {
			finished[r.RunID] = true
		}
	}
	var includedRecords []Record
	for _, r := range records {
		if r.Seq <= cutoff && (r.RunID == "" || started[r.RunID] && finished[r.RunID]) {
			includedRecords = append(includedRecords, r)
		}
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

	idMap := map[string]string{}
	for _, e := range includedEntries {
		idMap[e.ID] = NewEntryID()
	}
	runMap := map[string]string{}
	for runID := range started {
		if finished[runID] {
			runMap[runID] = NewRecordID()
		}
	}
	type forkItem struct {
		seq    int64
		entry  *Entry
		record *Record
	}
	items := make([]forkItem, 0, len(includedEntries)+len(includedRecords))
	for i := range includedEntries {
		items = append(items, forkItem{seq: int64(includedEntries[i].Seq), entry: &includedEntries[i]})
	}
	for i := range includedRecords {
		items = append(items, forkItem{seq: includedRecords[i].Seq, record: &includedRecords[i]})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].seq < items[j].seq })
	for _, item := range items {
		if item.entry != nil {
			copied := remapForkEntry(*item.entry, idMap)
			if err := forked.loadAndPersist(ctx, copied); err != nil {
				return nil, err
			}
			continue
		}
		copied := remapForkRecord(*item.record, idMap, runMap)
		if _, err := forked.AppendRecord(copied); err != nil {
			return nil, err
		}
	}
	if err := forked.InMemorySession.RebuildContext(); err != nil {
		return nil, err
	}
	return forked, nil
}

func remapForkEntry(e Entry, idMap map[string]string) Entry {
	e.ID = remapForkID(e.ID, idMap)
	e.ParentID = remapForkID(e.ParentID, idMap)
	if e.Meta != nil {
		meta := make(map[string]any, len(e.Meta))
		for k, v := range e.Meta {
			meta[k] = v
		}
		for _, key := range []string{MetaCutEntryID, "from_id"} {
			if old, ok := meta[key].(string); ok {
				meta[key] = remapForkID(old, idMap)
			}
		}
		e.Meta = meta
	}
	return e
}

func remapForkRecord(r Record, idMap, runMap map[string]string) Record {
	r.ID = NewRecordID()
	if r.RunID != "" {
		r.RunID = runMap[r.RunID]
	}
	r.SourceLeafID = remapForkID(r.SourceLeafID, idMap)
	r.ResultEntryID = remapForkID(r.ResultEntryID, idMap)
	r.AssistantEntryID = remapForkID(r.AssistantEntryID, idMap)
	r.EntryID = remapForkID(r.EntryID, idMap)
	if r.Target != nil {
		target := remapForkProvisioned(*r.Target, idMap)
		r.Target = &target
	}
	if r.Intent != nil {
		intent := *r.Intent
		intent.ResultEntryID = remapForkID(intent.ResultEntryID, idMap)
		intent.TargetID = remapForkID(intent.TargetID, idMap)
		intent.SummaryEntryID = remapForkID(intent.SummaryEntryID, idMap)
		intent.OriginalPrompt = remapForkProvisionedSlice(intent.OriginalPrompt, idMap)
		intent.InitialMessages = remapForkProvisionedSlice(intent.InitialMessages, idMap)
		r.Intent = &intent
	}
	return r
}

func remapForkProvisionedSlice(in []ProvisionedEntry, idMap map[string]string) []ProvisionedEntry {
	out := make([]ProvisionedEntry, len(in))
	for i := range in {
		out[i] = remapForkProvisioned(in[i], idMap)
	}
	return out
}

func remapForkProvisioned(p ProvisionedEntry, idMap map[string]string) ProvisionedEntry {
	p.ID = remapForkID(p.ID, idMap)
	return p
}

func remapForkID(id string, idMap map[string]string) string {
	if id == "" {
		return ""
	}
	if mapped, ok := idMap[id]; ok {
		return mapped
	}
	mapped := NewEntryID()
	idMap[id] = mapped
	return mapped
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
	return s.AppendProvisioned(provision(EntryUserMessage, openai.ChatMessageRoleUser, content, nil, nil))
}

func (s *SQLiteSession) AppendAssistantMessage(content string, toolCalls []openai.ToolCall) (Entry, error) {
	meta := map[string]any{}
	if len(toolCalls) > 0 {
		meta["tool_calls"] = toolCalls
	}
	return s.AppendProvisioned(provision(EntryAssistantMessage, openai.ChatMessageRoleAssistant, content, nil, meta))
}

func (s *SQLiteSession) AppendToolResult(toolCallID, content string) (Entry, error) {
	return s.AppendProvisioned(provision(EntryToolResult, openai.ChatMessageRoleTool, content, nil,
		map[string]any{"tool_call_id": toolCallID}))
}

func (s *SQLiteSession) AppendSystemNotice(content string) (Entry, error) {
	return s.AppendProvisioned(provision(EntrySystemNotice, openai.ChatMessageRoleSystem, content, nil, nil))
}

func (s *SQLiteSession) AppendTodos(items any) (Entry, error) {
	return s.AppendProvisioned(provision(EntryTodos, "", "", nil, map[string]any{"items": items}))
}

func (s *SQLiteSession) AppendApprovalAlways(toolName string) (Entry, error) {
	return s.AppendProvisioned(provision(EntryApprovalAlways, "", toolName, nil, map[string]any{"tool": toolName}))
}

func (s *SQLiteSession) AppendModelChange(model string) (Entry, error) {
	return s.AppendProvisioned(provision(EntryModelChange, "", model, nil, map[string]any{"model": model}))
}

func (s *SQLiteSession) AppendThinkingLevelChange(level string) (Entry, error) {
	return s.AppendProvisioned(provision(EntryThinkingLevelChange, "", level, nil, map[string]any{"level": level}))
}

func (s *SQLiteSession) AppendActiveToolsChange(names []string) (Entry, error) {
	return s.AppendProvisioned(provision(EntryActiveToolsChange, "", "", nil, map[string]any{"tools": names}))
}

func (s *SQLiteSession) AppendCompactionAt(summary, cutEntryID string) (Entry, error) {
	return s.AppendProvisioned(provision(EntryCompaction, "", summary, nil,
		map[string]any{MetaCutEntryID: cutEntryID}))
}

func (s *SQLiteSession) AppendCompaction(summary string) error {
	_, err := s.AppendProvisioned(provision(EntryCompaction, "", summary, nil, nil))
	if err == nil {
		s.ContextManager().ReplaceWithSummary(summary)
	}
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
	if _, err := s.AppendProvisioned(provision(EntryLabel, "", label, nil, nil)); err != nil {
		return err
	}
	_, err := s.store.db.Exec(
		`UPDATE sessions SET label = ?, updated_at = ? WHERE id = ?`,
		label, nowMillis(), s.ID())
	return err
}

func (s *SQLiteSession) AppendProvisioned(p ProvisionedEntry) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		return Entry{}, fmt.Errorf("provisioned entry id required")
	}
	if existing, ok, err := s.lookupEntry(context.Background(), p.ID); err != nil {
		return Entry{}, err
	} else if ok {
		if !p.Matches(existing) {
			return Entry{}, &ProvisionedEntryMismatchError{ID: p.ID}
		}
		return existing, nil
	}

	var metaJSON string
	if len(p.Meta) > 0 {
		b, err := json.Marshal(p.Meta)
		if err != nil {
			return Entry{}, fmt.Errorf("marshal entry meta: %w", err)
		}
		metaJSON = string(b)
	}
	tx, err := s.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Entry{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var seq int
	if err := tx.QueryRow(
		`UPDATE sessions SET next_seq = next_seq + 1 WHERE id = ? RETURNING next_seq - 1`,
		s.ID()).Scan(&seq); err != nil {
		return Entry{}, fmt.Errorf("allocate session sequence: %w", err)
	}
	e := Entry{
		ID: p.ID, ParentID: s.InMemorySession.Leaf(), Seq: seq, Kind: p.Kind,
		Role: p.Role, Content: p.Content, ToolCall: p.ToolCall, Meta: p.Meta,
		Time: time.Now().UTC(),
	}
	if _, err := tx.Exec(
		`INSERT INTO entries(id, session_id, parent_id, seq, kind, role, content,
		                     tool_call_id, meta, token_count, created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, s.ID(), nullable(e.ParentID), e.Seq, string(e.Kind), nullable(e.Role),
		e.Content, nullable(e.ToolCallID()), nullable(metaJSON), nil, e.Time.UnixMilli()); err != nil {
		return Entry{}, fmt.Errorf("insert provisioned entry: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE sessions SET updated_at = ?, leaf_entry_id = ? WHERE id = ?`,
		nowMillis(), e.ID, s.ID()); err != nil {
		return Entry{}, err
	}
	if _, err := tx.Exec(
		`INSERT INTO lanes(session_id, lane, leaf_entry_id) VALUES(?, 'main', ?)
		 ON CONFLICT(session_id, lane) DO UPDATE SET leaf_entry_id=excluded.leaf_entry_id`,
		s.ID(), e.ID); err != nil {
		return Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		return Entry{}, err
	}
	s.InMemorySession.mu.Lock()
	s.InMemorySession.appendStoredLocked(e)
	s.InMemorySession.mu.Unlock()
	return e, nil
}

func (s *SQLiteSession) lookupEntry(ctx context.Context, id string) (Entry, bool, error) {
	var (
		e          Entry
		parentID   sql.NullString
		role       sql.NullString
		content    sql.NullString
		toolCallID sql.NullString
		metaJSON   sql.NullString
		createdAt  int64
	)
	err := s.store.db.QueryRowContext(ctx,
		`SELECT id, parent_id, seq, kind, role, content, tool_call_id, meta, created_at
		   FROM entries WHERE session_id = ? AND id = ?`, s.ID(), id).
		Scan(&e.ID, &parentID, &e.Seq, &e.Kind, &role, &content, &toolCallID, &metaJSON, &createdAt)
	if err == sql.ErrNoRows {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	e.ParentID, e.Role, e.Content = parentID.String, role.String, content.String
	e.Time = time.UnixMilli(createdAt).UTC()
	if metaJSON.Valid && metaJSON.String != "" {
		_ = json.Unmarshal([]byte(metaJSON.String), &e.Meta)
	}
	if toolCallID.Valid {
		if e.Meta == nil {
			e.Meta = map[string]any{}
		}
		e.Meta["tool_call_id"] = toolCallID.String
	}
	RehydrateEntry(&e)
	return e, true, nil
}

// loadAndPersist seeds a forked session from an existing entry: it records the
// entry under a new id and writes it. The caller rebuilds context afterwards.
func (s *SQLiteSession) loadAndPersist(ctx context.Context, e Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.AppendProvisioned(ProvisionedEntry{
		ID: e.ID, Kind: e.Kind, Role: e.Role, Content: e.Content,
		ToolCall: e.ToolCall, Meta: e.Meta,
	})
	return err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
