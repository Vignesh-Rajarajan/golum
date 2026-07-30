package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MigrateJSONL imports legacy ~/.golum/sessions/*.jsonl files into the store.
//
// It is idempotent: each imported file is renamed to *.jsonl.imported on
// success, and sessions whose id already exists in the database are skipped.
// Returns the number of sessions imported.
func MigrateJSONL(ctx context.Context, store *SQLiteStore, jsonlDir string) (int, error) {
	dirEntries, err := os.ReadDir(jsonlDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // nothing to migrate
		}
		return 0, err
	}

	var files []string
	for _, de := range dirEntries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".jsonl") {
			continue
		}
		files = append(files, de.Name())
	}
	sort.Strings(files)

	imported := 0
	for _, name := range files {
		if err := ctx.Err(); err != nil {
			return imported, err
		}
		path := filepath.Join(jsonlDir, name)
		id := strings.TrimSuffix(name, ".jsonl")

		exists, err := store.sessionExists(ctx, id)
		if err != nil {
			return imported, err
		}
		if exists {
			continue
		}
		if err := importJSONLFile(ctx, store, id, path); err != nil {
			// A corrupt legacy file should not block startup; skip it.
			continue
		}
		_ = os.Rename(path, path+".imported")
		imported++
	}
	return imported, nil
}

func (s *SQLiteStore) sessionExists(ctx context.Context, id string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ?`, id).Scan(&one)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return false, nil
		}
		return false, nil
	}
	return true, nil
}

func importJSONLFile(ctx context.Context, store *SQLiteStore, id, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var entries []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // skip malformed lines rather than losing the whole session
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("no entries in %s", path)
	}

	created := entries[0].Time
	updated := entries[len(entries)-1].Time
	if created.IsZero() {
		created = time.Now().UTC()
	}
	if updated.IsZero() {
		updated = created
	}
	model := ""
	if store.cfg != nil {
		model = store.cfg.Model
	}
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO sessions(id, root_id, cwd, model, created_at, updated_at)
		 VALUES(?,?,?,?,?,?)`,
		id, id, store.promptCfg.CWD, model,
		created.UTC().UnixMilli(), updated.UTC().UnixMilli()); err != nil {
		return err
	}

	sess := &SQLiteSession{
		InMemorySession: NewInMemorySession(id, store.newContextManager()),
		store:           store,
	}
	for i, e := range entries {
		if e.ID == "" {
			e.ID = NewEntryID()
		}
		if e.Seq == 0 {
			e.Seq = i + 1
		}
		if e.Time.IsZero() {
			e.Time = created
		}
		RehydrateEntry(&e)
		if err := sess.InMemorySession.ReplayEntry(e); err != nil {
			return err
		}
		if err := sess.persist(ctx, e); err != nil {
			return err
		}
		if label := labelFromEntry(e); label != "" {
			if _, err := store.db.ExecContext(ctx,
				`UPDATE sessions SET label = ? WHERE id = ?`, label, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func labelFromEntry(e Entry) string {
	if e.Kind == EntryLabel {
		return e.Content
	}
	return ""
}
