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

type StoredExam struct {
	ID        string `json:"id"`
	BaseURL   string `json:"base_url"`
	State     string `json:"state"`
	UpdatedAt string `json:"updated_at"`
}

type StoredStudent struct {
	Subject     string `json:"subject"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
}

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
	);
	CREATE TABLE IF NOT EXISTS byod_exam_students (
		exam_id TEXT NOT NULL REFERENCES byod_exams(exam_id) ON DELETE CASCADE,
		subject TEXT NOT NULL,
		display_name TEXT NOT NULL DEFAULT '',
		enabled BOOLEAN NOT NULL DEFAULT true,
		PRIMARY KEY (exam_id, subject)
	)`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) ListExams(ctx context.Context) ([]StoredExam, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT exam_id, base_url, 'draft', updated_at::text FROM byod_exams ORDER BY exam_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var exams []StoredExam
	for rows.Next() {
		var exam StoredExam
		if err := rows.Scan(&exam.ID, &exam.BaseURL, &exam.State, &exam.UpdatedAt); err != nil {
			return nil, err
		}
		exams = append(exams, exam)
	}
	return exams, rows.Err()
}

func (s *PostgresStore) ListStudents(ctx context.Context, examID string) ([]StoredStudent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT subject, display_name, enabled FROM byod_exam_students WHERE exam_id=$1 ORDER BY subject`, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var students []StoredStudent
	for rows.Next() {
		var student StoredStudent
		if err := rows.Scan(&student.Subject, &student.DisplayName, &student.Enabled); err != nil {
			return nil, err
		}
		students = append(students, student)
	}
	return students, rows.Err()
}

func (s *PostgresStore) SetStudent(ctx context.Context, examID, subject string, enabled bool) error {
	if !validExamID(examID) || subject == "" || len(subject) > 256 {
		return errors.New("invalid student")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO byod_exam_students (exam_id, subject, enabled) VALUES ($1,$2,$3)
		ON CONFLICT (exam_id, subject) DO UPDATE SET enabled=EXCLUDED.enabled`, examID, subject, enabled)
	return err
}

func (s *PostgresStore) RemoveStudent(ctx context.Context, examID, subject string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM byod_exam_students WHERE exam_id=$1 AND subject=$2`, examID, subject)
	return err
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

func (s *PostgresStore) DeleteExam(ctx context.Context, examID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM byod_exams WHERE exam_id=$1`, examID)
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
