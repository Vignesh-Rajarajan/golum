package session

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
)

func TestAppendProvisionedIsIdempotent(t *testing.T) {
	store := testStore(t)
	raw, err := store.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sess := raw.(*SQLiteSession)
	p := ProvisionedEntry{ID: NewEntryID(), Kind: EntryUserMessage, Role: "user", Content: "hello"}
	first, err := sess.AppendProvisioned(p)
	if err != nil {
		t.Fatal(err)
	}
	second, err := sess.AppendProvisioned(p)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || len(sess.Entries()) != 1 {
		t.Fatalf("idempotent append produced duplicate: %#v %#v", first, second)
	}

	p.Content = "different"
	_, err = sess.AppendProvisioned(p)
	var mismatch *ProvisionedEntryMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected provisioned mismatch, got %v", err)
	}
}

func TestAppendProvisionedMatchesJSONNormalizedMetadata(t *testing.T) {
	type todo struct {
		Text string `json:"text"`
		Done bool   `json:"done"`
	}
	store := testStore(t)
	raw, err := store.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p := ProvisionedEntry{
		ID: NewEntryID(), Kind: EntryTodos,
		Meta: map[string]any{"items": []todo{{Text: "ship it", Done: true}}},
	}
	if _, err := raw.AppendProvisioned(p); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(context.Background(), raw.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.AppendProvisioned(p); err != nil {
		t.Fatalf("semantically identical metadata mismatched after JSON round trip: %v", err)
	}
	if got := len(reopened.Entries()); got != 1 {
		t.Fatalf("idempotent append produced %d entries", got)
	}
}

func TestEntriesAndRecordsShareSequence(t *testing.T) {
	store := testStore(t)
	raw, err := store.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sess := raw.(*SQLiteSession)
	e1, err := sess.AppendUserMessage("one")
	if err != nil {
		t.Fatal(err)
	}
	r, err := sess.AppendRecord(Record{
		Type: RecordOperationStarted, RunID: "run_1",
		Intent: &OperationIntent{Kind: "run"},
	})
	if err != nil {
		t.Fatal(err)
	}
	e2, err := sess.AppendSystemNotice("two")
	if err != nil {
		t.Fatal(err)
	}
	if !(int64(e1.Seq) < r.Seq && r.Seq < int64(e2.Seq)) {
		t.Fatalf("sequence is not shared and increasing: entry=%d record=%d entry=%d", e1.Seq, r.Seq, e2.Seq)
	}
}

func TestRecordsSurviveReopenAndForkRemapsIdentities(t *testing.T) {
	store := testStore(t)
	raw, err := store.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runID := "source_run"
	if _, err := raw.AppendRecord(Record{
		Type: RecordOperationStarted, RunID: runID,
		Intent: &OperationIntent{Kind: "run"},
	}); err != nil {
		t.Fatal(err)
	}
	answerID := NewEntryID()
	if _, err := raw.AppendRecord(Record{
		Type: RecordStepAttempt, RunID: runID, Step: "assistant",
		Attempt: 1, ResultEntryID: answerID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.AppendProvisioned(ProvisionedEntry{
		ID: answerID, Kind: EntryAssistantMessage, Role: "assistant", Content: "done",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.AppendRecord(Record{
		Type: RecordOperationFinished, RunID: runID, Outcome: "completed",
	}); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(context.Background(), raw.ID())
	if err != nil {
		t.Fatal(err)
	}
	reopenedRecords, err := reopened.FindRecords(RecordQuery{Lane: "main"})
	if err != nil || len(reopenedRecords) != 3 {
		t.Fatalf("reopened records=%d err=%v", len(reopenedRecords), err)
	}

	forked, err := store.Fork(context.Background(), raw.ID(), "")
	if err != nil {
		t.Fatal(err)
	}
	forkRecords, err := forked.FindRecords(RecordQuery{Lane: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(forkRecords) != 3 {
		t.Fatalf("fork records=%d want 3", len(forkRecords))
	}
	if forkRecords[0].RunID == runID || forkRecords[0].RunID == "" {
		t.Fatalf("run id was not remapped: %q", forkRecords[0].RunID)
	}
	var step Record
	for _, r := range forkRecords {
		if r.Type == RecordStepAttempt {
			step = r
		}
	}
	if _, ok := forked.GetEntry(step.ResultEntryID); !ok {
		t.Fatalf("step result id %q was not remapped to forked entry", step.ResultEntryID)
	}
	last := int64(-1)
	type sequenced struct{ seq int64 }
	var all []sequenced
	for _, e := range forked.Entries() {
		all = append(all, sequenced{int64(e.Seq)})
	}
	for _, r := range forkRecords {
		all = append(all, sequenced{r.Seq})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].seq < all[j].seq })
	for _, item := range all {
		if item.seq <= last {
			t.Fatalf("fork sequence is not strictly increasing: %d after %d", item.seq, last)
		}
		last = item.seq
	}
}

func TestV1MigrationBackfillsNextSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO schema_meta VALUES ('version','1');
		CREATE TABLE sessions (
		  id TEXT PRIMARY KEY, root_id TEXT NOT NULL, parent_session_id TEXT,
		  forked_from_entry_id TEXT, label TEXT, cwd TEXT NOT NULL DEFAULT '',
		  model TEXT, leaf_entry_id TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
		);
		CREATE TABLE entries (
		  id TEXT PRIMARY KEY, session_id TEXT NOT NULL, parent_id TEXT, seq INTEGER NOT NULL,
		  kind TEXT NOT NULL, role TEXT, content TEXT, tool_call_id TEXT, meta TEXT,
		  token_count INTEGER, created_at INTEGER NOT NULL
		);
		INSERT INTO sessions(id, root_id, leaf_entry_id, created_at, updated_at)
		  VALUES('legacy','legacy','e_old',1,1);
		INSERT INTO entries(id, session_id, seq, kind, role, content, created_at)
		  VALUES('e_old','legacy',7,'user_message','user','old',1);
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenSQLiteStore(path, &config.Config{Model: "gpt-4o"}, prompt.PromptConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	raw, err := store.Open(context.Background(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	e, err := raw.AppendUserMessage("new")
	if err != nil {
		t.Fatal(err)
	}
	if e.Seq != 8 {
		t.Fatalf("migrated sequence=%d, want 8", e.Seq)
	}
	var version string
	if err := store.DB().QueryRow(`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "2" {
		t.Fatalf("schema version=%q", version)
	}
	if e.Time.Before(time.UnixMilli(1)) {
		t.Fatal("new entry timestamp was not assigned")
	}
}
