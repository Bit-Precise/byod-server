package byodserver

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	admincontract "byod-server/internal/admin"
)

//go:embed openapi.yaml
var openAPISpec []byte

// Keep the checked-in generated contract in the build graph. If the OpenAPI
// document is changed, go generate updates this value and CI's diff check
// forces the generated artifact to be committed alongside the handler change.
var _ = admincontract.OpenAPISpecSHA256

// The administrator UI is built from admin-ui and embedded into the same
// binary so the Helm workload exposes one self-contained service.
//
//go:embed admin-ui/dist
var adminUIDist embed.FS

// The student exam shell is served from the BYOD server origin.  It is kept
// out of Chromium so the UI and API flow can be updated by deploying the
// server image, without rebuilding or reinstalling the browser.
//
//go:embed exam-ui/dist
var examUIDist embed.FS

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

// Domain entities used by the browser protocol and durable store.
type Exam struct {
	ID            string
	ExamCode      string
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

// browserLoginState is a short-lived, one-time OIDC transaction used by the
// Settings "Sign in with Connect" entry point. It deliberately does not mint
// an exam session: its only purpose is to complete a real authorization-code
// flow in the normal browser profile so a later exam authorization can reuse
// the Connect SSO session.
type browserLoginState struct {
	CodeVerifier string
	CreatedAt    time.Time
}

type ExamEvent struct {
	ID               string `json:"id"`
	SessionID        string `json:"session_id"`
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
	// TunnelEndpoint is the public host:port exposed by an L4 load balancer.
	// It is intentionally separate from the HTTP control-plane origin.
	TunnelEndpoint string
	// TunnelPrivateEndpoint is an optional endpoint for clients in the
	// configured private address ranges. The browser still receives one
	// endpoint, selected when it fetches the exam configuration.
	TunnelPrivateEndpoint string
	TunnelPrivateCIDRs    []*net.IPNet
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
	AdminEmails     map[string]bool
	PolicyOverrides map[string]map[string]any
	mu              sync.RWMutex
	sessions        map[string]*Session
	exams           map[string]*Exam
	events          map[string][]ExamEvent
	tunnelTickets   map[string]*tunnelTicket
	browserLogins   map[string]*browserLoginState
	completions     map[string]time.Time
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

// parseUpstreamURL validates an operator-supplied upstream.  The URL's
// authority selects the transparent TLS dial target; its path and query are
// the initial page that the browser opens after the exam starts.
func parseUpstreamURL(raw string, requireHTTPS bool) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.Opaque != "" ||
		u.User != nil || u.Fragment != "" {
		return nil, errors.New("upstream must be an absolute URL without credentials or fragment")
	}
	if requireHTTPS {
		if u.Scheme != "https" {
			return nil, errors.New("upstream must use HTTPS")
		}
	} else if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("upstream must use HTTP or HTTPS")
	}
	return u, nil
}

func NewService(examOrigin, upstream string, secret []byte) (*Service, error) {
	origin, err := url.Parse(examOrigin)
	if err != nil || origin.Host == "" || (origin.Scheme != "http" && origin.Scheme != "https") {
		return nil, errors.New("exam origin must be an absolute HTTP(S) URL")
	}
	base, err := parseUpstreamURL(upstream, false)
	if err != nil {
		return nil, errors.New("upstream must be an absolute HTTP(S) URL")
	}
	return &Service{ExamOrigin: strings.TrimRight(examOrigin, "/"), Upstream: base,
		TunnelEndpoint: "127.0.0.1:8788",
		ExamUpstreams:  make(map[string]*url.URL),
		PolicySecret:   secret, OIDCAuthorize: "https://idp.example/authorize",
		AdminEmails: make(map[string]bool),
		sessions:    make(map[string]*Session), exams: make(map[string]*Exam), events: make(map[string][]ExamEvent),
		tunnelTickets: make(map[string]*tunnelTicket), browserLogins: make(map[string]*browserLoginState), completions: make(map[string]time.Time)}, nil
}

// ParseExamUpstreams parses an operator-supplied JSON object mapping exam IDs
// to absolute HTTP(S) source page URLs, for example:
// {"course-101":"https://cs101.gbu.edu.cn/paper/category/exam"}.
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
		parsed, err := parseUpstreamURL(rawURL, false)
		if err != nil {
			return nil, fmt.Errorf("exam upstream for %q must be an absolute HTTP(S) URL without credentials or fragment", examID)
		}
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
	sourceURL, sourceOrigin, sourceHost := s.sourceDetails(examID)
	document := map[string]any{
		"version": 1, "exam_id": examID,
		"allowed_origins": []string{s.ExamOrigin, sourceOrigin},
		"allowed_paths":   []string{"/" + examID + "/**"},
		"source": map[string]any{
			"origin":      sourceOrigin,
			"url":         sourceURL,
			"host":        sourceHost,
			"endpoint_id": examID,
			"transport":   "byod-tunnel-v1",
		},
		"browser": map[string]any{
			// The fields mirror the first SEB-style baseline. Chromium's native
			// enforcement consumes these values; keeping them in the signed
			// document makes the server the single policy source of truth.
			"allow_background":  false,
			"allow_new_tabs":    false,
			"allow_new_windows": false,
			"allow_devtools":    false,
			"allow_print":       false,
			"allow_view_source": false,
			"allow_save_page":   false,
			"allow_downloads":   false,
			"allow_extensions":  false,
			"allow_incognito":   false,
			"allow_fullscreen":  true,
			// This controls browser window fullscreen on exam activation. It is
			// intentionally separate from allow_fullscreen, which controls page
			// fullscreen requests.
			"require_fullscreen": false,
			// When enabled together with require_fullscreen, Chromium rejects
			// Esc/F11/menu attempts to leave the exam's browser fullscreen mode.
			// It is cleared automatically when the session ends.
			"lock_fullscreen":               false,
			"allow_clipboard_read":          false,
			"allow_clipboard_write":         false,
			"allow_screen_capture":          false,
			"allow_navigation_outside_exam": false,
			"kiosk_mode":                    true,
			"exit_requires_unlock":          true,
		},
		"navigation": map[string]any{
			// The control-plane exam origin and the configured source origin are
			// both valid top-level origins during an active attempt.  The source
			// remains constrained to the signed, per-exam tunnel route below;
			// allowing it here avoids the policy engine blocking the actual exam
			// page after the browser switches from grips://exam.
			"allowed_origins": []string{s.ExamOrigin, sourceOrigin},
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
	if s.ExamStore != nil {
		if stored, err := s.ExamStore.Policy(context.Background(), examID); err == nil && stored != nil {
			for key, value := range stored {
				mergePolicyValue(document, key, value)
			}
		}
	}
	document["exam_id"] = examID
	document["allowed_origins"] = []string{s.ExamOrigin, sourceOrigin}
	return map[string]any{"key_id": "dev-hmac-1", "alg": "HS256", "document": document,
		"signature": s.sign(document)}
}

// sourceOrigin returns the browser-facing HTTPS origin for the configured
// upstream. The browser uses this value as the inner TLS server name; the
// tunnel endpoint itself is selected by the session's endpoint_id.
func (s *Service) sourceOrigin(examID string) (origin, host string) {
	_, origin, host = s.sourceDetails(examID)
	return origin, host
}

// sourceDetails separates the browser-facing page URL from the origin used
// for navigation policy and the host used to select the transparent tunnel.
func (s *Service) sourceDetails(examID string) (page, origin, host string) {
	upstream, err := s.upstreamForExam(context.Background(), examID)
	if err != nil || upstream == nil || upstream.Hostname() == "" {
		return "", "", ""
	}
	host = upstream.Hostname()
	pageURL := *upstream
	originURL := *upstream
	originURL.Path = ""
	originURL.RawPath = ""
	originURL.RawQuery = ""
	originURL.ForceQuery = false
	originURL.Fragment = ""
	return pageURL.String(), originURL.String(), host
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
	return s.configurationForRequest(examID, nil)
}

func (s *Service) configurationForRequest(examID string, request *http.Request) map[string]any {
	authorizeEndpoint := s.OIDCAuthorize
	clientID := "byod-browser"
	if s.DevAuth {
		authorizeEndpoint = s.ExamOrigin + "/dev/authorize"
	} else if s.OIDC != nil {
		authorizeEndpoint = s.OIDC.OAuth2.Endpoint.AuthURL
		clientID = s.OIDC.ClientID
	}
	sourceURL, sourceOrigin, sourceHost := s.sourceDetails(examID)
	exam := map[string]any{"id": examID, "origin": s.ExamOrigin,
		"proxy_origin": s.ExamOrigin, "unlock_path": "/" + examID + "/end",
		"source_url": sourceURL, "source_origin": sourceOrigin, "source_host": sourceHost,
		"endpoint_id": examID, "transport": "byod-tunnel-v1"}
	if s.ExamStore != nil {
		if stored, ok, err := s.ExamStore.GetExam(context.Background(), examID); err == nil && ok {
			exam["name"] = stored.Name
			exam["hashtag"] = stored.Hashtag
			exam["state"] = stored.State
			exam["starts_at"] = stored.StartsAt
			exam["ends_at"] = stored.EndsAt
		}
	}
	return map[string]any{"version": 1,
		"exam": exam,
		"tunnel": map[string]any{"protocol": "byod-tunnel-v1", "endpoint_id": examID,
			"endpoint":    s.tunnelEndpointForRequest(request),
			"ticket_path": "/v1/sessions/{session_id}/tunnel-ticket"},
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

func (s *Service) beginBrowserLogin(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil && !s.DevAuth {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "oidc_not_configured"})
		return
	}
	state := "browser-" + randomToken(24)
	verifier := pkceVerifier()
	now := time.Now()
	s.mu.Lock()
	if s.browserLogins == nil {
		s.browserLogins = make(map[string]*browserLoginState)
	}
	for key, pending := range s.browserLogins {
		if pending == nil || now.Sub(pending.CreatedAt) >= 15*time.Minute {
			delete(s.browserLogins, key)
		}
	}
	s.browserLogins[state] = &browserLoginState{CodeVerifier: verifier, CreatedAt: now}
	s.mu.Unlock()

	authorizationURL := s.ExamOrigin + "/dev/browser-authorize?state=" + url.QueryEscape(state)
	if s.OIDC != nil && !s.DevAuth {
		authorizationURL = s.OIDC.authorizationURL(state, verifier)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, authorizationURL, http.StatusFound)
}

func (s *Service) finishBrowserLogin(w http.ResponseWriter, r *http.Request, state, code string) bool {
	if !strings.HasPrefix(state, "browser-") {
		return false
	}
	s.mu.Lock()
	pending := s.browserLogins[state]
	valid := code != "" && pending != nil && time.Since(pending.CreatedAt) < 15*time.Minute
	// Authorization codes and state values are single-use. Reserve the state
	// before exchanging the code so concurrent callbacks cannot both succeed.
	delete(s.browserLogins, state)
	s.mu.Unlock()
	if !valid {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_browser_login_callback"})
		return true
	}
	identity, err := s.identityFromCode(r.Context(), code, pending.CodeVerifier, "")
	if err != nil {
		s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "oidc_exchange_failed"})
		return true
	}
	if s.ExamStore != nil {
		if _, err := s.ExamStore.ResolveIdentity(r.Context(), identity); err != nil {
			s.userError(w, err)
			return true
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "grips://login/?complete=1", http.StatusSeeOther)
	return true
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

// validSessionReturnURI accepts both the legacy and trusted-host Grips control
// documents and the server-hosted HTTPS exam shell. The HTTPS form is
// same-origin and may carry a target exam path, but it can never point to
// another host.
func (s *Service) validSessionReturnURI(returnURL *url.URL, examID string) bool {
	if returnURL == nil || examID == "" {
		return false
	}
	if returnURL.Scheme == "grips" &&
		(returnURL.Host == "exam" || returnURL.Host == "exam.cs.ac.cn") {
		return returnURL.User == nil && returnURL.Fragment == "" &&
			(returnURL.Path == "" || returnURL.Path == "/")
	}
	origin, err := url.Parse(s.ExamOrigin)
	if err != nil || returnURL.Scheme != origin.Scheme || returnURL.Host != origin.Host ||
		returnURL.User != nil || returnURL.Fragment != "" ||
		returnURL.Path != "" && returnURL.Path != "/" {
		return false
	}
	target := returnURL.Query().Get("target")
	if target == "" {
		return true
	}
	targetURL, err := url.Parse(target)
	if err != nil || targetURL.Scheme != origin.Scheme || targetURL.Host != origin.Host ||
		targetURL.User != nil || targetURL.Fragment != "" {
		return false
	}
	return targetURL.Path == "/"+examID || strings.HasPrefix(targetURL.Path, "/"+examID+"/")
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
	if token == "" {
		return nil
	}
	s.mu.RLock()
	for _, session := range s.sessions {
		if hmac.Equal([]byte(session.BrowserToken), []byte(token)) {
			s.mu.RUnlock()
			return session
		}
	}
	s.mu.RUnlock()
	// A server restart clears the in-memory session map. Restore the session
	// from its token digest so an otherwise valid browser can continue without
	// re-authenticating. Historical rows created before token persistence are
	// intentionally not recoverable and will follow the stale-session path.
	if s.ExamStore != nil {
		if session, err := s.ExamStore.FindSessionByToken(context.Background(), token); err == nil && session != nil {
			s.mu.Lock()
			if existing := s.sessions[session.ID]; existing != nil {
				s.mu.Unlock()
				return existing
			}
			s.sessions[session.ID] = session
			s.mu.Unlock()
			return session
		}
	}
	return nil
}

func (s *Service) authorize(token, id string) *Session {
	if token == "" || id == "" {
		return nil
	}
	s.mu.RLock()
	session := s.sessions[id]
	if session != nil && hmac.Equal([]byte(session.BrowserToken), []byte(token)) {
		s.mu.RUnlock()
		return session
	}
	s.mu.RUnlock()
	if s.ExamStore != nil {
		if restored, err := s.ExamStore.GetSessionByToken(context.Background(), id, token); err == nil && restored != nil {
			s.mu.Lock()
			if existing := s.sessions[id]; existing != nil {
				s.mu.Unlock()
				return existing
			}
			s.sessions[id] = restored
			s.mu.Unlock()
			return restored
		}
	}
	return nil
}

// checkStudentEligibility is called at both authentication and activation.
// An administrator may change the roster after a student has authenticated but
// before the scheduled start, so activation must re-check the durable roster
// and one-time completion record.
func (s *Service) checkStudentEligibility(ctx context.Context, examID, subject string) error {
	if examID == "" || subject == "" {
		return nil
	}
	if s.ExamStore != nil {
		allowed, err := s.ExamStore.UserAccess(ctx, s.identityIssuer(), subject, examID)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrExamStudentDenied
		}
	}
	completed, err := s.studentCompleted(ctx, examID, subject)
	if err != nil {
		return err
	}
	if completed {
		return ErrExamAlreadyDone
	}
	return nil
}

func (s *Service) appendEvent(session *Session, typ, severity, details string) ExamEvent {
	event := ExamEvent{ID: randomToken(12), SessionID: session.ID, AttemptID: session.AttemptID, BrowserSessionID: session.BrowserSessionID,
		Type: typ, Severity: severity, Details: details, OccurredAt: time.Now().Unix()}
	s.events[session.ID] = append(s.events[session.ID], event)
	if s.ExamStore != nil {
		_ = s.ExamStore.SaveEvent(context.Background(), event)
	}
	if s.ExamStore != nil {
		_ = s.ExamStore.SaveSession(context.Background(), session)
	}
	return event
}

func (s *Service) enforceIdleTimeout(session *Session) bool {
	if session == nil {
		return false
	}
	if s.ExamStore != nil {
		allowed, err := s.ExamStore.UserAccess(context.Background(), s.identityIssuer(), session.Subject, session.ExamID)
		if err != nil || !allowed {
			return false
		}
	}
	_, maxIdle := s.sessionLimits(session.ExamID)
	s.mu.Lock()
	suspended := false
	if session.State == "active" && time.Now().Unix()-session.LastSeenAt > maxIdle {
		session.State = "suspended"
		session.ViolationCount++
		session.LastViolation = "heartbeat_timeout"
		s.revokeTunnelTicketsLocked(session.ID)
		s.appendEvent(session, "heartbeat_timeout", "critical", "")
		suspended = true
	}
	active := session.State == "active" || session.State == "authenticated"
	s.mu.Unlock()
	if suspended && s.ExamStore != nil {
		_ = s.ExamStore.RevokeTunnelTickets(context.Background(), session.ID)
	}
	return active
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
	started := time.Now()
	requestID := requestID(r.Header.Get("X-Request-ID"))
	r = r.WithContext(context.WithValue(r.Context(), requestLogContextKey{}, requestID))
	w.Header().Set("X-Request-ID", requestID)
	response := &requestLogWriter{ResponseWriter: w, status: http.StatusOK}
	defer logHTTPRequest(r, response, started)
	w = response
	// grips://exam is a trusted Chromium WebUI origin, but it is still
	// cross-origin from the HTTPS exam endpoint.  Explicit CORS headers are
	// therefore required for the browser-side session bootstrap.
	if r.Header.Get("Origin") == "grips://exam" {
		w.Header().Set("Access-Control-Allow-Origin", "grips://exam")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-BYOD-Session")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")
		w.Header().Add("Vary", "Origin")
	}
	if s.userAuthRoute(w, r) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/api/") {
		s.adminAPI(w, r)
		return
	}
	if r.Method == http.MethodOptions {
		if r.Header.Get("Origin") != "grips://exam" {
			slog.WarnContext(r.Context(), "cors_request_rejected", "request_id", requestID,
				"origin", r.Header.Get("Origin"), "path", r.URL.Path)
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
	if r.URL.Path == "/" || r.URL.Path == "/exam" || r.URL.Path == "/exam/" {
		serveExamUI(w, r, "index.html")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/exam-assets/") {
		serveExamUI(w, r, strings.TrimPrefix(r.URL.Path, "/exam-assets/"))
		return
	}
	if r.URL.Path == "/account" || strings.HasPrefix(r.URL.Path, "/account/") {
		r.URL.Path = "/admin/"
		serveAdminUI(w, r)
		return
	}
	if r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/") {
		serveAdminUI(w, r)
		return
	}
	if r.URL.Path == "/openapi.yaml" {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(openAPISpec)
		return
	}
	if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
		if r.URL.Path == "/readyz" && s.ExamStore != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			err := s.ExamStore.Ping(ctx)
			cancel()
			if err != nil {
				s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "error": "database_unavailable"})
				return
			}
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "byod-server", "oidc": s.OIDC != nil || s.DevAuth})
		return
	}
	if r.URL.Path == "/browser/login" {
		s.beginBrowserLogin(w, r)
		return
	}
	if r.URL.Path == "/v1/exams/available" {
		u, _, err := s.currentUser(r)
		if err != nil {
			s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login_required"})
			return
		}
		if s.ExamStore == nil {
			s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "database_required"})
			return
		}
		exams, err := s.ExamStore.ListAvailableExamsForUser(r.Context(), u.ID)
		if err != nil {
			s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "exam_list_unavailable"})
			return
		}
		s.writeJSON(w, http.StatusOK, exams)
		return
	}
	if r.URL.Path == "/v1/exam-entry" {
		s.writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/.well-known/byod-configuration") {
		if examID := examIDFromWellKnown(r.URL.Path); examID != "" {
			if _, err := s.examWindow(r.Context(), examID, gateAuthenticate); err != nil {
				s.writeExamError(w, err)
				return
			}
			s.writeJSON(w, http.StatusOK, s.configurationForRequest(examID, r))
			return
		}
	}
	if r.URL.Path == "/oidc/callback" {
		state, code := r.URL.Query().Get("state"), r.URL.Query().Get("code")
		if s.finishUserLogin(w, r, state, code) {
			return
		}
		if s.finishBrowserLogin(w, r, state, code) {
			return
		}
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
		var identity OIDCIdentity
		if s.OIDC != nil && !s.DevAuth {
			var err error
			identity, err = s.OIDC.exchangeIdentity(r.Context(), code, codeVerifier, "")
			subject = identity.Subject
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
			identity = OIDCIdentity{Issuer: s.identityIssuer(), Subject: "oidc:" + code}
			subject = "oidc:" + code
		}
		if s.ExamStore != nil {
			identity.Issuer = s.identityIssuer()
			if _, err := s.ExamStore.ResolveIdentity(r.Context(), identity); err != nil {
				if errors.Is(err, ErrUserDisabled) || errors.Is(err, ErrIdentityConflict) {
					s.userError(w, err)
				} else {
					s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "user_identity_unavailable"})
				}
				return
			}
		}
		if _, gateErr := s.examWindow(r.Context(), session.ExamID, gateAuthenticate); gateErr != nil {
			s.mu.Lock()
			if current := s.sessions[state]; current != nil {
				current.State = "ended"
			}
			s.mu.Unlock()
			s.writeExamError(w, gateErr)
			return
		}
		if eligibilityErr := s.checkStudentEligibility(r.Context(), session.ExamID, subject); eligibilityErr != nil {
			if errors.Is(eligibilityErr, ErrExamStudentDenied) {
				s.mu.Lock()
				if current := s.sessions[state]; current != nil && current.State == "authenticating" {
					current.State = "pending"
				}
				s.mu.Unlock()
				s.writeExamError(w, eligibilityErr)
				return
			}
			if errors.Is(eligibilityErr, ErrExamAlreadyDone) {
				s.mu.Lock()
				if current := s.sessions[state]; current != nil {
					current.State = "ended"
				}
				s.mu.Unlock()
				s.writeExamError(w, eligibilityErr)
				return
			}
			s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "student_eligibility_unavailable"})
			return
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
			if err != nil || !s.validSessionReturnURI(redirect, examID) {
				s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_return_uri"})
				return
			}
			target := redirect.Query().Get("target")
			if target == "" {
				if redirect.Scheme == "https" {
					target = strings.TrimRight(s.ExamOrigin, "/") + "/" + examID
				} else {
					s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing_return_target"})
					return
				}
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
	if r.URL.Path == "/dev/browser-authorize" && s.DevAuth {
		state := r.URL.Query().Get("state")
		s.mu.RLock()
		_, ok := s.browserLogins[state]
		s.mu.RUnlock()
		if !ok {
			s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_state"})
			return
		}
		callback := &url.URL{Path: "/oidc/callback"}
		query := callback.Query()
		query.Set("state", state)
		query.Set("code", "dev-browser-login")
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
	if len(parts) == 3 && parts[0] == "exams" && parts[2] == "complete" {
		s.completeExamRequest(w, r, parts[1])
		return
	}
	if len(parts) == 2 && parts[1] == "end" {
		session := s.findByToken(tokenFromRequest(r))
		if session == nil || session.ExamID != parts[0] {
			s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "active_session_required"})
			return
		}
		if err := s.endWithReason(r.Context(), session, "manual"); err != nil {
			s.writeCompletionError(w, err)
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.Redirect(w, r, s.ExamOrigin+"/?ended=1", http.StatusSeeOther)
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
		if _, gateErr := s.examWindow(r.Context(), session.ExamID, gateAuthenticate); gateErr != nil {
			if errors.Is(gateErr, ErrExamEnded) {
				s.mu.Lock()
				session.State = "ended"
				s.mu.Unlock()
			}
			s.writeJSON(w, examErrorStatus(gateErr), map[string]any{"error": gateErr.Error(), "session_id": session.ID, "state": session.State})
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

func serveAdminUI(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/admin/")
	if name == "" {
		name = "index.html"
	}
	if strings.HasPrefix(name, "assets/") {
		// asset paths are immutable and can be cached by the browser
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		name = "index.html"
		w.Header().Set("Cache-Control", "no-store")
	}
	http.ServeFileFS(w, r, adminUIDist, "admin-ui/dist/"+name)
}

func serveExamUI(w http.ResponseWriter, r *http.Request, name string) {
	if name == "" || strings.Contains(name, "..") || strings.Contains(name, "\\") {
		name = "index.html"
	}
	// app.js is intentionally not cached: the dynamic shell and its native
	// bridge contract are released together with the server image. A stale
	// cached app.js can otherwise keep a browser on the old HTTPS-only flow.
	if name == "index.html" || name == "app.js" || name == "bridge.html" {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=300")
	}
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; base-uri 'none'; object-src 'none'; "+
			"frame-ancestors grips://exam.cs.ac.cn; connect-src 'self'; script-src 'self'; "+
			"style-src 'self' 'unsafe-inline'; img-src 'self' data:;")
	http.ServeFileFS(w, r, examUIDist, "exam-ui/dist/"+name)
}

func (s *Service) adminAPI(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireAdminAPI(w, r)
	if !ok {
		return
	}
	if s.ExamStore == nil {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "database_required"})
		return
	}
	if s.globalUserAPI(w, r, actor) {
		return
	}
	if r.URL.Path == "/admin/api/exams" && r.Method == http.MethodGet {
		var exams []StoredExam
		var err error
		if actor.PlatformAdmin {
			exams, err = s.ExamStore.ListExams(r.Context())
		} else {
			exams, err = s.ExamStore.ListExamsForUser(r.Context(), actor.ID)
		}
		if err != nil {
			s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database_error"})
			return
		}
		s.writeJSON(w, http.StatusOK, exams)
		return
	}
	if r.URL.Path == "/admin/api/exams" && r.Method == http.MethodPost {
		if !actor.PlatformAdmin {
			s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "platform_admin_required"})
			return
		}
		var input struct {
			Name     string         `json:"name"`
			Hashtag  string         `json:"hashtag"`
			BaseURL  string         `json:"base_url"`
			StartsAt *time.Time     `json:"starts_at"`
			EndsAt   *time.Time     `json:"ends_at"`
			Policy   map[string]any `json:"policy"`
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || !validExamName(input.Name) || !validExamHashtag(input.Hashtag) {
			s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_exam"})
			return
		}
		exam, err := s.ExamStore.CreateExamNamed(r.Context(), input.Name, input.Hashtag, input.BaseURL, input.StartsAt, input.EndsAt, input.Policy)
		if err != nil {
			s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_exam"})
			return
		}
		s.writeJSON(w, http.StatusCreated, exam)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 5 && parts[0] == "admin" && parts[1] == "api" && parts[2] == "exams" && parts[4] == "admins" {
		if r.Method == http.MethodGet {
			admins, err := s.ExamStore.ListExamAdmins(r.Context(), parts[3])
			if err != nil {
				s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database_error"})
			} else {
				s.writeJSON(w, http.StatusOK, admins)
			}
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if len(parts) == 6 && parts[0] == "admin" && parts[1] == "api" && parts[2] == "exams" && parts[4] == "admins" {
		if !actor.PlatformAdmin {
			s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "platform_admin_required"})
			return
		}
		if r.Method == http.MethodPut {
			var input struct {
				Enabled bool `json:"enabled"`
			}
			input.Enabled = true
			if decodeUserInput(r, &input) != nil {
				s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_exam_admin"})
				return
			}
			if err := s.ExamStore.SetExamAdmin(r.Context(), parts[3], parts[5], input.Enabled, actor.ID); err != nil {
				s.userError(w, err)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
			return
		}
		if r.Method == http.MethodDelete {
			if err := s.ExamStore.RemoveExamAdmin(r.Context(), parts[3], parts[5], actor.ID); err != nil {
				s.userError(w, err)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if len(parts) == 5 && parts[0] == "admin" && parts[1] == "api" && parts[2] == "exams" && parts[4] == "publish" && r.Method == http.MethodPost {
		exam, ok, err := s.ExamStore.GetExam(r.Context(), parts[3])
		if err != nil {
			s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database_error"})
			return
		}
		if !ok {
			s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "exam_not_found"})
			return
		}
		if exam.State == "ended" {
			s.writeJSON(w, http.StatusConflict, map[string]string{"error": "exam_ended"})
			return
		}
		if exam.EndsAt != nil && !time.Now().Before(*exam.EndsAt) {
			s.writeJSON(w, http.StatusConflict, map[string]string{"error": "exam_ended"})
			return
		}
		nextState := "active"
		if exam.StartsAt != nil && time.Now().Before(*exam.StartsAt) {
			nextState = "scheduled"
		}
		if err := s.ExamStore.SetExamState(r.Context(), parts[3], nextState); err != nil {
			s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database_error"})
			return
		}
		exam.State = nextState
		s.writeJSON(w, http.StatusOK, exam)
		return
	}
	if r.URL.Path == "/admin/api/sessions" && r.Method == http.MethodGet {
		if !actor.PlatformAdmin {
			s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "platform_admin_required"})
			return
		}
		if sessions, err := s.ExamStore.ListAllSessions(r.Context()); err == nil {
			s.writeJSON(w, http.StatusOK, sessions)
			return
		}
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database_error"})
		return
	}
	if len(parts) == 4 && parts[0] == "admin" && parts[1] == "api" && parts[2] == "exams" {
		found, foundOK, err := s.ExamStore.GetExam(r.Context(), parts[3])
		if err != nil {
			s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database_error"})
			return
		}
		if !foundOK {
			found = nil
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
			if !actor.PlatformAdmin {
				s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "platform_admin_required"})
				return
			}
			if err := s.ExamStore.DeleteExam(r.Context(), parts[3]); err != nil {
				s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database_error"})
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodPatch {
			var input struct {
				Name     string         `json:"name"`
				Hashtag  string         `json:"hashtag"`
				BaseURL  string         `json:"base_url"`
				StartsAt *time.Time     `json:"starts_at"`
				EndsAt   *time.Time     `json:"ends_at"`
				Policy   map[string]any `json:"policy"`
			}
			decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&input) != nil || !validExamName(input.Name) || !validExamHashtag(input.Hashtag) || input.BaseURL == "" {
				s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_exam"})
				return
			}
			updated, err := s.ExamStore.UpdateExamNamed(r.Context(), parts[3], input.Name, input.Hashtag, input.BaseURL, input.StartsAt, input.EndsAt, input.Policy)
			if err != nil {
				s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_exam"})
				return
			}
			s.writeJSON(w, http.StatusOK, updated)
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
		// Legacy subject rosters are imported on authenticated login. All new
		// edits must use global user IDs to avoid a second identity database.
		s.writeJSON(w, http.StatusGone, map[string]string{"error": "use_global_user_participants"})
		return
	}
	if len(parts) == 5 && parts[0] == "admin" && parts[1] == "api" && parts[2] == "exams" && parts[4] == "sessions" && r.Method == http.MethodGet {
		if sessions, err := s.ExamStore.ListSessions(r.Context(), parts[3]); err == nil {
			s.writeJSON(w, http.StatusOK, sessions)
			return
		}
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
		if events, err := s.ExamStore.ListEvents(r.Context(), parts[3]); err == nil {
			s.writeJSON(w, http.StatusOK, events)
			return
		}
		s.mu.RLock()
		events := append([]ExamEvent(nil), s.events[parts[3]]...)
		s.mu.RUnlock()
		s.writeJSON(w, http.StatusOK, events)
		return
	}
	if len(parts) == 4 && parts[0] == "admin" && parts[1] == "api" && parts[2] == "sessions" && r.Method == http.MethodGet {
		if session, err := s.ExamStore.GetSession(r.Context(), parts[3]); err == nil {
			s.writeJSON(w, http.StatusOK, session)
			return
		}
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "session_not_found"})
		return
	}
	if len(parts) == 4 && parts[0] == "admin" && parts[1] == "api" && parts[2] == "sessions" && r.Method == http.MethodPost {
		var input struct {
			Action string `json:"action"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&input) != nil || (input.Action != "suspend" && input.Action != "resume") {
			s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_action"})
			return
		}
		if session, err := s.ExamStore.GetSession(r.Context(), parts[3]); err == nil {
			oldState := session.State
			if input.Action == "resume" && oldState != "suspended" {
				s.writeJSON(w, http.StatusConflict, map[string]string{"error": "session_not_suspended"})
				return
			}
			if input.Action == "suspend" && (oldState == "ended" || oldState == "pending") {
				s.writeJSON(w, http.StatusConflict, map[string]string{"error": "session_not_active"})
				return
			}
			s.mu.Lock()
			if input.Action == "suspend" {
				session.State = "suspended"
				s.revokeTunnelTicketsLocked(session.ID)
			} else {
				session.State = "active"
			}
			var auditEvent ExamEvent
			if live := s.sessions[parts[3]]; live != nil {
				live.State = session.State
				auditEvent = s.appendEvent(live, "admin_"+input.Action, "warning", oldState+" -> "+session.State)
			} else {
				auditEvent = ExamEvent{ID: randomToken(12), SessionID: session.ID, Type: "admin_" + input.Action, Severity: "warning", Details: oldState + " -> " + session.State, OccurredAt: time.Now().Unix()}
			}
			s.mu.Unlock()
			if input.Action == "suspend" && s.ExamStore != nil {
				_ = s.ExamStore.RevokeTunnelTickets(r.Context(), session.ID)
			}
			_ = s.ExamStore.SaveSession(r.Context(), &Session{ID: session.ID, ExamID: session.ExamID, Subject: session.Subject, State: session.State, CreatedAt: session.CreatedAt.Unix(), LastSeenAt: session.LastSeenAt.Unix(), ViolationCount: session.ViolationCount})
			if auditEvent.ID != "" {
				_ = s.ExamStore.SaveEvent(r.Context(), auditEvent)
			}
			s.writeJSON(w, http.StatusOK, session)
			return
		}
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "session_not_found"})
		return
	}
	if r.URL.Path == "/admin/api/events" && r.Method == http.MethodGet {
		if !actor.PlatformAdmin {
			s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "platform_admin_required"})
			return
		}
		limit := 200
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil {
				limit = n
			}
		}
		if events, err := s.ExamStore.ListAllEvents(r.Context(), limit); err == nil {
			s.writeJSON(w, http.StatusOK, events)
			return
		}
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database_error"})
		return
	}
	s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
}

func (s *Service) completeExamRequest(w http.ResponseWriter, r *http.Request, examID string) {
	session := s.findByToken(tokenFromRequest(r))
	if session == nil || session.ExamID != examID {
		s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "active_session_required"})
		return
	}
	if session.State == "ended" {
		s.writeExamError(w, ErrExamAlreadyDone)
		return
	}
	if session.State != "active" && session.State != "authenticated" {
		s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "active_session_required"})
		return
	}
	if err := s.endWithReason(r.Context(), session, "manual"); err != nil {
		s.writeCompletionError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "byod_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: strings.HasPrefix(s.ExamOrigin, "https://"), SameSite: http.SameSiteStrictMode})
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, s.ExamOrigin+"/?ended=1", http.StatusSeeOther)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"session_id": session.ID, "exam_id": examID, "state": "ended"})
}

func (s *Service) post(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v1/exams/") && strings.HasSuffix(r.URL.Path, "/complete") {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 4 || parts[0] != "v1" || parts[1] != "exams" || !validExamID(parts[2]) {
			s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		s.completeExamRequest(w, r, parts[2])
		return
	}
	if r.URL.Path == "/v1/exam-entry" {
		// The student-facing code-entry flow was removed. Exam discovery is
		// identity-based: OIDC login followed by GET /v1/exams/available.
		s.writeJSON(w, http.StatusGone, map[string]string{"error": "exam_code_entry_removed", "message": "sign in with Connect to choose an assigned exam"})
		return
	}
	if r.URL.Path == "/v1/sessions" {
		var input struct {
			ExamID    string `json:"exam_id"`
			ReturnURI string `json:"return_uri"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&input); err != nil {
			s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_exam_id"})
			return
		}
		if !validExamID(input.ExamID) {
			s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_exam_id"})
			return
		}
		if _, err := s.examWindow(r.Context(), input.ExamID, gateAuthenticate); err != nil {
			s.writeExamError(w, err)
			return
		}
		// A student who already completed the Connect login does not need a
		// second OIDC round-trip for every exam. The user session cookie is
		// bound to the OIDC identity and the participant roster is checked before
		// issuing an authenticated exam session.
		authenticatedSubject := ""
		if s.ExamStore != nil {
			user, _, userErr := s.currentUser(r)
			if userErr != nil {
				s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login_required"})
				return
			}
			if user.Subject == nil || strings.TrimSpace(*user.Subject) == "" {
				s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity_not_bound"})
				return
			}
			authenticatedSubject = *user.Subject
			if eligibilityErr := s.checkStudentEligibility(r.Context(), input.ExamID, authenticatedSubject); eligibilityErr != nil {
				s.writeExamError(w, eligibilityErr)
				return
			}
		}
		if input.ReturnURI != "" {
			returnURL, err := url.Parse(input.ReturnURI)
			if err != nil || !s.validSessionReturnURI(returnURL, input.ExamID) {
				s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_return_uri"})
				return
			}
		}
		now := time.Now().Unix()
		state := "pending"
		if authenticatedSubject != "" {
			state = "authenticated"
		}
		session := &Session{ID: randomToken(18), AttemptID: randomToken(18), BrowserSessionID: randomToken(18), BrowserToken: randomToken(32), ExamID: input.ExamID, Subject: authenticatedSubject, State: state, CreatedAt: now, LastSeenAt: now, ReturnURI: input.ReturnURI}
		s.mu.Lock()
		if _, exists := s.exams[input.ExamID]; !exists {
			code, _ := newExamCode()
			s.exams[input.ExamID] = &Exam{ID: input.ExamID, ExamCode: code, Origin: s.ExamOrigin, PolicyVersion: 1}
		}
		s.sessions[session.ID] = session
		if s.ExamStore != nil {
			_ = s.ExamStore.SaveSession(r.Context(), session)
		}
		s.appendEvent(session, "attempt_created", "info", "")
		if authenticatedSubject != "" {
			s.appendEvent(session, "authentication_succeeded", "info", "existing_user_session")
		}
		s.mu.Unlock()
		session.CodeVerifier = pkceVerifier()
		http.SetCookie(w, &http.Cookie{Name: "byod_session", Value: session.BrowserToken, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.ExamOrigin, "https://"), SameSite: http.SameSiteStrictMode})
		authorizationURL := ""
		if state == "pending" {
			authorizationURL = s.authorizationURL(session)
		}
		s.writeJSON(w, http.StatusCreated, map[string]string{"session_id": session.ID, "attempt_id": session.AttemptID, "browser_session_id": session.BrowserSessionID, "browser_token": session.BrowserToken,
			"authorization_url": authorizationURL, "state": session.State})
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
			if _, gateErr := s.examWindow(r.Context(), session.ExamID, gateStart); gateErr != nil {
				s.writeExamError(w, gateErr)
				return
			}
			if eligibilityErr := s.checkStudentEligibility(r.Context(), session.ExamID, session.Subject); eligibilityErr != nil {
				s.mu.Lock()
				if errors.Is(eligibilityErr, ErrExamAlreadyDone) {
					session.State = "ended"
				} else if errors.Is(eligibilityErr, ErrExamStudentDenied) {
					session.State = "suspended"
				}
				s.mu.Unlock()
				if errors.Is(eligibilityErr, ErrExamAlreadyDone) || errors.Is(eligibilityErr, ErrExamStudentDenied) {
					s.writeExamError(w, eligibilityErr)
				} else {
					s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "student_eligibility_unavailable"})
				}
				return
			}
			if s.ExamStore != nil {
				if err := s.ExamStore.ActivateSession(r.Context(), session.ID, session.ExamID, session.Subject, s.identityIssuer()); err != nil {
					s.writeExamError(w, err)
					return
				}
			}
			s.mu.Lock()
			if session.State != "authenticated" && session.State != "active" {
				state := session.State
				s.mu.Unlock()
				s.writeJSON(w, http.StatusConflict, map[string]string{"error": "authentication_required", "state": state})
				return
			}
			if _, done := s.completions[session.ExamID+"\x00"+session.Subject]; done {
				s.mu.Unlock()
				s.writeExamError(w, ErrExamAlreadyDone)
				return
			}
			wasActive := session.State == "active"
			session.State = "active"
			session.LastSeenAt = time.Now().Unix()
			if !wasActive {
				s.appendEvent(session, "exam_started", "info", "")
			}
			s.mu.Unlock()
			s.writeJSON(w, http.StatusOK, map[string]string{"session_id": session.ID, "state": session.State, "proxy_base": "/" + session.ExamID + "/"})
			return
		}
		if parts[3] == "end" {
			if err := s.endWithReason(r.Context(), session, "manual"); err != nil {
				s.writeCompletionError(w, err)
				return
			}
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
			if session.State == "ended" {
				s.mu.Unlock()
				s.writeExamError(w, ErrExamAlreadyDone)
				return
			}
			session.ViolationCount++
			session.LastViolation = input.Type
			revoked := false
			if input.Type == "background" || input.Type == "devtools" {
				session.State = "suspended"
				s.revokeTunnelTicketsLocked(session.ID)
				revoked = true
			}
			event := s.appendEvent(session, input.Type, "critical", input.Details)
			count, state := session.ViolationCount, session.State
			s.mu.Unlock()
			if revoked && s.ExamStore != nil {
				_ = s.ExamStore.RevokeTunnelTickets(r.Context(), session.ID)
			}
			s.writeJSON(w, http.StatusOK, map[string]any{"session_id": session.ID, "state": state, "violation_count": count, "event_id": event.ID})
			return
		}
		if parts[3] == "heartbeat" {
			if _, gateErr := s.examWindow(r.Context(), session.ExamID, gateActive); gateErr != nil {
				s.writeExamError(w, gateErr)
				return
			}
			s.mu.Lock()
			now := time.Now().Unix()
			heartbeat, maxIdle := s.sessionLimits(session.ExamID)
			revoked := false
			if session.State == "active" && now-session.LastSeenAt > maxIdle {
				session.State = "suspended"
				session.ViolationCount++
				session.LastViolation = "heartbeat_timeout"
				s.revokeTunnelTicketsLocked(session.ID)
				revoked = true
				s.appendEvent(session, "heartbeat_timeout", "critical", "")
			} else if session.State == "active" {
				session.LastSeenAt = now
			}
			state := session.State
			lastSeen := session.LastSeenAt
			s.mu.Unlock()
			if revoked && s.ExamStore != nil {
				_ = s.ExamStore.RevokeTunnelTickets(r.Context(), session.ID)
			}
			if s.ExamStore != nil {
				_ = s.ExamStore.SaveSession(r.Context(), session)
			}
			s.writeJSON(w, http.StatusOK, map[string]any{"session_id": session.ID, "state": state, "last_seen_at": lastSeen, "heartbeat_seconds": heartbeat, "max_idle_seconds": maxIdle})
			return
		}
		if parts[3] == "tunnel-ticket" {
			ticket, info, err := s.IssueTunnelTicket(r.Context(), session.ID)
			if err != nil {
				s.writeJSON(w, http.StatusConflict, map[string]string{"error": "active_session_required"})
				return
			}
			s.writeJSON(w, http.StatusCreated, map[string]any{
				"ticket": ticket, "endpoint_id": info.EndpointID,
				"expires_at": info.ExpiresAt.UTC(), "protocol": "byod-tunnel-v1",
			})
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
	_ = s.endWithReason(context.Background(), session, "manual")
}

func (s *Service) endWithReason(ctx context.Context, session *Session, reason string) error {
	if session == nil {
		return ErrExamNotFound
	}
	s.mu.RLock()
	if session.State == "ended" {
		s.mu.RUnlock()
		return nil
	}
	subject := session.Subject
	s.mu.RUnlock()
	claimed, err := s.recordStudentCompletion(ctx, session.ExamID, subject, session.ID, time.Now())
	if err != nil {
		return err
	}
	if !claimed {
		// A second authenticated session for the same student raced the first
		// submission. Close this session as well, but report the terminal state
		// to the caller instead of pretending that a second submission worked.
		s.mu.Lock()
		if session.State != "ended" {
			session.State = "ended"
			s.revokeTunnelTicketsLocked(session.ID)
			s.appendEvent(session, "duplicate_completion", "warning", "student_already_completed")
		}
		s.mu.Unlock()
		return ErrExamAlreadyDone
	}
	s.mu.Lock()
	if session.State == "ended" {
		s.mu.Unlock()
		return nil
	}
	session.State = "ended"
	s.revokeTunnelTicketsLocked(session.ID)
	details := reason
	if details == "" {
		details = "manual"
	}
	s.appendEvent(session, "exam_completed", "info", details)
	// All previously authenticated attempts for this student are terminal,
	// not just the tab that submitted. Existing CONNECT streams are checked
	// against these states and shut down by the tunnel monitor.
	for _, other := range s.sessions {
		if other.ID != session.ID && subject != "" && other.ExamID == session.ExamID && other.Subject == subject && other.State != "ended" {
			other.State = "ended"
			s.revokeTunnelTicketsLocked(other.ID)
			s.appendEvent(other, "exam_completed_elsewhere", "info", session.ID)
		}
	}
	s.mu.Unlock()
	if s.ExamStore != nil {
		if err := s.ExamStore.RevokeTunnelTickets(ctx, session.ID); err != nil {
			return err
		}
	}
	return nil
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
	if _, gateErr := s.examWindow(r.Context(), examID, gateActive); gateErr != nil {
		s.writeExamError(w, gateErr)
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
	// Never forward the browser bearer token to the exam origin. The BYOD server
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
