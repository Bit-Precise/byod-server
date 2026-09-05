package middleware

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"time"

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

type StoredSession struct {
	ID             string    `json:"id"`
	ExamID         string    `json:"exam_id"`
	Subject        string    `json:"subject"`
	State          string    `json:"state"`
	CreatedAt      time.Time `json:"created_at"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	ViolationCount int       `json:"violation_count"`
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
	);
	CREATE TABLE IF NOT EXISTS byod_sessions (
		id TEXT PRIMARY KEY, exam_id TEXT NOT NULL, subject TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL, last_seen_at TIMESTAMPTZ NOT NULL,
		violation_count INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS byod_events (
		id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES byod_sessions(id) ON DELETE CASCADE,
		type TEXT NOT NULL, severity TEXT NOT NULL, details TEXT NOT NULL DEFAULT '', occurred_at TIMESTAMPTZ NOT NULL
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

func (s *PostgresStore) SaveSession(ctx context.Context, session *Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO byod_sessions (id, exam_id, subject, state, created_at, last_seen_at, violation_count)
		VALUES ($1,$2,$3,$4,to_timestamp($5),to_timestamp($6),$7)
		ON CONFLICT (id) DO UPDATE SET subject=EXCLUDED.subject,state=EXCLUDED.state,last_seen_at=EXCLUDED.last_seen_at,violation_count=EXCLUDED.violation_count`, session.ID, session.ExamID, session.Subject, session.State, session.CreatedAt, session.LastSeenAt, session.ViolationCount)
	return err
}

func (s *PostgresStore) SaveEvent(ctx context.Context, event ExamEvent) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO byod_events (id, session_id, type, severity, details, occurred_at)
		VALUES ($1,$2,$3,$4,$5,to_timestamp($6)) ON CONFLICT (id) DO NOTHING`, event.ID, event.SessionID, event.Type, event.Severity, event.Details, event.OccurredAt)
	return err
}

func (s *PostgresStore) ListSessions(ctx context.Context, examID string) ([]StoredSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, exam_id, subject, state, created_at, last_seen_at, violation_count FROM byod_sessions WHERE exam_id=$1 ORDER BY created_at DESC`, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []StoredSession
	for rows.Next() {
		var item StoredSession
		if err := rows.Scan(&item.ID, &item.ExamID, &item.Subject, &item.State, &item.CreatedAt, &item.LastSeenAt, &item.ViolationCount); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *PostgresStore) ListEvents(ctx context.Context, sessionID string) ([]ExamEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id, type, severity, details, EXTRACT(EPOCH FROM occurred_at)::bigint FROM byod_events WHERE session_id=$1 ORDER BY occurred_at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ExamEvent
	for rows.Next() {
		var item ExamEvent
		if err := rows.Scan(&item.ID, &item.SessionID, &item.Type, &item.Severity, &item.Details, &item.OccurredAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
