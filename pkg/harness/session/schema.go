package session

// schemaSQL is applied on every open; every statement is IF NOT EXISTS so it
// doubles as the migration for a fresh database.
//
// Design notes:
//   - meta holds the verbatim Entry.Meta JSON so an Entry round-trips with full
//     fidelity. tool_call_id is denormalized out of meta into its own column so
//     the tool_calls/tool_result balance invariant is queryable in SQL.
//   - root_id / parent_session_id exist from day one so subagent trees (and a
//     later per-tree database split) need no schema migration.
//   - memories_fts is an external-content FTS5 table over memories(content);
//     the triggers below keep it in sync.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS schema_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  id                   TEXT PRIMARY KEY,
  root_id              TEXT NOT NULL,
  parent_session_id    TEXT,
  forked_from_entry_id TEXT,
  label                TEXT,
  cwd                  TEXT NOT NULL DEFAULT '',
  model                TEXT,
  leaf_entry_id        TEXT,
  created_at           INTEGER NOT NULL,
  updated_at           INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_root    ON sessions(root_id);
CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at DESC);

CREATE TABLE IF NOT EXISTS entries (
  id           TEXT PRIMARY KEY,
  session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  parent_id    TEXT,
  seq          INTEGER NOT NULL,
  kind         TEXT NOT NULL,
  role         TEXT,
  content      TEXT,
  tool_call_id TEXT,
  meta         TEXT,
  token_count  INTEGER,
  created_at   INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_entries_session_seq ON entries(session_id, seq);
CREATE INDEX IF NOT EXISTS idx_entries_parent      ON entries(parent_id);
CREATE INDEX IF NOT EXISTS idx_entries_tool_call   ON entries(tool_call_id);

CREATE TABLE IF NOT EXISTS memories (
  id         TEXT PRIMARY KEY,
  tier       TEXT NOT NULL,
  scope      TEXT NOT NULL,
  key        TEXT,
  content    TEXT NOT NULL,
  session_id TEXT,
  file_path  TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_memories_tier  ON memories(tier, scope);
CREATE UNIQUE INDEX IF NOT EXISTS idx_memories_key ON memories(tier, scope, key)
  WHERE key IS NOT NULL AND key <> '';

CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
  content,
  content='memories',
  content_rowid='rowid'
);

CREATE TRIGGER IF NOT EXISTS memories_ai AFTER INSERT ON memories BEGIN
  INSERT INTO memories_fts(rowid, content) VALUES (new.rowid, new.content);
END;

CREATE TRIGGER IF NOT EXISTS memories_ad AFTER DELETE ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, content) VALUES('delete', old.rowid, old.content);
END;

CREATE TRIGGER IF NOT EXISTS memories_au AFTER UPDATE ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, content) VALUES('delete', old.rowid, old.content);
  INSERT INTO memories_fts(rowid, content) VALUES (new.rowid, new.content);
END;
`
