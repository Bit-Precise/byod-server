package middleware

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"

	_ "github.com/lib/pq"
)

// PostgresStore keeps operator-managed exam metadata out of pod environment
// variables. The database URL is only a connection credential; exam source
// URLs live in the byod_exams table.
type PostgresStore struct{ db *sql.DB }

func OpenPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS byod_exams (
		exam_id TEXT PRIMARY KEY,
		base_url TEXT NOT NULL,
		policy_json JSONB NOT NULL DEFAULT '{}'::jsonb,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) Close() error { return s.db.Close() }

func (s *PostgresStore) UpsertExam(ctx context.Context, examID, baseURL string) error {
	if !validExamID(examID) {
		return errors.New("invalid exam id")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return errors.New("base_url must be an absolute HTTP(S) URL without credentials or fragment")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO byod_exams (exam_id, base_url) VALUES ($1,$2)
		ON CONFLICT (exam_id) DO UPDATE SET base_url=EXCLUDED.base_url, updated_at=now()`, examID, strings.TrimRight(baseURL, "/"))
	return err
}

func (s *PostgresStore) Upstream(ctx context.Context, examID string) (*url.URL, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT base_url FROM byod_exams WHERE exam_id=$1`, examID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, false, err
	}
	return u, true, nil
}
