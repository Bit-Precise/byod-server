package byodserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	_ "github.com/lib/pq"
	"net/url"
	"strings"
	"time"
)

type PostgresStore struct{ db *sql.DB }

func (s *PostgresStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

type StoredExam struct {
	ID        string         `json:"id"`
	BaseURL   string         `json:"base_url"`
	State     string         `json:"state"`
	StartsAt  *time.Time     `json:"starts_at,omitempty"`
	EndsAt    *time.Time     `json:"ends_at,omitempty"`
	Policy    map[string]any `json:"policy,omitempty"`
	UpdatedAt string         `json:"updated_at,omitempty"`
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

type StoredTunnelTicket struct {
	SessionID  string
	ExamID     string
	EndpointID string
	ExpiresAt  time.Time
}

func OpenPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS byod_schema_migrations(version INTEGER PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS byod_exams(exam_id TEXT PRIMARY KEY,base_url TEXT NOT NULL,policy_json JSONB NOT NULL DEFAULT '{}'::jsonb,state TEXT NOT NULL DEFAULT 'draft',starts_at TIMESTAMPTZ,ends_at TIMESTAMPTZ,updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS byod_exam_students(exam_id TEXT NOT NULL REFERENCES byod_exams(exam_id) ON DELETE CASCADE,subject TEXT NOT NULL,display_name TEXT NOT NULL DEFAULT '',enabled BOOLEAN NOT NULL DEFAULT true,PRIMARY KEY(exam_id,subject));
CREATE TABLE IF NOT EXISTS byod_sessions(id TEXT PRIMARY KEY,exam_id TEXT NOT NULL,attempt_id TEXT NOT NULL DEFAULT '',browser_session_id TEXT NOT NULL DEFAULT '',subject TEXT NOT NULL DEFAULT '',state TEXT NOT NULL,created_at TIMESTAMPTZ NOT NULL,last_seen_at TIMESTAMPTZ NOT NULL,violation_count INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS byod_events(id TEXT PRIMARY KEY,session_id TEXT NOT NULL REFERENCES byod_sessions(id) ON DELETE CASCADE,attempt_id TEXT NOT NULL DEFAULT '',browser_session_id TEXT NOT NULL DEFAULT '',type TEXT NOT NULL,severity TEXT NOT NULL,details TEXT NOT NULL DEFAULT '',occurred_at TIMESTAMPTZ NOT NULL);
CREATE TABLE IF NOT EXISTS byod_tunnel_tickets(ticket_hash BYTEA PRIMARY KEY,session_id TEXT NOT NULL REFERENCES byod_sessions(id) ON DELETE CASCADE,exam_id TEXT NOT NULL,endpoint_id TEXT NOT NULL,expires_at TIMESTAMPTZ NOT NULL,used_at TIMESTAMPTZ,created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS byod_sessions_exam_created_idx ON byod_sessions(exam_id,created_at DESC);
CREATE INDEX IF NOT EXISTS byod_events_session_occurred_idx ON byod_events(session_id,occurred_at);
CREATE INDEX IF NOT EXISTS byod_tunnel_tickets_session_idx ON byod_tunnel_tickets(session_id,expires_at);
ALTER TABLE byod_exams ADD COLUMN IF NOT EXISTS state TEXT NOT NULL DEFAULT 'draft';ALTER TABLE byod_exams ADD COLUMN IF NOT EXISTS starts_at TIMESTAMPTZ;ALTER TABLE byod_exams ADD COLUMN IF NOT EXISTS ends_at TIMESTAMPTZ;ALTER TABLE byod_sessions ADD COLUMN IF NOT EXISTS attempt_id TEXT NOT NULL DEFAULT '';ALTER TABLE byod_sessions ADD COLUMN IF NOT EXISTS browser_session_id TEXT NOT NULL DEFAULT '';ALTER TABLE byod_events ADD COLUMN IF NOT EXISTS attempt_id TEXT NOT NULL DEFAULT '';ALTER TABLE byod_events ADD COLUMN IF NOT EXISTS browser_session_id TEXT NOT NULL DEFAULT '';INSERT INTO byod_schema_migrations(version) VALUES(1) ON CONFLICT DO NOTHING;`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &PostgresStore{db: db}, nil
}
func (s *PostgresStore) ListExams(ctx context.Context) ([]StoredExam, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT exam_id,base_url,state,starts_at,ends_at,policy_json,updated_at::text FROM byod_exams ORDER BY exam_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredExam
	for rows.Next() {
		var x StoredExam
		var p []byte
		if err := rows.Scan(&x.ID, &x.BaseURL, &x.State, &x.StartsAt, &x.EndsAt, &p, &x.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(p, &x.Policy)
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *PostgresStore) ListStudents(ctx context.Context, id string) ([]StoredStudent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT subject,display_name,enabled FROM byod_exam_students WHERE exam_id=$1 ORDER BY subject`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredStudent
	for rows.Next() {
		var x StoredStudent
		if err := rows.Scan(&x.Subject, &x.DisplayName, &x.Enabled); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *PostgresStore) StudentAccess(ctx context.Context, examID, subject string) (configured, enabled bool, err error) {
	var count int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM byod_exam_students WHERE exam_id=$1`, examID).Scan(&count); err != nil {
		return
	}
	if count == 0 {
		return false, true, nil
	}
	err = s.db.QueryRowContext(ctx, `SELECT enabled FROM byod_exam_students WHERE exam_id=$1 AND subject=$2`, examID, subject).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return true, false, nil
	}
	return true, enabled, err
}
func (s *PostgresStore) SetStudent(ctx context.Context, id, subject string, enabled bool) error {
	return s.SetStudentDetails(ctx, id, subject, "", enabled)
}
func (s *PostgresStore) SetStudentDetails(ctx context.Context, id, subject, name string, enabled bool) error {
	if !validExamID(id) || subject == "" || len(subject) > 256 || len(name) > 256 {
		return errors.New("invalid student")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO byod_exam_students(exam_id,subject,display_name,enabled)VALUES($1,$2,$3,$4)ON CONFLICT(exam_id,subject)DO UPDATE SET display_name=EXCLUDED.display_name,enabled=EXCLUDED.enabled`, id, subject, name, enabled)
	return err
}
func (s *PostgresStore) RemoveStudent(ctx context.Context, id, subject string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM byod_exam_students WHERE exam_id=$1 AND subject=$2`, id, subject)
	return err
}
func (s *PostgresStore) Close() error { return s.db.Close() }
func (s *PostgresStore) UpsertExam(ctx context.Context, id, base string) error {
	return s.UpsertExamDetails(ctx, id, base, "draft", nil, nil, nil)
}
func (s *PostgresStore) UpsertExamDetails(ctx context.Context, id, base, state string, start, end *time.Time, policy map[string]any) error {
	if !validExamID(id) {
		return errors.New("invalid exam id")
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return errors.New("invalid base_url")
	}
	if state == "" {
		state = "draft"
	}
	if state != "draft" && state != "scheduled" && state != "active" && state != "ended" {
		return errors.New("invalid state")
	}
	p := []byte(`{}`)
	if policy != nil {
		p, err = json.Marshal(policy)
		if err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO byod_exams(exam_id,base_url,state,starts_at,ends_at,policy_json)VALUES($1,$2,$3,$4,$5,$6)ON CONFLICT(exam_id)DO UPDATE SET base_url=EXCLUDED.base_url,state=EXCLUDED.state,starts_at=EXCLUDED.starts_at,ends_at=EXCLUDED.ends_at,policy_json=EXCLUDED.policy_json,updated_at=now()`, id, strings.TrimRight(base, "/"), state, start, end, p)
	return err
}
func (s *PostgresStore) DeleteExam(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM byod_exams WHERE exam_id=$1`, id)
	return err
}
func (s *PostgresStore) Upstream(ctx context.Context, id string) (*url.URL, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT base_url FROM byod_exams WHERE exam_id=$1`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	u, err := url.Parse(raw)
	return u, true, err
}

func (s *PostgresStore) ExamState(ctx context.Context, id string) (string, bool, error) {
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT state FROM byod_exams WHERE exam_id=$1`, id).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return state, true, nil
}

func (s *PostgresStore) Policy(ctx context.Context, id string) (map[string]any, error) {
	var raw []byte
	if err := s.db.QueryRowContext(ctx, `SELECT policy_json FROM byod_exams WHERE exam_id=$1`, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}
func (s *PostgresStore) SaveSession(ctx context.Context, x *Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO byod_sessions(id,exam_id,attempt_id,browser_session_id,subject,state,created_at,last_seen_at,violation_count)VALUES($1,$2,$3,$4,$5,$6,to_timestamp($7),to_timestamp($8),$9)ON CONFLICT(id)DO UPDATE SET subject=EXCLUDED.subject,state=EXCLUDED.state,last_seen_at=EXCLUDED.last_seen_at,violation_count=EXCLUDED.violation_count`, x.ID, x.ExamID, x.AttemptID, x.BrowserSessionID, x.Subject, x.State, x.CreatedAt, x.LastSeenAt, x.ViolationCount)
	return err
}
func (s *PostgresStore) SaveEvent(ctx context.Context, x ExamEvent) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO byod_events(id,session_id,attempt_id,browser_session_id,type,severity,details,occurred_at)VALUES($1,$2,$3,$4,$5,$6,$7,to_timestamp($8))ON CONFLICT(id)DO NOTHING`, x.ID, x.SessionID, x.AttemptID, x.BrowserSessionID, x.Type, x.Severity, x.Details, x.OccurredAt)
	return err
}

func (s *PostgresStore) CreateTunnelTicket(ctx context.Context, hash []byte, sessionID, examID, endpointID string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO byod_tunnel_tickets(ticket_hash,session_id,exam_id,endpoint_id,expires_at)VALUES($1,$2,$3,$4,$5)`, hash, sessionID, examID, endpointID, expiresAt)
	return err
}

func (s *PostgresStore) ConsumeTunnelTicket(ctx context.Context, hash []byte, endpointID string, now time.Time) (StoredTunnelTicket, bool, error) {
	var result StoredTunnelTicket
	err := s.db.QueryRowContext(ctx, `UPDATE byod_tunnel_tickets t SET used_at=$3 FROM byod_sessions s WHERE t.ticket_hash=$1 AND t.session_id=s.id AND t.endpoint_id=$2 AND t.used_at IS NULL AND t.expires_at>$3 AND s.state='active' RETURNING t.session_id,t.exam_id,t.endpoint_id,t.expires_at`, hash, endpointID, now).Scan(&result.SessionID, &result.ExamID, &result.EndpointID, &result.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredTunnelTicket{}, false, nil
	}
	if err != nil {
		return StoredTunnelTicket{}, false, err
	}
	return result, true, nil
}

// LookupTunnelTicket validates a short-lived ticket without consuming it.
// CONNECT clients may open more than one source connection during a page load;
// the ticket remains bounded by its expiry and active-session state and is
// revoked when the session ends.
func (s *PostgresStore) LookupTunnelTicket(ctx context.Context, hash []byte, endpointID string, now time.Time) (StoredTunnelTicket, bool, error) {
	var result StoredTunnelTicket
	err := s.db.QueryRowContext(ctx, `SELECT t.session_id,t.exam_id,t.endpoint_id,t.expires_at FROM byod_tunnel_tickets t JOIN byod_sessions s ON t.session_id=s.id WHERE t.ticket_hash=$1 AND t.endpoint_id=$2 AND t.expires_at>$3 AND s.state='active'`, hash, endpointID, now).Scan(&result.SessionID, &result.ExamID, &result.EndpointID, &result.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredTunnelTicket{}, false, nil
	}
	if err != nil {
		return StoredTunnelTicket{}, false, err
	}
	return result, true, nil
}

func (s *PostgresStore) RevokeTunnelTickets(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE byod_tunnel_tickets SET used_at=now() WHERE session_id=$1 AND used_at IS NULL`, sessionID)
	return err
}
func (s *PostgresStore) ListSessions(ctx context.Context, id string) ([]StoredSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,exam_id,subject,state,created_at,last_seen_at,violation_count FROM byod_sessions WHERE exam_id=$1 ORDER BY created_at DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredSession
	for rows.Next() {
		var x StoredSession
		if err := rows.Scan(&x.ID, &x.ExamID, &x.Subject, &x.State, &x.CreatedAt, &x.LastSeenAt, &x.ViolationCount); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListAllSessions(ctx context.Context) ([]StoredSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,exam_id,subject,state,created_at,last_seen_at,violation_count FROM byod_sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredSession
	for rows.Next() {
		var x StoredSession
		if err := rows.Scan(&x.ID, &x.ExamID, &x.Subject, &x.State, &x.CreatedAt, &x.LastSeenAt, &x.ViolationCount); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *PostgresStore) GetSession(ctx context.Context, id string) (*StoredSession, error) {
	var x StoredSession
	err := s.db.QueryRowContext(ctx, `SELECT id,exam_id,subject,state,created_at,last_seen_at,violation_count FROM byod_sessions WHERE id=$1`, id).Scan(&x.ID, &x.ExamID, &x.Subject, &x.State, &x.CreatedAt, &x.LastSeenAt, &x.ViolationCount)
	if err != nil {
		return nil, err
	}
	return &x, nil
}
func (s *PostgresStore) ListEvents(ctx context.Context, id string) ([]ExamEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,session_id,attempt_id,browser_session_id,type,severity,details,EXTRACT(EPOCH FROM occurred_at)::bigint FROM byod_events WHERE session_id=$1 ORDER BY occurred_at`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}
func (s *PostgresStore) ListAllEvents(ctx context.Context, n int) ([]ExamEvent, error) {
	if n <= 0 || n > 1000 {
		n = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,session_id,attempt_id,browser_session_id,type,severity,details,EXTRACT(EPOCH FROM occurred_at)::bigint FROM byod_events ORDER BY occurred_at DESC LIMIT $1`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

type eventRows interface {
	Next() bool
	Scan(...any) error
}

func scanEvents(rows eventRows) ([]ExamEvent, error) {
	var out []ExamEvent
	for rows.Next() {
		var x ExamEvent
		if err := rows.Scan(&x.ID, &x.SessionID, &x.AttemptID, &x.BrowserSessionID, &x.Type, &x.Severity, &x.Details, &x.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, nil
}
