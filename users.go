package byodserver

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/mail"
	"strings"
	"time"
)

const userSchema = `
CREATE TABLE IF NOT EXISTS byod_users(
 id TEXT PRIMARY KEY, issuer TEXT NOT NULL, subject TEXT,
 email TEXT, display_name TEXT NOT NULL DEFAULT '',
 role TEXT NOT NULL DEFAULT 'student' CHECK(role IN ('student','admin')),
 enabled BOOLEAN NOT NULL DEFAULT true, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 last_login_at TIMESTAMPTZ, UNIQUE(issuer,subject), UNIQUE(issuer,email));
CREATE TABLE IF NOT EXISTS byod_exam_participants(
 exam_id TEXT NOT NULL REFERENCES byod_exams(exam_id) ON DELETE CASCADE,
 user_id TEXT NOT NULL REFERENCES byod_users(id), enabled BOOLEAN NOT NULL DEFAULT true,
 PRIMARY KEY(exam_id,user_id));
CREATE TABLE IF NOT EXISTS byod_user_sessions(
 token_hash BYTEA PRIMARY KEY, user_id TEXT NOT NULL REFERENCES byod_users(id), expires_at TIMESTAMPTZ NOT NULL);
CREATE TABLE IF NOT EXISTS byod_login_states(
 state_hash BYTEA PRIMARY KEY, binding_hash BYTEA NOT NULL, verifier TEXT NOT NULL, nonce TEXT NOT NULL,
 destination TEXT NOT NULL, expires_at TIMESTAMPTZ NOT NULL);
CREATE TABLE IF NOT EXISTS byod_user_audit(
 id BIGSERIAL PRIMARY KEY, actor_id TEXT NOT NULL, user_id TEXT NOT NULL REFERENCES byod_users(id),
 action TEXT NOT NULL, details TEXT NOT NULL DEFAULT '', occurred_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS byod_user_audit_time_idx ON byod_user_audit(occurred_at DESC);
INSERT INTO byod_schema_migrations(version) VALUES(2) ON CONFLICT DO NOTHING;
`

type User struct {
	ID          string     `json:"id"`
	Issuer      string     `json:"issuer"`
	Subject     *string    `json:"subject"`
	Email       *string    `json:"email"`
	DisplayName string     `json:"display_name"`
	Role        string     `json:"role"`
	Enabled     bool       `json:"enabled"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at"`
}
type Participant struct {
	User    User `json:"user"`
	Enabled bool `json:"enabled"`
}
type UserAudit struct {
	ID         int64     `json:"id"`
	ActorID    string    `json:"actor_id"`
	UserID     string    `json:"user_id"`
	Action     string    `json:"action"`
	Details    string    `json:"details"`
	OccurredAt time.Time `json:"occurred_at"`
}

var ErrIdentityConflict = errors.New("identity_conflict")
var ErrUserDisabled = errors.New("user_disabled")
var ErrLastAdmin = errors.New("last_admin_required")

const userColumns = `id,issuer,subject,email,display_name,role,enabled,created_at,last_login_at`

type scanner interface{ Scan(...any) error }

func scanUser(row scanner) (u User, err error) {
	err = row.Scan(&u.ID, &u.Issuer, &u.Subject, &u.Email, &u.DisplayName, &u.Role, &u.Enabled, &u.CreatedAt, &u.LastLoginAt)
	return
}
func normalizeEmail(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	addr, err := mail.ParseAddress(value)
	if err != nil || addr.Address != value || len(value) > 254 || !strings.Contains(value, "@") {
		return "", errors.New("invalid_email")
	}
	return value, nil
}
func NormalizeEmailForConfig(raw string) (string, error) { return normalizeEmail(raw) }
func digestToken(token string) []byte                    { sum := sha256.Sum256([]byte(token)); return sum[:] }
func auditUser(ctx context.Context, tx *sql.Tx, actor, id, action, details string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO byod_user_audit(actor_id,user_id,action,details) VALUES($1,$2,$3,$4)`, actor, id, action, details)
	return err
}

// Serializes invitations, identity binding and role changes across replicas.
// In particular two concurrent demotions cannot remove the last administrator.
func lockUsers(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(728401125)`)
	return err
}
func (s *PostgresStore) ResolveIdentity(ctx context.Context, identity OIDCIdentity) (User, error) {
	if identity.Issuer == "" || identity.Subject == "" {
		return User{}, errors.New("invalid_identity")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if err = lockUsers(ctx, tx); err != nil {
		return User{}, err
	}
	email := ""
	if identity.EmailVerified {
		email, _ = normalizeEmail(identity.Email)
	}
	u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM byod_users WHERE issuer=$1 AND subject=$2`, identity.Issuer, identity.Subject))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return User{}, err
	}
	if errors.Is(err, sql.ErrNoRows) && email != "" {
		u, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM byod_users WHERE issuer=$1 AND email=$2`, identity.Issuer, email))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return User{}, err
		}
		if err == nil && u.Subject != nil {
			return User{}, ErrIdentityConflict
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		role := "student"
		if email != "" && s.AdminEmails[email] {
			role = "admin"
		}
		u, err = scanUser(tx.QueryRowContext(ctx, `INSERT INTO byod_users(id,issuer,subject,email,display_name,role) VALUES($1,$2,$3,NULLIF($4,''),$5,$6) RETURNING `+userColumns, randomToken(18), identity.Issuer, identity.Subject, email, identity.Name, role))
		if err != nil {
			return User{}, err
		}
		if err = auditUser(ctx, tx, u.ID, u.ID, "user_registered", ""); err != nil {
			return User{}, err
		}
	} else if u.Subject == nil {
		if !u.Enabled {
			return User{}, ErrUserDisabled
		}
		if _, err = tx.ExecContext(ctx, `UPDATE byod_users SET subject=$2 WHERE id=$1`, u.ID, identity.Subject); err != nil {
			return User{}, err
		}
		if err = auditUser(ctx, tx, u.ID, u.ID, "identity_linked", "verified_email"); err != nil {
			return User{}, err
		}
	}
	if !u.Enabled {
		return User{}, ErrUserDisabled
	}
	// An existing subject is authoritative. Never merge accounts or move privileges
	// just because an IdP later changes its email claim.
	u, err = scanUser(tx.QueryRowContext(ctx, `UPDATE byod_users SET last_login_at=now(),display_name=CASE WHEN display_name='' THEN $2 ELSE display_name END WHERE id=$1 RETURNING `+userColumns, u.ID, identity.Name))
	if err != nil {
		return User{}, err
	}
	// Import old subject-based rosters only after a real login binds the identity.
	_, err = tx.ExecContext(ctx, `INSERT INTO byod_exam_participants(exam_id,user_id,enabled) SELECT exam_id,$1,enabled FROM byod_exam_students WHERE subject=$2 ON CONFLICT DO NOTHING`, u.ID, identity.Subject)
	if err != nil {
		return User{}, err
	}
	if err = auditUser(ctx, tx, u.ID, u.ID, "login", ""); err != nil {
		return User{}, err
	}
	return u, tx.Commit()
}
func (s *PostgresStore) InviteUser(ctx context.Context, issuer, email, name, role, actor string) (User, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	if issuer == "" || (role != "student" && role != "admin") || len(name) > 256 {
		return User{}, errors.New("invalid_user")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if err = lockUsers(ctx, tx); err != nil {
		return User{}, err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM byod_users WHERE issuer=$1 AND email=$2)`, issuer, email).Scan(&exists); err != nil {
		return User{}, err
	}
	if exists {
		return User{}, errors.New("email_exists")
	}
	u, err := scanUser(tx.QueryRowContext(ctx, `INSERT INTO byod_users(id,issuer,email,display_name,role) VALUES($1,$2,$3,$4,$5) RETURNING `+userColumns, randomToken(18), issuer, email, name, role))
	if err != nil {
		return User{}, err
	}
	if err = auditUser(ctx, tx, actor, u.ID, "user_invited", role); err != nil {
		return User{}, err
	}
	return u, tx.Commit()
}
func (s *PostgresStore) ListUsers(ctx context.Context, issuer, query string, limit, offset int) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM byod_users WHERE issuer=$1 AND ($2='' OR strpos(lower(coalesce(email,'')||' '||display_name||' '||coalesce(subject,'')),lower($2))>0) ORDER BY created_at,id LIMIT $3 OFFSET $4`, issuer, query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}
func (s *PostgresStore) GetUser(ctx context.Context, id string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM byod_users WHERE id=$1`, id))
}
func (s *PostgresStore) UpdateUser(ctx context.Context, id, role, name string, enabled bool, actor string) (User, error) {
	if (role != "student" && role != "admin") || len(name) > 256 {
		return User{}, errors.New("invalid_user")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if err = lockUsers(ctx, tx); err != nil {
		return User{}, err
	}
	old, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM byod_users WHERE id=$1`, id))
	if err != nil {
		return User{}, err
	}
	if old.Role == "admin" && old.Enabled && old.Subject != nil && (role != "admin" || !enabled) {
		var others int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM byod_users WHERE issuer=$1 AND role='admin' AND enabled AND subject IS NOT NULL AND id<>$2`, old.Issuer, id).Scan(&others); err != nil {
			return User{}, err
		}
		if others == 0 {
			return User{}, ErrLastAdmin
		}
	}
	u, err := scanUser(tx.QueryRowContext(ctx, `UPDATE byod_users SET role=$2,display_name=$3,enabled=$4 WHERE id=$1 RETURNING `+userColumns, id, role, name, enabled))
	if err != nil {
		return User{}, err
	}
	if !enabled {
		if _, err = tx.ExecContext(ctx, `DELETE FROM byod_user_sessions WHERE user_id=$1`, id); err != nil {
			return User{}, err
		}
	}
	if err = auditUser(ctx, tx, actor, id, "user_updated", string(canonicalJSON(map[string]any{"old_role": old.Role, "role": role, "old_enabled": old.Enabled, "enabled": enabled, "display_name": name}))); err != nil {
		return User{}, err
	}
	return u, tx.Commit()
}
func (s *PostgresStore) UserAccess(ctx context.Context, issuer, subject, examID string) (bool, error) {
	var allowed bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM byod_users u JOIN byod_exam_participants p ON p.user_id=u.id WHERE u.issuer=$1 AND u.subject=$2 AND u.enabled AND p.exam_id=$3 AND p.enabled)`, issuer, subject, examID).Scan(&allowed)
	return allowed, err
}
func (s *PostgresStore) SetParticipant(ctx context.Context, exam, id string, enabled bool, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO byod_exam_participants(exam_id,user_id,enabled) VALUES($1,$2,$3) ON CONFLICT(exam_id,user_id) DO UPDATE SET enabled=EXCLUDED.enabled`, exam, id, enabled); err != nil {
		return err
	}
	if err = auditUser(ctx, tx, actor, id, "exam_access_updated", string(canonicalJSON(map[string]any{"exam_id": exam, "enabled": enabled}))); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *PostgresStore) RemoveParticipant(ctx context.Context, exam, id, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM byod_exam_participants WHERE exam_id=$1 AND user_id=$2`, exam, id); err != nil {
		return err
	}
	// Remove legacy eligibility too, so the next login cannot re-import it.
	if _, err = tx.ExecContext(ctx, `DELETE FROM byod_exam_students WHERE exam_id=$1 AND subject=(SELECT subject FROM byod_users WHERE id=$2)`, exam, id); err != nil {
		return err
	}
	if err = auditUser(ctx, tx, actor, id, "exam_access_removed", exam); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *PostgresStore) ListParticipants(ctx context.Context, exam string) ([]Participant, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.id,u.issuer,u.subject,u.email,u.display_name,u.role,u.enabled,u.created_at,u.last_login_at,p.enabled FROM byod_exam_participants p JOIN byod_users u ON u.id=p.user_id WHERE p.exam_id=$1 ORDER BY u.display_name,u.id`, exam)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Participant{}
	for rows.Next() {
		var p Participant
		u := &p.User
		if err = rows.Scan(&u.ID, &u.Issuer, &u.Subject, &u.Email, &u.DisplayName, &u.Role, &u.Enabled, &u.CreatedAt, &u.LastLoginAt, &p.Enabled); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}
func (s *PostgresStore) UserAudit(ctx context.Context, limit int) ([]UserAudit, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,actor_id,user_id,action,details,occurred_at FROM byod_user_audit ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []UserAudit{}
	for rows.Next() {
		var e UserAudit
		if err = rows.Scan(&e.ID, &e.ActorID, &e.UserID, &e.Action, &e.Details, &e.OccurredAt); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}
