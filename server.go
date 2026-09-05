package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

//go:embed openapi.yaml
var openAPISpec []byte

type Session struct {
	ID               string
	AttemptID        string
	BrowserSessionID string
	BrowserToken     string
	ExamID           string
	Subject          string
	State            string
	CreatedAt        int64
	LastSeenAt       int64
	ReturnURI        string
	CodeVerifier     string
	ViolationCount   int
	LastViolation    string
}

// Domain entities. Session is retained as the wire-compatible aggregate for
// now; these types define the storage boundary for the next persistence layer.
type Exam struct {
	ID            string
	Origin        string
	PolicyVersion int
}

type ExamAttempt struct {
	ID        string
	ExamID    string
	Subject   string
	State     string
	CreatedAt int64
	StartedAt int64
	EndedAt   int64
}

type BrowserSession struct {
	ID         string
	AttemptID  string
	CreatedAt  int64
	LastSeenAt int64
	RevokedAt  int64
}

type ExamEvent struct {
	ID               string `json:"id"`
	AttemptID        string `json:"attempt_id"`
	BrowserSessionID string `json:"browser_session_id"`
	Type             string `json:"type"`
	Severity         string `json:"severity"`
	Details          string `json:"details,omitempty"`
	OccurredAt       int64  `json:"occurred_at"`
}

type Service struct {
	ExamOrigin string
	Upstream   *url.URL
	// ExamUpstreams optionally overrides the default upstream per exam ID. The
	// map is operator-provided configuration, never taken from a browser
	// request, so exam routing cannot be turned into an open proxy.
	ExamUpstreams   map[string]*url.URL
	ExamStore       *PostgresStore
	PolicySecret    []byte
	OIDCAuthorize   string
	OIDC            *OIDCAuthenticator
	DevAuth         bool
	AdminToken      string
	PolicyOverrides map[string]map[string]any
	mu              sync.RWMutex
	sessions        map[string]*Session
	exams           map[string]*Exam
	events          map[string][]ExamEvent
}

func (s *Service) upstreamForExam(ctx context.Context, examID string) (*url.URL, error) {
	if s.ExamStore != nil {
		if upstream, ok, err := s.ExamStore.Upstream(ctx, examID); err != nil {
			return nil, err
		} else if ok {
			return upstream, nil
		}
	}
	if configured, ok := s.ExamUpstreams[examID]; ok {
		return configured, nil
	}
	return s.Upstream, nil
}

func NewService(examOrigin, upstream string, secret []byte) (*Service, error) {
	origin, err := url.Parse(examOrigin)
	if err != nil || origin.Host == "" || (origin.Scheme != "http" && origin.Scheme != "https") {
		return nil, errors.New("exam origin must be an absolute HTTP(S) URL")
	}
	base, err := url.Parse(upstream)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, errors.New("upstream must be an absolute HTTP(S) URL")
	}
	return &Service{ExamOrigin: strings.TrimRight(examOrigin, "/"), Upstream: base,
		ExamUpstreams: make(map[string]*url.URL),
		PolicySecret:  secret, OIDCAuthorize: "https://idp.example/authorize",
		sessions: make(map[string]*Session), exams: make(map[string]*Exam), events: make(map[string][]ExamEvent)}, nil
}

// ParseExamUpstreams parses an operator-supplied JSON object mapping exam IDs
// to absolute HTTP(S) base URLs, for example:
// {"course-101":"https://cs101.gbu.edu.cn"}.
func ParseExamUpstreams(data []byte) (map[string]*url.URL, error) {
	var configured map[string]string
	if err := json.Unmarshal(data, &configured); err != nil {
		return nil, err
	}
	result := make(map[string]*url.URL, len(configured))
	for examID, rawURL := range configured {
		if !validExamID(examID) {
			return nil, fmt.Errorf("invalid exam upstream id %q", examID)
		}
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Fragment != "" {
			return nil, fmt.Errorf("exam upstream for %q must be an absolute HTTP(S) URL without credentials or fragment", examID)
		}
		parsed.Path = strings.TrimRight(parsed.Path, "/")
		result[examID] = parsed
	}
	return result, nil
}

func canonicalJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// ParsePolicyOverrides accepts either {"exam-id": {document...}} or a
// single document containing an "exam_id" field. The caller signs the
// resulting canonical document when serving configuration.
func ParsePolicyOverrides(data []byte) (map[string]map[string]any, error) {
	var single map[string]any
	if err := json.Unmarshal(data, &single); err != nil {
		return nil, err
	}
	if id, ok := single["exam_id"].(string); ok && id != "" {
		return map[string]map[string]any{id: single}, nil
	}
	var keyed map[string]map[string]any
	if err := json.Unmarshal(data, &keyed); err != nil {
		return nil, err
	}
	return keyed, nil
}

func (s *Service) sign(document map[string]any) string {
	digest := hmac.New(sha256.New, s.PolicySecret)
	digest.Write(canonicalJSON(document))
	return base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}

func (s *Service) policy(examID string) map[string]any {
	document := map[string]any{
		"version": 1, "exam_id": examID,
		"allowed_origins": []string{s.ExamOrigin},
		"allowed_paths":   []string{"/" + examID + "/**"},
		"browser": map[string]any{
			// The fields mirror the first SEB-style baseline. Chromium's native
			// enforcement consumes these values; keeping them in the signed
			// document makes the server the single policy source of truth.
			"allow_background":              false,
			"allow_new_tabs":                false,
			"allow_new_windows":             false,
			"allow_devtools":                false,
			"allow_print":                   false,
			"allow_view_source":             false,
			"allow_save_page":               false,
			"allow_downloads":               false,
			"allow_extensions":              false,
			"allow_incognito":               false,
			"allow_fullscreen":              true,
			"allow_clipboard_read":          false,
			"allow_clipboard_write":         false,
			"allow_screen_capture":          false,
			"allow_navigation_outside_exam": false,
			"kiosk_mode":                    true,
			"exit_requires_unlock":          true,
		},
		"navigation": map[string]any{
			"allowed_origins": []string{s.ExamOrigin},
			"blocked_schemes": []string{"file", "javascript", "data", "devtools"},
		},
		"violations": map[string]any{
			"on_background":         "suspend",
			"max_background_events": 0,
			"on_new_tab":            "block",
			"on_devtools":           "block",
		},
		"session": map[string]any{"heartbeat_seconds": 15, "max_idle_seconds": 45},
	}
	if override, ok := s.PolicyOverrides[examID]; ok {
		// The override replaces only the signed document; the server still
		// forces the exam identity and origin to prevent cross-exam policies.
		for key, value := range override {
			mergePolicyValue(document, key, value)
		}
	}
	document["exam_id"] = examID
	document["allowed_origins"] = []string{s.ExamOrigin}
	return map[string]any{"key_id": "dev-hmac-1", "alg": "HS256", "document": document,
		"signature": s.sign(document)}
}

func mergePolicyValue(document map[string]any, key string, value any) {
	existing, existingOK := document[key].(map[string]any)
	incoming, incomingOK := value.(map[string]any)
	if existingOK && incomingOK {
		for childKey, childValue := range incoming {
			mergePolicyValue(existing, childKey, childValue)
		}
		return
	}
	document[key] = value
}

func (s *Service) sessionLimits(examID string) (heartbeat, maxIdle int64) {
	heartbeat, maxIdle = 15, 45
	document, _ := s.policy(examID)["document"].(map[string]any)
	session, _ := document["session"].(map[string]any)
	if value, ok := policyInt(session["heartbeat_seconds"]); ok && value >= 5 && value <= 300 {
		heartbeat = value
	}
	if value, ok := policyInt(session["max_idle_seconds"]); ok && value >= heartbeat && value <= 3600 {
		maxIdle = value
	}
	return heartbeat, maxIdle
}

func policyInt(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), typed == float64(int64(typed))
	default:
		return 0, false
	}
}

func (s *Service) pathAllowed(examID, requestPath string) bool {
	document, _ := s.policy(examID)["document"].(map[string]any)
	paths, _ := document["allowed_paths"].([]string)
	if configured, ok := document["allowed_paths"].([]any); ok {
		paths = nil
		for _, value := range configured {
			if pattern, ok := value.(string); ok {
				paths = append(paths, pattern)
			}
		}
	}
	if navigation, ok := document["navigation"].(map[string]any); ok {
		if configured, ok := navigation["allowed_paths"].([]string); ok {
			paths = configured
		}
		if configured, ok := navigation["allowed_paths"].([]any); ok {
			paths = paths[:0]
			for _, value := range configured {
				if pattern, ok := value.(string); ok {
					paths = append(paths, pattern)
				}
			}
		}
	}
	for _, pattern := range paths {
		if strings.HasSuffix(pattern, "/**") {
			base := strings.TrimSuffix(pattern, "/**")
			if requestPath == base || strings.HasPrefix(requestPath, base+"/") {
				return true
			}
		}
		if pattern == requestPath {
			return true
		}
	}
	return false
}

func (s *Service) configuration(examID string) map[string]any {
	authorizeEndpoint := s.OIDCAuthorize
	clientID := "byod-browser"
	if s.DevAuth {
		authorizeEndpoint = s.ExamOrigin + "/dev/authorize"
	} else if s.OIDC != nil {
		authorizeEndpoint = s.OIDC.OAuth2.Endpoint.AuthURL
		clientID = s.OIDC.ClientID
	}
	return map[string]any{"version": 1,
		"exam": map[string]any{"id": examID, "origin": s.ExamOrigin,
			"proxy_origin": s.ExamOrigin, "unlock_path": "/" + examID + "/end"},
		"oidc": map[string]any{"authorization_endpoint": authorizeEndpoint,
			"callback_endpoint": "/oidc/callback", "client_id": clientID,
			"response_type": "code"}, "policy": s.policy(examID)}
}

func (s *Service) authorizationURL(session *Session) string {
	if s.DevAuth {
		query := url.Values{"client_id": {"byod-browser"}, "redirect_uri": {s.ExamOrigin + "/oidc/callback"}, "response_type": {"code"}, "state": {session.ID}}
		return s.ExamOrigin + "/dev/authorize?" + query.Encode()
	}
	if s.OIDC != nil {
		return s.OIDC.authorizationURL(session.ID, session.CodeVerifier)
	}
	query := url.Values{"client_id": {"byod-browser"}, "redirect_uri": {s.ExamOrigin + "/oidc/callback"}, "response_type": {"code"}, "state": {session.ID}}
	return s.OIDCAuthorize + "?" + query.Encode()
}

func tokenFromRequest(r *http.Request) string {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	}
	if c, err := r.Cookie("byod_session"); err == nil {
		return c.Value
	}
	return ""
}

func validExamID(id string) bool {
	if id == "" || id == "." || id == ".." || len(id) > 128 {
		return false
	}
	for _, char := range id {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '-' && char != '_' && char != '.' {
			return false
		}
	}
	return true
}

func (s *Service) findByToken(token string) *Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, session := range s.sessions {
		if hmac.Equal([]byte(session.BrowserToken), []byte(token)) {
			return session
		}
	}
	return nil
}

func (s *Service) authorize(token, id string) *Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session := s.sessions[id]
	if session != nil && hmac.Equal([]byte(session.BrowserToken), []byte(token)) {
		return session
	}
	return nil
}

func (s *Service) appendEvent(session *Session, typ, severity, details string) ExamEvent {
	event := ExamEvent{ID: randomToken(12), AttemptID: session.AttemptID, BrowserSessionID: session.BrowserSessionID,
		Type: typ, Severity: severity, Details: details, OccurredAt: time.Now().Unix()}
	s.events[session.ID] = append(s.events[session.ID], event)
	return event
}

func (s *Service) enforceIdleTimeout(session *Session) bool {
	if session == nil {
		return false
	}
	_, maxIdle := s.sessionLimits(session.ExamID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if session.State == "active" && time.Now().Unix()-session.LastSeenAt > maxIdle {
		session.State = "suspended"
		session.ViolationCount++
		session.LastViolation = "heartbeat_timeout"
		s.appendEvent(session, "heartbeat_timeout", "critical", "")
	}
	return session.State == "active" || session.State == "authenticated"
}

func (s *Service) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	body := canonicalJSON(value)
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/admin/api/") {
		s.adminAPI(w, r)
		return
	}
	// grips://exam is a trusted Chromium WebUI origin, but it is still
	// cross-origin from the HTTPS exam endpoint.  Explicit CORS headers are
	// therefore required for the browser-side session bootstrap.
	if r.Header.Get("Origin") == "grips://exam" {
		w.Header().Set("Access-Control-Allow-Origin", "grips://exam")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-BYOD-Session")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Add("Vary", "Origin")
	}
	if r.Method == http.MethodOptions {
		if r.Header.Get("Origin") != "grips://exam" {
			s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "cors_origin_denied"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.get(w, r)
	case http.MethodPost:
		s.post(w, r)
	case http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions:
		s.proxy(w, r)
	default:
		s.writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	}
}

func examIDFromWellKnown(requestPath string) string {
	const marker = "/.well-known/byod-configuration"
	prefix := strings.Trim(requestPath[:len(requestPath)-len(marker)], "/")
	prefix = strings.TrimPrefix(prefix, "exam/")
	if !validExamID(prefix) || strings.Contains(prefix, "/") {
		return ""
	}
	return prefix
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/openapi.yaml" {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(openAPISpec)
		return
	}
	if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
		s.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "byod-server", "oidc": s.OIDC != nil || s.DevAuth})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/.well-known/byod-configuration") {
		if examID := examIDFromWellKnown(r.URL.Path); examID != "" {
			s.writeJSON(w, http.StatusOK, s.configuration(examID))
			return
		}
	}
	if r.URL.Path == "/oidc/callback" {
		state, code := r.URL.Query().Get("state"), r.URL.Query().Get("code")
		s.mu.Lock()
		session := s.sessions[state]
		validState := code != "" && session != nil && session.State == "pending" && time.Since(time.Unix(session.CreatedAt, 0)) < 15*time.Minute
		if validState {
			// Reserve the one-time state before doing the network token exchange.
			// A second callback cannot race the first one into authentication.
			session.State = "authenticating"
		}
		var codeVerifier string
		if validState {
			codeVerifier = session.CodeVerifier
		}
		s.mu.Unlock()
		if !validState {
			s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_oidc_callback"})
			return
		}
		var subject string
		if s.OIDC != nil && !s.DevAuth {
			var err error
			subject, err = s.OIDC.exchange(r.Context(), code, codeVerifier)
			if err != nil {
				s.mu.Lock()
				if current := s.sessions[state]; current != nil && current.State == "authenticating" {
					current.State = "pending"
				}
				s.mu.Unlock()
				s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "oidc_exchange_failed"})
				return
			}
		} else {
			subject = "oidc:" + code
		}
		s.mu.Lock()
		if current := s.sessions[state]; current == nil || current.State != "authenticating" {
			s.mu.Unlock()
			s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_oidc_callback"})
			return
		} else {
			session = current
		}
		session.Subject = subject
		session.State = "authenticated"
		s.appendEvent(session, "authentication_succeeded", "info", "")
		returnURI := session.ReturnURI
		examID := session.ExamID
		browserToken := session.BrowserToken
		s.mu.Unlock()
		if returnURI != "" {
			redirect, err := url.Parse(returnURI)
			if err != nil || redirect.Scheme != "grips" || redirect.Host != "exam" {
				s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_return_uri"})
				return
			}
			target := redirect.Query().Get("target")
			if target == "" {
				s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing_return_target"})
				return
			}
			targetURL, targetErr := url.Parse(target)
			examOrigin, _ := url.Parse(s.ExamOrigin)
			validTargetPath := targetErr == nil && targetURL != nil &&
				(targetURL.Path == "/"+examID || strings.HasPrefix(targetURL.Path, "/"+examID+"/"))
			if targetErr != nil || examOrigin == nil || targetURL == nil || targetURL.Scheme != examOrigin.Scheme ||
				targetURL.Host != examOrigin.Host || !validTargetPath {
				s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_return_target"})
				return
			}
			callback := &url.URL{Path: "/byod/complete"}
			query := callback.Query()
			query.Set("session_id", session.ID)
			query.Set("target", target)
			callback.RawQuery = query.Encode()
			http.SetCookie(w, &http.Cookie{Name: "byod_session", Value: browserToken, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.ExamOrigin, "https://"), SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, callback.String(), http.StatusSeeOther)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]string{"session_id": session.ID, "state": session.State})
		return
	}
	if r.URL.Path == "/dev/authorize" && s.DevAuth {
		state := r.URL.Query().Get("state")
		if state == "" {
			s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing_state"})
			return
		}
		s.mu.RLock()
		_, ok := s.sessions[state]
		s.mu.RUnlock()
		if !ok {
			s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_state"})
			return
		}
		callback := &url.URL{Path: "/oidc/callback"}
		query := callback.Query()
		query.Set("state", state)
		query.Set("code", "dev-student-42")
		callback.RawQuery = query.Encode()
		http.Redirect(w, r, callback.String(), http.StatusSeeOther)
		return
	}
	if r.URL.Path == "/byod/complete" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, "<!doctype html><title>BYOD sign-in complete</title><p>Returning to the BYOD exam tab…</p>")
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 2 && parts[1] == "end" {
		session := s.findByToken(tokenFromRequest(r))
		if session == nil || session.ExamID != parts[0] {
			s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "active_session_required"})
			return
		}
		s.end(session)
		if strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.Redirect(w, r, "grips://exam/?ended=1", http.StatusSeeOther)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]string{"session_id": session.ID, "state": "ended"})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/sessions/") && len(parts) == 3 {
		session := s.authorize(tokenFromRequest(r), parts[2])
		if session == nil {
			s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"session_id": session.ID, "exam_id": session.ExamID,
			"state": session.State, "subject": session.Subject != "", "violation_count": session.ViolationCount,
			"last_violation": session.LastViolation, "last_seen_at": session.LastSeenAt})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/sessions/") && len(parts) == 4 && parts[3] == "events" {
		session := s.authorize(tokenFromRequest(r), parts[2])
		if session == nil {
			s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		s.mu.RLock()
		events := append([]ExamEvent(nil), s.events[session.ID]...)
		s.mu.RUnlock()
		s.writeJSON(w, http.StatusOK, map[string]any{"attempt_id": session.AttemptID, "events": events})
		return
	}
	if len(parts) >= 1 && validExamID(parts[0]) && !strings.HasPrefix(r.URL.Path, "/v1/") && !strings.HasPrefix(r.URL.Path, "/oidc/") {
		s.proxy(w, r)
		return
	}
	s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
}

func (s *Service) adminAPI(w http.ResponseWriter, r *http.Request) {
	if s.AdminToken == "" || r.Header.Get("X-Admin-Token") != s.AdminToken {
		s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "admin_auth_required"})
		return
	}
	if s.ExamStore == nil {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "database_required"})
		return
	}
	if r.URL.Path == "/admin/api/exams" && r.Method == http.MethodGet {
		exams, err := s.ExamStore.ListExams(r.Context())
		if err != nil {
			s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database_error"})
			return
		}
		s.writeJSON(w, http.StatusOK, exams)
		return
	}
	if r.URL.Path == "/admin/api/exams" && r.Method == http.MethodPost {
		var input struct {
			ID      string `json:"id"`
			BaseURL string `json:"base_url"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&input) != nil || !validExamID(input.ID) {
			s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_exam"})
			return
		}
		if err := s.ExamStore.UpsertExam(r.Context(), input.ID, input.BaseURL); err != nil {
			s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_exam"})
			return
		}
		s.writeJSON(w, http.StatusCreated, map[string]any{"id": input.ID, "base_url": strings.TrimRight(input.BaseURL, "/"), "state": "draft"})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 4 && parts[0] == "admin" && parts[1] == "api" && parts[2] == "exams" {
		exams, err := s.ExamStore.ListExams(r.Context())
		if err != nil {
			s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database_error"})
			return
		}
		var found *StoredExam
		for i := range exams {
			if exams[i].ID == parts[3] {
				found = &exams[i]
				break
			}
		}
		if r.Method == http.MethodGet {
			if found == nil {
				s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "exam_not_found"})
			} else {
				s.writeJSON(w, http.StatusOK, found)
			}
			return
		}
		if r.Method == http.MethodDelete {
			if err := s.ExamStore.DeleteExam(r.Context(), parts[3]); err != nil {
				s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database_error"})
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodPatch {
			var input struct {
				BaseURL string `json:"base_url"`
			}
			if json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&input) != nil || input.BaseURL == "" {
				s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_exam"})
				return
			}
			if err := s.ExamStore.UpsertExam(r.Context(), parts[3], input.BaseURL); err != nil {
				s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_exam"})
				return
			}
			s.writeJSON(w, http.StatusOK, map[string]any{"id": parts[3], "base_url": strings.TrimRight(input.BaseURL, "/"), "state": "draft"})
			return
		}
	}
	if len(parts) == 5 && parts[0] == "admin" && parts[1] == "api" && parts[2] == "exams" && parts[4] == "students" && r.Method == http.MethodGet {
		students, err := s.ExamStore.ListStudents(r.Context(), parts[3])
		if err != nil {
			s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database_error"})
			return
		}
		s.writeJSON(w, http.StatusOK, students)
		return
	}
	if len(parts) == 6 && parts[0] == "admin" && parts[1] == "api" && parts[2] == "exams" && parts[4] == "students" {
		var err error
		switch r.Method {
		case http.MethodPut:
			err = s.ExamStore.SetStudent(r.Context(), parts[3], parts[5], true)
		case http.MethodDelete:
			err = s.ExamStore.RemoveStudent(r.Context(), parts[3], parts[5])
		default:
			s.writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if err != nil {
			s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_student"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) == 5 && parts[0] == "admin" && parts[1] == "api" && parts[2] == "exams" && parts[4] == "sessions" && r.Method == http.MethodGet {
		s.mu.RLock()
		result := make([]map[string]any, 0)
		for _, session := range s.sessions {
			if session.ExamID == parts[3] {
				result = append(result, map[string]any{"id": session.ID, "exam_id": session.ExamID, "subject": session.Subject, "state": session.State, "created_at": time.Unix(session.CreatedAt, 0).UTC(), "last_seen_at": time.Unix(session.LastSeenAt, 0).UTC(), "violation_count": session.ViolationCount})
			}
		}
		s.mu.RUnlock()
		s.writeJSON(w, http.StatusOK, result)
		return
	}
	if len(parts) == 5 && parts[0] == "admin" && parts[1] == "api" && parts[2] == "sessions" && parts[4] == "events" && r.Method == http.MethodGet {
		s.mu.RLock()
		events := append([]ExamEvent(nil), s.events[parts[3]]...)
		s.mu.RUnlock()
		s.writeJSON(w, http.StatusOK, events)
		return
	}
	s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
}

func (s *Service) post(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/sessions" {
		var input struct {
			ExamID    string `json:"exam_id"`
			ReturnURI string `json:"return_uri"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&input); err != nil || !validExamID(input.ExamID) {
			s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_exam_id"})
			return
		}
		if input.ReturnURI != "" {
			returnURL, err := url.Parse(input.ReturnURI)
			if err != nil || returnURL.Scheme != "grips" || returnURL.Host != "exam" {
				s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_return_uri"})
				return
			}
		}
		now := time.Now().Unix()
		session := &Session{ID: randomToken(18), AttemptID: randomToken(18), BrowserSessionID: randomToken(18), BrowserToken: randomToken(32), ExamID: input.ExamID, State: "pending", CreatedAt: now, LastSeenAt: now, ReturnURI: input.ReturnURI}
		s.mu.Lock()
		if _, exists := s.exams[input.ExamID]; !exists {
			s.exams[input.ExamID] = &Exam{ID: input.ExamID, Origin: s.ExamOrigin, PolicyVersion: 1}
		}
		s.sessions[session.ID] = session
		s.appendEvent(session, "attempt_created", "info", "")
		s.mu.Unlock()
		session.CodeVerifier = pkceVerifier()
		http.SetCookie(w, &http.Cookie{Name: "byod_session", Value: session.BrowserToken, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.ExamOrigin, "https://"), SameSite: http.SameSiteStrictMode})
		s.writeJSON(w, http.StatusCreated, map[string]string{"session_id": session.ID, "attempt_id": session.AttemptID, "browser_session_id": session.BrowserSessionID, "browser_token": session.BrowserToken,
			"authorization_url": s.authorizationURL(session), "state": session.State})
		// Headers must be set before writeJSON writes the status; this cookie is
		// also useful when the student follows the public /<exam>/end link.
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/sessions/") {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 4 {
			s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		session := s.authorize(tokenFromRequest(r), parts[2])
		if session == nil {
			s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if parts[3] == "start" {
			s.mu.Lock()
			if session.State != "authenticated" {
				state := session.State
				s.mu.Unlock()
				s.writeJSON(w, http.StatusConflict, map[string]string{"error": "authentication_required", "state": state})
				return
			}
			session.State = "active"
			session.LastSeenAt = time.Now().Unix()
			s.appendEvent(session, "exam_started", "info", "")
			s.mu.Unlock()
			s.writeJSON(w, http.StatusOK, map[string]string{"session_id": session.ID, "state": session.State, "proxy_base": "/" + session.ExamID + "/"})
			return
		}
		if parts[3] == "end" {
			s.end(session)
			s.writeJSON(w, http.StatusOK, map[string]string{"session_id": session.ID, "state": "ended"})
			return
		}
		if parts[3] == "violations" {
			var input struct {
				Type    string `json:"type"`
				Details string `json:"details"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&input); err != nil || input.Type == "" || len(input.Type) > 64 {
				s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_violation"})
				return
			}
			s.mu.Lock()
			session.ViolationCount++
			session.LastViolation = input.Type
			if input.Type == "background" || input.Type == "devtools" {
				session.State = "suspended"
			}
			event := s.appendEvent(session, input.Type, "critical", input.Details)
			count, state := session.ViolationCount, session.State
			s.mu.Unlock()
			s.writeJSON(w, http.StatusOK, map[string]any{"session_id": session.ID, "state": state, "violation_count": count, "event_id": event.ID})
			return
		}
		if parts[3] == "heartbeat" {
			s.mu.Lock()
			now := time.Now().Unix()
			heartbeat, maxIdle := s.sessionLimits(session.ExamID)
			if session.State == "active" && now-session.LastSeenAt > maxIdle {
				session.State = "suspended"
				session.ViolationCount++
				session.LastViolation = "heartbeat_timeout"
				s.appendEvent(session, "heartbeat_timeout", "critical", "")
			} else if session.State == "active" {
				session.LastSeenAt = now
			}
			state := session.State
			lastSeen := session.LastSeenAt
			s.mu.Unlock()
			s.writeJSON(w, http.StatusOK, map[string]any{"session_id": session.ID, "state": state, "last_seen_at": lastSeen, "heartbeat_seconds": heartbeat, "max_idle_seconds": maxIdle})
			return
		}
	}
	if !strings.HasPrefix(r.URL.Path, "/v1/") {
		s.proxy(w, r)
		return
	}
	s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
}

func (s *Service) end(session *Session) {
	s.mu.Lock()
	session.State = "ended"
	session.Subject = ""
	s.appendEvent(session, "exam_ended", "info", "")
	s.mu.Unlock()
}

func (s *Service) proxy(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 1 || parts[0] == "" {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	examID, resource := parts[0], strings.Join(parts[1:], "/")
	if examID == "exam" {
		if len(parts) < 4 || parts[2] != "proxy" {
			s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		examID, resource = parts[1], strings.Join(parts[3:], "/")
	}
	sessionID := r.Header.Get("X-BYOD-Session")
	session := s.authorize(tokenFromRequest(r), sessionID)
	if sessionID == "" {
		session = s.findByToken(tokenFromRequest(r))
	}
	if session == nil || session.ExamID != examID || session.State != "active" {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "active_session_required"})
		return
	}
	if !s.enforceIdleTimeout(session) {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "session_suspended"})
		return
	}
	requestPath := path.Clean("/" + examID + "/" + resource)
	if requestPath != "/"+examID && !strings.HasPrefix(requestPath, "/"+examID+"/") {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "path_not_allowed"})
		return
	}
	if !s.pathAllowed(examID, requestPath) {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "path_not_allowed"})
		return
	}
	s.mu.Lock()
	session.LastSeenAt = time.Now().Unix()
	s.mu.Unlock()
	upstream, upstreamErr := s.upstreamForExam(r.Context(), examID)
	if upstreamErr != nil {
		s.writeJSON(w, http.StatusBadGateway, map[string]string{"error": "exam_configuration_unavailable"})
		return
	}
	target := *upstream
	cleanResource := strings.TrimPrefix(requestPath, "/"+examID)
	target.Path = strings.TrimRight(upstream.Path, "/") + cleanResource
	target.RawQuery = r.URL.RawQuery
	request, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		s.writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream_error"})
		return
	}
	for key, values := range r.Header {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	// Never forward the browser bearer token to the exam origin. The middleware
	// is the only component that may mint identity headers for the upstream.
	request.Header.Del("Authorization")
	request.Header.Set("Cookie", stripCookie(request.Header.Get("Cookie"), "byod_session"))
	request.Header.Set("X-BYOD-Subject", session.Subject)
	request.Header.Set("X-BYOD-Session", session.ID)
	request.Header.Set("X-Forwarded-Host", r.Host)
	request.Header.Set("X-Forwarded-Proto", requestScheme(r))
	response, err := (&http.Client{Timeout: 10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}).Do(request)
	if err != nil {
		s.writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream_error"})
		return
	}
	defer response.Body.Close()
	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, response.Body)
	}
}

func requestScheme(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
		return strings.Split(forwarded, ",")[0]
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func stripCookie(header, name string) string {
	if header == "" {
		return ""
	}
	kept := make([]string, 0)
	for _, item := range strings.Split(header, ";") {
		item = strings.TrimSpace(item)
		key, _, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), name) {
			continue
		}
		if item != "" {
			kept = append(kept, item)
		}
	}
	return strings.Join(kept, "; ")
}

func copyResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		if isHopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func isHopByHopHeader(key string) bool {
	switch strings.ToLower(key) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func randomToken(bytes int) string {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer)
}
