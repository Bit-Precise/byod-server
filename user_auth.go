package byodserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (s *Service) identityIssuer() string {
	if s.OIDC != nil && !s.DevAuth {
		return s.OIDC.Issuer
	}
	return "urn:byod:dev"
}
func (s *Service) identityFromCode(ctx context.Context, code, verifier, nonce string) (OIDCIdentity, error) {
	if s.OIDC != nil && !s.DevAuth {
		return s.OIDC.exchangeIdentity(ctx, code, verifier, nonce)
	}
	// Only local development/test adapters call this branch. Production web
	// login refuses to start unless an OIDC provider is configured.
	return OIDCIdentity{Issuer: s.identityIssuer(), Subject: "oidc:" + code, Name: "Development user"}, nil
}
func (s *Service) loginCookieName() string {
	if strings.HasPrefix(s.ExamOrigin, "https://") {
		return "__Host-byod_user"
	}
	return "byod_user"
}
func (s *Service) stateCookieName() string {
	if strings.HasPrefix(s.ExamOrigin, "https://") {
		return "__Host-byod_login"
	}
	return "byod_login"
}
func (s *Service) setUserCookie(w http.ResponseWriter, name, value string, maxAge int) {
	secure := strings.HasPrefix(s.ExamOrigin, "https://")
	sameSite := http.SameSiteLaxMode
	// The authenticated student page is served by the HTTPS exam origin. Keep
	// the user session cookie usable by same-origin API calls; SameSite=None is
	// retained for compatibility with older grips:// clients.
	if name == s.loginCookieName() && secure {
		sameSite = http.SameSiteNoneMode
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: secure, SameSite: sameSite, MaxAge: maxAge})
}
func (s *Service) csrfToken(token string) string {
	h := hmac.New(sha256.New, s.PolicySecret)
	h.Write([]byte("user-csrf:" + token))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *Service) currentUser(r *http.Request) (User, string, error) {
	cookie, err := r.Cookie(s.loginCookieName())
	if err != nil || cookie.Value == "" {
		return User{}, "", errors.New("login_required")
	}
	if s.ExamStore == nil {
		return User{}, "", errors.New("database_required")
	}
	u, err := scanUser(s.ExamStore.db.QueryRowContext(r.Context(), `SELECT u.id,u.issuer,u.subject,u.email,u.display_name,u.role,u.platform_admin,u.enabled,u.created_at,u.last_login_at FROM byod_user_sessions s JOIN byod_users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now() AND u.enabled AND u.issuer=$2`, digestToken(cookie.Value), s.identityIssuer()))
	return u, cookie.Value, err
}
func (s *Service) validCSRF(r *http.Request, token string) bool {
	return r.Header.Get("Origin") == s.ExamOrigin && hmac.Equal([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.csrfToken(token)))
}
func (s *Service) requireAdmin(w http.ResponseWriter, r *http.Request) (User, bool) {
	u, token, err := s.currentUser(r)
	if err != nil {
		s.writeJSON(w, 401, map[string]string{"error": "login_required"})
		return User{}, false
	}
	// The caller may additionally authorize an exam-admin membership. This
	// helper remains the platform-wide guard for global user/audit APIs.
	if !u.PlatformAdmin {
		s.writeJSON(w, 403, map[string]string{"error": "platform_admin_required"})
		return User{}, false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.validCSRF(r, token) {
		s.writeJSON(w, 403, map[string]string{"error": "csrf_denied"})
		return User{}, false
	}
	return u, true
}

// requireAdminAPI authenticates the web session and permits either a platform
// administrator or an administrator explicitly assigned to the exam resource
// in the URL. Global user/audit APIs remain platform-admin-only at their
// handlers.
func (s *Service) requireAdminAPI(w http.ResponseWriter, r *http.Request) (User, bool) {
	u, token, err := s.currentUser(r)
	if err != nil {
		s.writeJSON(w, 401, map[string]string{"error": "login_required"})
		return User{}, false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.validCSRF(r, token) {
		s.writeJSON(w, 403, map[string]string{"error": "csrf_denied"})
		return User{}, false
	}
	if u.PlatformAdmin {
		return u, true
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) >= 4 && parts[0] == "admin" && parts[1] == "api" && parts[2] == "exams" && s.ExamStore != nil {
		allowed, checkErr := s.ExamStore.CanManageExam(r.Context(), parts[3], u.ID)
		if checkErr == nil && allowed {
			return u, true
		}
	}
	if len(parts) >= 4 && parts[0] == "admin" && parts[1] == "api" && parts[2] == "sessions" && s.ExamStore != nil {
		allowed, checkErr := s.ExamStore.CanManageSession(r.Context(), parts[3], u.ID)
		if checkErr == nil && allowed {
			return u, true
		}
	}
	s.writeJSON(w, 403, map[string]string{"error": "exam_admin_required"})
	return User{}, false
}
func (s *Service) beginUserLogin(w http.ResponseWriter, r *http.Request) {
	if s.ExamStore == nil || (s.OIDC == nil && !s.DevAuth) {
		s.writeJSON(w, 503, map[string]string{"error": "login_not_configured"})
		return
	}
	destination := "/admin/"
	requestedDestination := r.URL.Query().Get("return_to")
	// Preserve a bookmarked admin page across OIDC, but never accept an
	// absolute or protocol-relative URL as a post-login destination.
	if isGripsExamUIReturnURI(requestedDestination) ||
		s.isExamUIReturnURI(requestedDestination) ||
		requestedDestination == "/account/" ||
		(strings.HasPrefix(requestedDestination, "/admin/") &&
			!strings.Contains(requestedDestination, "://")) {
		destination = requestedDestination
	}
	state, binding, verifier, nonce := "user-"+randomToken(24), randomToken(32), pkceVerifier(), randomToken(24)
	_, err := s.ExamStore.db.ExecContext(r.Context(), `DELETE FROM byod_login_states WHERE expires_at<now()`)
	if err == nil {
		_, err = s.ExamStore.db.ExecContext(r.Context(), `INSERT INTO byod_login_states(state_hash,binding_hash,verifier,nonce,destination,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, digestToken(state), digestToken(binding), verifier, nonce, destination, time.Now().Add(10*time.Minute))
	}
	if err != nil {
		s.writeJSON(w, 503, map[string]string{"error": "login_unavailable"})
		return
	}
	s.setUserCookie(w, s.stateCookieName(), binding, 600)
	target := s.ExamOrigin + "/dev/user-authorize?state=" + url.QueryEscape(state)
	if s.OIDC != nil && !s.DevAuth {
		u, _ := url.Parse(s.OIDC.authorizationURL(state, verifier))
		q := u.Query()
		q.Set("nonce", nonce)
		u.RawQuery = q.Encode()
		target = u.String()
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

// isGripsExamUIReturnURI accepts the two browser-owned aliases used by old
// and current Grips builds, but does not accept arbitrary custom-scheme URLs.
func isGripsExamUIReturnURI(raw string) bool {
	target, err := url.Parse(raw)
	if err != nil || target.Scheme != "grips" ||
		(target.Host != "exam" && target.Host != "exam.cs.ac.cn") ||
		target.User != nil || target.Fragment != "" ||
		(target.Path != "" && target.Path != "/") {
		return false
	}
	query := target.Query()
	return query.Get("auth") == "1" && len(query) == 1
}

func (s *Service) isExamUIReturnURI(raw string) bool {
	if raw == "" {
		return false
	}
	target, err := url.Parse(raw)
	origin, originErr := url.Parse(s.ExamOrigin)
	if err != nil || originErr != nil || target.Scheme != origin.Scheme ||
		target.Host != origin.Host || target.User != nil || target.Fragment != "" ||
		target.Path != "" && target.Path != "/" {
		return false
	}
	query := target.Query()
	return query.Get("auth") == "1" && len(query) == 1
}
func (s *Service) finishUserLogin(w http.ResponseWriter, r *http.Request, state, code string) bool {
	if !strings.HasPrefix(state, "user-") {
		return false
	}
	fail := func() { s.writeJSON(w, 401, map[string]string{"error": "oidc_login_failed"}) }
	cookie, err := r.Cookie(s.stateCookieName())
	if err != nil || s.ExamStore == nil || code == "" {
		fail()
		return true
	}
	var verifier, nonce, destination string
	err = s.ExamStore.db.QueryRowContext(r.Context(), `DELETE FROM byod_login_states WHERE state_hash=$1 AND binding_hash=$2 AND expires_at>now() RETURNING verifier,nonce,destination`, digestToken(state), digestToken(cookie.Value)).Scan(&verifier, &nonce, &destination)
	if err != nil {
		fail()
		return true
	}
	s.setUserCookie(w, s.stateCookieName(), "", -1)
	identity, err := s.identityFromCode(r.Context(), code, verifier, nonce)
	if err != nil {
		fail()
		return true
	}
	u, err := s.ExamStore.ResolveIdentity(r.Context(), identity)
	if err != nil {
		s.userError(w, err)
		return true
	}
	token := randomToken(32)
	// Rotate the web session on every login. No OIDC token leaves the server.
	if old, err := r.Cookie(s.loginCookieName()); err == nil {
		_, _ = s.ExamStore.db.ExecContext(r.Context(), `DELETE FROM byod_user_sessions WHERE token_hash=$1`, digestToken(old.Value))
	}
	_, err = s.ExamStore.db.ExecContext(r.Context(), `INSERT INTO byod_user_sessions(token_hash,user_id,expires_at) VALUES($1,$2,$3)`, digestToken(token), u.ID, time.Now().Add(12*time.Hour))
	if err != nil {
		s.userError(w, err)
		return true
	}
	s.setUserCookie(w, s.loginCookieName(), token, 43200)
	// All authenticated identities can land on the same control-center shell;
	// the API applies platform/exam-admin capabilities per resource.
	if destination == "" {
		destination = "/admin/"
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
	return true
}
func (s *Service) userAuthRoute(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/auth/login":
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
		} else {
			s.beginUserLogin(w, r)
		}
		return true
	case "/auth/me":
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return true
		}
		u, token, err := s.currentUser(r)
		if err != nil {
			s.writeJSON(w, 401, map[string]string{"error": "login_required"})
		} else {
			examAdmin := false
			if s.ExamStore != nil {
				examAdmin, _ = s.ExamStore.HasExamAdmin(r.Context(), u.ID)
			}
			s.writeJSON(w, 200, map[string]any{"user": u, "csrf_token": s.csrfToken(token), "capabilities": map[string]bool{"platform_admin": u.PlatformAdmin, "exam_admin": examAdmin}})
		}
		return true
	case "/auth/logout":
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return true
		}
		_, token, err := s.currentUser(r)
		if err != nil {
			s.writeJSON(w, 401, map[string]string{"error": "login_required"})
			return true
		}
		if !s.validCSRF(r, token) {
			s.writeJSON(w, 403, map[string]string{"error": "csrf_denied"})
			return true
		}
		if _, err = s.ExamStore.db.ExecContext(r.Context(), `DELETE FROM byod_user_sessions WHERE token_hash=$1`, digestToken(token)); err != nil {
			s.userError(w, err)
			return true
		}
		s.setUserCookie(w, s.loginCookieName(), "", -1)
		w.WriteHeader(204)
		return true
	case "/dev/user-authorize":
		if !s.DevAuth || r.Method != http.MethodGet {
			w.WriteHeader(404)
			return true
		}
		http.Redirect(w, r, "/oidc/callback?state="+url.QueryEscape(r.URL.Query().Get("state"))+"&code=dev-student-42", 303)
		return true
	}
	return false
}
func (s *Service) userError(w http.ResponseWriter, err error) {
	status, message := 500, "user_operation_failed"
	switch {
	case errors.Is(err, sql.ErrNoRows):
		status, message = 404, "user_not_found"
	case errors.Is(err, ErrUserDisabled):
		status, message = 403, "user_disabled"
	case errors.Is(err, ErrIdentityConflict):
		status, message = 409, "identity_conflict"
	case errors.Is(err, ErrLastAdmin):
		status, message = 409, "last_platform_admin_required"
	case err.Error() == "invalid_email" || err.Error() == "invalid_user":
		status, message = 400, err.Error()
	case err.Error() == "email_exists":
		status, message = 409, "email_exists"
	}
	s.writeJSON(w, status, map[string]string{"error": message})
}
func decodeUserInput(r *http.Request, v any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	return decoder.Decode(v)
}
func (s *Service) globalUserAPI(w http.ResponseWriter, r *http.Request, actor User) bool {
	if r.URL.Path == "/admin/api/users" {
		switch r.Method {
		case http.MethodGet:
			limit, offset := 50, 0
			if n, e := strconv.Atoi(r.URL.Query().Get("limit")); e == nil && n > 0 && n <= 100 {
				limit = n
			}
			if n, e := strconv.Atoi(r.URL.Query().Get("offset")); e == nil && n >= 0 {
				offset = n
			}
			users, err := s.ExamStore.ListUsers(r.Context(), s.identityIssuer(), r.URL.Query().Get("q"), limit, offset)
			if err != nil {
				s.userError(w, err)
			} else {
				s.writeJSON(w, 200, users)
			}
		case http.MethodPost:
			var input struct {
				Email         string `json:"email"`
				DisplayName   string `json:"display_name"`
				PlatformAdmin bool   `json:"platform_admin"`
				// Role is accepted for one release for old clients; it is not
				// persisted and does not make exam memberships exclusive.
				Role string `json:"role"`
			}
			if decodeUserInput(r, &input) != nil {
				s.writeJSON(w, 400, map[string]string{"error": "invalid_user"})
				return true
			}
			if input.Role == "admin" {
				input.PlatformAdmin = true
			}
			u, err := s.ExamStore.InviteUser(r.Context(), s.identityIssuer(), input.Email, input.DisplayName, input.PlatformAdmin, actor.ID)
			if err != nil {
				s.userError(w, err)
			} else {
				s.writeJSON(w, 201, u)
			}
		default:
			w.WriteHeader(405)
		}
		return true
	}
	if r.URL.Path == "/admin/api/user-audit" {
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return true
		}
		events, err := s.ExamStore.UserAudit(r.Context(), 200)
		if err != nil {
			s.userError(w, err)
		} else {
			s.writeJSON(w, 200, events)
		}
		return true
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 4 && parts[2] == "users" {
		u, err := s.ExamStore.GetUser(r.Context(), parts[3])
		if err != nil {
			s.userError(w, err)
			return true
		}
		if u.Issuer != s.identityIssuer() {
			w.WriteHeader(404)
			return true
		}
		switch r.Method {
		case http.MethodGet:
			s.writeJSON(w, 200, u)
		case http.MethodPatch:
			var input struct {
				PlatformAdmin *bool   `json:"platform_admin"`
				Role          *string `json:"role"`
				DisplayName   *string `json:"display_name"`
				Enabled       *bool   `json:"enabled"`
			}
			if decodeUserInput(r, &input) != nil {
				s.writeJSON(w, 400, map[string]string{"error": "invalid_user"})
				return true
			}
			platformAdmin := u.PlatformAdmin
			if input.PlatformAdmin != nil {
				platformAdmin = *input.PlatformAdmin
			}
			if input.Role != nil {
				platformAdmin = *input.Role == "admin"
			}
			if input.DisplayName != nil {
				u.DisplayName = *input.DisplayName
			}
			if input.Enabled != nil {
				u.Enabled = *input.Enabled
			}
			u, err = s.ExamStore.UpdateUser(r.Context(), u.ID, platformAdmin, u.DisplayName, u.Enabled, actor.ID)
			if err != nil {
				s.userError(w, err)
			} else {
				s.writeJSON(w, 200, u)
			}
		default:
			w.WriteHeader(405)
		}
		return true
	}
	if len(parts) >= 5 && parts[2] == "exams" && parts[4] == "participants" {
		if len(parts) == 5 && r.Method == http.MethodGet {
			items, err := s.ExamStore.ListParticipants(r.Context(), parts[3])
			if err != nil {
				s.userError(w, err)
			} else {
				s.writeJSON(w, 200, items)
			}
			return true
		}
		if len(parts) != 6 {
			w.WriteHeader(405)
			return true
		}
		u, err := s.ExamStore.GetUser(r.Context(), parts[5])
		if err != nil {
			s.userError(w, err)
			return true
		}
		if u.Issuer != s.identityIssuer() {
			w.WriteHeader(404)
			return true
		}
		switch r.Method {
		case http.MethodPut:
			var input struct {
				Enabled bool `json:"enabled"`
			}
			input.Enabled = true
			if decodeUserInput(r, &input) != nil {
				s.writeJSON(w, 400, map[string]string{"error": "invalid_participant"})
				return true
			}
			err = s.ExamStore.SetParticipant(r.Context(), parts[3], u.ID, input.Enabled, actor.ID)
		case http.MethodDelete:
			err = s.ExamStore.RemoveParticipant(r.Context(), parts[3], u.ID, actor.ID)
		default:
			w.WriteHeader(405)
			return true
		}
		if err != nil {
			s.userError(w, err)
		} else {
			w.WriteHeader(204)
		}
		return true
	}
	return false
}
