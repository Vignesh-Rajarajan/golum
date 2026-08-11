package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func (s *SQLiteSession) AppendRecord(r Record) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.ID == "" {
		r.ID = NewRecordID()
	}
	if r.Lane == "" {
		r.Lane = "main"
	}
	if err := r.Validate(); err != nil {
		return Record{}, err
	}
	if existing, ok, err := s.lookupRecord(context.Background(), r.ID); err != nil {
		return Record{}, err
	} else if ok {
		return existing, nil
	}
	tx, err := s.store.db.Begin()
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.QueryRow(
		`UPDATE sessions SET next_seq = next_seq + 1 WHERE id = ? RETURNING next_seq - 1`,
		s.ID()).Scan(&r.Seq); err != nil {
		return Record{}, fmt.Errorf("allocate session sequence: %w", err)
	}
	if r.Time.IsZero() {
		r.Time = time.Now().UTC()
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return Record{}, err
	}
	if _, err := tx.Exec(
		`INSERT INTO records(id, session_id, seq, lane, type, run_id, payload, created_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		r.ID, s.ID(), r.Seq, r.Lane, string(r.Type), nullable(r.RunID), string(payload),
		r.Time.UnixMilli()); err != nil {
		return Record{}, err
	}
	if _, err := tx.Exec(`UPDATE sessions SET updated_at=? WHERE id=?`, nowMillis(), s.ID()); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	s.InMemorySession.mu.Lock()
	s.InMemorySession.records = append(s.InMemorySession.records, r)
	if int(r.Seq) > s.InMemorySession.seq {
		s.InMemorySession.seq = int(r.Seq)
	}
	s.InMemorySession.mu.Unlock()
	return r, nil
}

func (s *SQLiteSession) FindRecords(q RecordQuery) ([]Record, error) {
	query := `SELECT id, seq, lane, type, run_id, payload, created_at
	            FROM records WHERE session_id=?`
	args := []any{s.ID()}
	if q.Lane != "" {
		query += ` AND lane=?`
		args = append(args, q.Lane)
	}
	if q.RunID != "" {
		query += ` AND run_id=?`
		args = append(args, q.RunID)
	}
	if q.Type != "" {
		query += ` AND type=?`
		args = append(args, string(q.Type))
	}
	if q.After != 0 {
		query += ` AND seq>?`
		args = append(args, q.After)
	}
	query += ` ORDER BY seq ASC`
	if q.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, q.Limit)
	}
	rows, err := s.store.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecords(rows)
}

func (s *SQLiteStore) loadRecords(ctx context.Context, sessionID string) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, seq, lane, type, run_id, payload, created_at
		   FROM records WHERE session_id=? ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecords(rows)
}

func (s *SQLiteSession) FindOpenOperations(lane string, limit int) ([]Record, error) {
	if lane == "" {
		lane = "main"
	}
	query := `SELECT r.id, r.seq, r.lane, r.type, r.run_id, r.payload, r.created_at
	            FROM records r
	           WHERE r.session_id=? AND r.lane=? AND r.type=?
	             AND NOT EXISTS (
	               SELECT 1 FROM records f
	                WHERE f.session_id=r.session_id AND f.lane=r.lane
	                  AND f.run_id=r.run_id AND f.type=?
	             )
	           ORDER BY r.seq DESC`
	args := []any{s.ID(), lane, string(RecordOperationStarted), string(RecordOperationFinished)}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.store.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecords(rows)
}

func (s *SQLiteSession) lookupRecord(ctx context.Context, id string) (Record, bool, error) {
	row := s.store.db.QueryRowContext(ctx,
		`SELECT id, seq, lane, type, run_id, payload, created_at
		   FROM records WHERE session_id=? AND id=?`, s.ID(), id)
	r, err := scanRecord(row)
	if err == sql.ErrNoRows {
		return Record{}, false, nil
	}
	return r, err == nil, err
}

type recordScanner interface{ Scan(...any) error }

func scanRecord(row recordScanner) (Record, error) {
	var (
		r         Record
		typ       string
		runID     sql.NullString
		payload   string
		createdAt int64
	)
	if err := row.Scan(&r.ID, &r.Seq, &r.Lane, &typ, &runID, &payload, &createdAt); err != nil {
		return Record{}, err
	}
	if strings.TrimSpace(payload) != "" {
		if err := json.Unmarshal([]byte(payload), &r); err != nil {
			return Record{}, err
		}
	}
	r.Type, r.RunID, r.Time = RecordType(typ), runID.String, time.UnixMilli(createdAt).UTC()
	return r, nil
}

func scanRecords(rows *sql.Rows) ([]Record, error) {
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
