package byodserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestSessionExpiryPolicy(t *testing.T) {
	s, _ := NewService("https://exam.cs.ac.cn", "https://source.example", nil)
	if heartbeat, idle := s.sessionLimits("exam"); heartbeat != 15 || idle != 300 {
		t.Fatalf("defaults: %d/%d", heartbeat, idle)
	}
	s.PolicyOverrides = map[string]map[string]any{"exam": {"session": map[string]any{"heartbeat_seconds": 10, "max_idle_seconds": 600}}}
	for _, tc := range []struct {
		policy          map[string]any
		heartbeat, idle int64
	}{
		{nil, 10, 600},
		{map[string]any{"session": map[string]any{"max_idle_seconds": float64(120)}}, 10, 120},
		{map[string]any{"session": map[string]any{"heartbeat_seconds": 300, "max_idle_seconds": 120}}, 300, 300},
		{map[string]any{"session": map[string]any{"max_idle_seconds": -1}}, 10, 300},
	} {
		h, idle := s.effectiveSessionLimits("exam", tc.policy)
		if h != tc.heartbeat || idle != tc.idle {
			t.Fatalf("limits: %d/%d for %#v", h, idle, tc.policy)
		}
	}
	if _, idle := s.sessionLimits("exam"); idle != 600 {
		t.Fatal("DB policy mutated config")
	}
	doc := s.policy("exam")["document"].(map[string]any)
	if h, idle := sessionLimitsFromDocument(doc); h != 10 || idle != 600 {
		t.Fatal("signed policy differs from enforcement")
	}
}

func TestSessionExpiryBoundaryStatesAndNoSubmission(t *testing.T) {
	s, _ := NewService("https://exam.cs.ac.cn", "https://source.example", nil)
	now := time.Now().Truncate(time.Second)
	for _, state := range []string{"pending", "authenticating", "authenticated", "active", "suspended", "ended"} {
		s.sessions[state] = &Session{ID: state, ExamID: "exam", Subject: "student", State: state, LastSeenAt: now.Add(-301 * time.Second).Unix()}
	}
	s.sessions["boundary"] = &Session{ID: "boundary", ExamID: "exam", State: "active", LastSeenAt: now.Add(-300 * time.Second).Unix()}
	s.sessions["custom"] = &Session{ID: "custom", ExamID: "other", State: "active", LastSeenAt: now.Add(-301 * time.Second).Unix()}
	s.PolicyOverrides = map[string]map[string]any{"other": {"session": map[string]any{"max_idle_seconds": 600}}}
	s.tunnelTickets["ticket"] = &tunnelTicket{SessionID: "active", ExpiresAt: now.Add(time.Hour)}
	count, err := s.ExpireIdleSessions(context.Background(), now)
	if err != nil || count != 5 {
		t.Fatalf("expired %d: %v", count, err)
	}
	if s.sessions["boundary"].State != "active" || s.sessions["custom"].State != "active" {
		t.Fatal("expired a fresh/custom session")
	}
	if len(s.tunnelTickets) != 0 || len(s.completions) != 0 {
		t.Fatal("expiry kept credentials or submitted exam")
	}
	if event := s.events["active"]; len(event) != 1 || event[0].Type != "session_expired" || s.sessions["active"].ViolationCount != 0 {
		t.Fatal("expiry not recorded as a non-violation lifecycle event")
	}
	count, err = s.ExpireIdleSessions(context.Background(), now)
	if err != nil || count != 0 {
		t.Fatalf("not idempotent: %d %v", count, err)
	}
	if err := s.checkStudentEligibility(context.Background(), "exam", "student"); err != nil {
		t.Fatalf("cannot re-enter after timeout: %v", err)
	}
	s.Role = "data"
	count, err = s.ExpireIdleSessions(context.Background(), now.Add(time.Hour))
	if err != nil || count != 0 || s.sessions["boundary"].State != "active" {
		t.Fatal("data role advanced session state")
	}
}

func TestSessionExpirySchedulerWithoutRequests(t *testing.T) {
	s, _ := NewService("https://exam.cs.ac.cn", "https://source.example", nil)
	s.sessions["old"] = &Session{ID: "old", ExamID: "exam", State: "active", LastSeenAt: time.Now().Add(-time.Hour).Unix()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.runSessionExpiry(ctx, 10*time.Millisecond) }()
	t.Cleanup(func() { cancel(); <-done })
	for _, id := range []string{"old", "periodic"} {
		if id == "periodic" {
			s.mu.Lock()
			s.sessions[id] = &Session{ID: id, ExamID: "exam", State: "authenticated", LastSeenAt: time.Now().Add(-time.Hour).Unix()}
			s.mu.Unlock()
		}
		deadline := time.Now().Add(time.Second)
		for {
			s.mu.RLock()
			ended := s.sessions[id].State == "ended"
			s.mu.RUnlock()
			if ended {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("scheduler needed an HTTP request")
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func TestLateHeartbeatEndsInsteadOfResurrecting(t *testing.T) {
	for _, state := range []string{"active", "authenticated", "suspended"} {
		t.Run(state, func(t *testing.T) {
			s, _ := NewService("https://exam.cs.ac.cn", "https://source.example", nil)
			s.PolicyOverrides = map[string]map[string]any{"exam": {"session": map[string]any{"heartbeat_seconds": 5, "max_idle_seconds": 20}}}
			x := &Session{ID: "session", ExamID: "exam", Subject: "student", State: state, BrowserToken: "token", LastSeenAt: time.Now().Add(-21 * time.Second).Unix()}
			s.sessions[x.ID] = x
			r := httptest.NewRequest(http.MethodPost, "/v1/sessions/session/heartbeat", nil)
			r.Header.Set("Authorization", "Bearer token")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			var response map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || response["state"] != "ended" || response["max_idle_seconds"] != float64(20) {
				t.Fatalf("late heartbeat: %d %s", w.Code, w.Body.String())
			}
			if x.ViolationCount != 0 || len(s.completions) != 0 {
				t.Fatal("timeout became a violation or completion")
			}
			w = httptest.NewRecorder()
			r = httptest.NewRequest(http.MethodPost, "/v1/sessions/session/start", nil)
			r.Header.Set("Authorization", "Bearer token")
			s.ServeHTTP(w, r)
			if w.Code != 409 || x.State != "ended" {
				t.Fatal("old session could restart")
			}
		})
	}
}

func TestWaitingSessionHeartbeatKeepsItAlive(t *testing.T) {
	s, _ := NewService("https://exam.cs.ac.cn", "https://source.example", nil)
	x := &Session{ID: "waiting", ExamID: "exam", State: "authenticated", LastSeenAt: time.Now().Add(-2 * time.Minute).Unix()}
	s.sessions[x.ID] = x
	state, last, _, idle, err := s.refreshSession(context.Background(), x, true)
	if err != nil || state != "authenticated" || idle != 300 || time.Now().Unix()-last > 1 {
		t.Fatalf("waiting heartbeat: %s %d %v", state, last, err)
	}
}

func TestSessionExpiryConcurrentStatusRequests(t *testing.T) {
	s, _ := NewService("https://exam.cs.ac.cn", "https://source.example", nil)
	x := &Session{ID: "session", ExamID: "exam", State: "active", BrowserToken: "token", LastSeenAt: time.Now().Unix()}
	s.sessions[x.ID] = x
	var workers sync.WaitGroup
	for _, operation := range []string{"status", "heartbeat", "sweep"} {
		workers.Add(1)
		go func(operation string) {
			defer workers.Done()
			for i := 0; i < 100; i++ {
				if operation == "sweep" {
					if _, err := s.ExpireIdleSessions(context.Background(), time.Now().Add(time.Hour)); err != nil {
						t.Error(err)
					}
					continue
				}
				method, path := http.MethodGet, "/v1/sessions/session"
				if operation == "heartbeat" {
					method, path = http.MethodPost, path+"/heartbeat"
				}
				r := httptest.NewRequest(method, path, nil)
				r.Header.Set("Authorization", "Bearer token")
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if w.Code != 200 {
					t.Errorf("%s: %d", operation, w.Code)
				}
			}
		}(operation)
	}
	workers.Wait()
	if x.State != "ended" {
		t.Fatal("concurrent heartbeat resurrected ended session")
	}
}

func TestPostgresSessionExpiryDurableNoSubmission(t *testing.T) {
	f := newSplitFixture(t)
	ctx := context.Background()
	store := f.control.ExamStore
	if _, err := store.db.ExecContext(ctx, `UPDATE byod_exams SET policy_json=jsonb_set(policy_json,'{session}','{"heartbeat_seconds":5,"max_idle_seconds":20}') WHERE exam_id=$1`, f.session.ExamID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE byod_sessions SET last_seen_at=now()-interval '21 seconds' WHERE id=$1`, f.session.ID); err != nil {
		t.Fatal(err)
	}
	// A restarted control plane has no cached session. Sweep must use DB rows.
	f.control.sessions = make(map[string]*Session)
	if _, err := f.control.ExpireIdleSessions(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	x, err := store.GetSession(ctx, f.session.ID)
	if err != nil || x.State != "ended" {
		t.Fatalf("durable expiry: %#v %v", x, err)
	}
	if completed, err := store.StudentCompleted(ctx, f.session.ExamID, f.session.Subject); err != nil || completed {
		t.Fatalf("timeout claimed completion: %v", err)
	}
	if _, err := f.data.lookupVLESSCredential(ctx, f.uuid); err == nil {
		t.Fatal("expired session credential accepted")
	}
	var revoked bool
	if err := store.db.QueryRowContext(ctx, `SELECT used_at IS NOT NULL FROM byod_tunnel_tickets WHERE session_id=$1`, f.session.ID).Scan(&revoked); err != nil || !revoked {
		t.Fatalf("credential not revoked: %v", err)
	}
	events, err := store.ListEvents(ctx, f.session.ID)
	if err != nil || len(events) != 1 || events[0].Type != "session_expired" {
		t.Fatalf("expiry audit: %#v %v", events, err)
	}
	// Neither a stale writer nor activation can revive the terminal row.
	if err := store.SaveSession(ctx, f.session); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateSession(ctx, f.session.ID, f.session.ExamID, f.session.Subject, f.control.identityIssuer()); err == nil {
		t.Fatal("reactivated expired session")
	}
	state, _, _, _, err := f.control.refreshSession(ctx, f.session, true)
	if err != nil || state != "ended" {
		t.Fatalf("stale heartbeat resurrected session: %s %v", state, err)
	}
	if err := f.control.checkStudentEligibility(ctx, f.session.ExamID, f.session.Subject); err != nil {
		t.Fatalf("fresh attempt blocked: %v", err)
	}
	// New session for the same student can activate as usual.
	fresh := *f.session
	fresh.ID = randomToken(18)
	fresh.State = "authenticated"
	fresh.LastSeenAt = time.Now().Unix()
	if err := store.SaveSession(ctx, &fresh); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateSession(ctx, fresh.ID, fresh.ExamID, fresh.Subject, f.control.identityIssuer()); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresSessionExpiryConcurrentHeartbeat(t *testing.T) {
	f := newSplitFixture(t)
	ctx := context.Background()
	store := f.control.ExamStore
	now := time.Now().Truncate(time.Second)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE byod_sessions SET last_seen_at=$2 WHERE id=$1`, f.session.ID, now); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		events, err := store.EndIdleSessions(ctx, f.session.ExamID, f.session.ID, now, 20)
		if err == nil && len(events) != 0 {
			err = fmt.Errorf("fresh heartbeat lost race")
		}
		done <- err
	}()
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	x, err := store.GetSession(ctx, f.session.ID)
	if err != nil || x.State != "active" {
		t.Fatal("fresh session was expired", err)
	}
	if events, err := store.EndIdleSessions(ctx, f.session.ExamID, f.session.ID, now.Add(301*time.Second), 300); err != nil || len(events) != 1 {
		t.Fatal("missing terminal transition", err)
	}
	if touched, err := store.TouchActiveSession(ctx, f.session.ID, now.Unix()+302, 300); err != nil || touched {
		t.Fatal("ended session revived by data plane", err)
	}
}
