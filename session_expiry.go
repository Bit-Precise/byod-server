package byodserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

const defaultHeartbeatSeconds int64 = 15
const defaultMaxIdleSeconds int64 = 300
const sessionExpiryInterval = 10 * time.Second

func sessionLimitsFromDocument(document map[string]any) (heartbeat, maxIdle int64) {
	heartbeat, maxIdle = defaultHeartbeatSeconds, defaultMaxIdleSeconds
	session, _ := document["session"].(map[string]any)
	if value, ok := policyInt(session["heartbeat_seconds"]); ok && value >= 5 && value <= 300 {
		heartbeat = value
	}
	if value, ok := policyInt(session["max_idle_seconds"]); ok && value >= heartbeat && value <= 3600 {
		maxIdle = value
	}
	return
}

func (s *Service) effectiveSessionLimits(examID string, stored map[string]any) (int64, int64) {
	document := map[string]any{"session": map[string]any{"heartbeat_seconds": defaultHeartbeatSeconds, "max_idle_seconds": defaultMaxIdleSeconds}}
	for _, policy := range []map[string]any{s.PolicyOverrides[examID], stored} {
		if value, ok := policy["session"]; ok {
			// Copy nested maps: merging a DB override must not mutate config.
			if fields, ok := value.(map[string]any); ok {
				copy := make(map[string]any, len(fields))
				for key, value := range fields {
					copy[key] = value
				}
				value = copy
			}
			mergePolicyValue(document, "session", value)
		}
	}
	return sessionLimitsFromDocument(document)
}

func (s *Service) sessionLimitsContext(ctx context.Context, examID string) (int64, int64, error) {
	var stored map[string]any
	if s.ExamStore != nil {
		var err error
		stored, err = s.ExamStore.Policy(ctx, examID)
		if err != nil {
			return 0, 0, err
		}
	}
	heartbeat, idle := s.effectiveSessionLimits(examID, stored)
	return heartbeat, idle, nil
}

func expirableSession(state string) bool {
	switch state {
	case "pending", "authenticating", "authenticated", "active", "suspended":
		return true
	}
	return false
}

func sessionExpiredEvent(id, attempt, browser string, now time.Time, maxIdle int64) ExamEvent {
	return ExamEvent{ID: randomToken(12), SessionID: id, AttemptID: attempt, BrowserSessionID: browser,
		Type: "session_expired", Severity: "info", Details: fmt.Sprintf("heartbeat_timeout; max_idle_seconds=%d", maxIdle), OccurredAt: now.Unix()}
}

// EndIdleSessions atomically ends only stale sessions, revokes their credentials
// and records the reason. This is NOT submission: no completion slot is claimed.
// PostgreSQL rechecks the predicate after waiting for a concurrent heartbeat.
func (s *PostgresStore) EndIdleSessions(ctx context.Context, examID, sessionID string, now time.Time, maxIdle int64) ([]ExamEvent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `UPDATE byod_sessions SET state='ended'
WHERE exam_id=$1 AND ($2='' OR id=$2)
AND state IN ('pending','authenticating','authenticated','active','suspended')
AND last_seen_at<$3
RETURNING id,attempt_id,browser_session_id`, examID, sessionID, now.Add(-time.Duration(maxIdle)*time.Second))
	if err != nil {
		return nil, err
	}
	var events []ExamEvent
	for rows.Next() {
		var id, attempt, browser string
		if err := rows.Scan(&id, &attempt, &browser); err != nil {
			rows.Close()
			return nil, err
		}
		events = append(events, sessionExpiredEvent(id, attempt, browser, now, maxIdle))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		if _, err = tx.ExecContext(ctx, `UPDATE byod_tunnel_tickets SET used_at=$2 WHERE session_id=$1 AND used_at IS NULL`, event.SessionID, now); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO byod_events(id,session_id,attempt_id,browser_session_id,type,severity,details,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, event.ID, event.SessionID, event.AttemptID, event.BrowserSessionID, event.Type, event.Severity, event.Details, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return events, nil
}

// Caller holds s.mu. Never use appendEvent here: it saves a cached Session,
// whereas the conditional database transition above is authoritative.
func (s *Service) endIdleSessionsLocked(ctx context.Context, examID, sessionID string, now time.Time, maxIdle int64) (int, error) {
	var events []ExamEvent
	if s.ExamStore != nil {
		var err error
		events, err = s.ExamStore.EndIdleSessions(ctx, examID, sessionID, now, maxIdle)
		if err != nil {
			return 0, err
		}
	} else {
		for _, session := range s.sessions {
			if session.ExamID == examID && (sessionID == "" || session.ID == sessionID) && expirableSession(session.State) && time.Unix(session.LastSeenAt, 0).Before(now.Add(-time.Duration(maxIdle)*time.Second)) {
				events = append(events, sessionExpiredEvent(session.ID, session.AttemptID, session.BrowserSessionID, now, maxIdle))
			}
		}
	}
	for _, event := range events {
		if session := s.sessions[event.SessionID]; session != nil {
			session.State = "ended"
		}
		s.revokeTunnelTicketsLocked(event.SessionID)
		s.events[event.SessionID] = append(s.events[event.SessionID], event)
	}
	return len(events), nil
}

// ExpireIdleSessions also sees historical rows absent from the process cache.
// Only the control role advances the state machine; data only updates liveness.
func (s *Service) ExpireIdleSessions(ctx context.Context, now time.Time) (int, error) {
	if s.Role == "data" {
		return 0, nil
	}
	now = now.Truncate(time.Second)
	policies := make(map[string]map[string]any)
	if s.ExamStore != nil {
		rows, err := s.ExamStore.db.QueryContext(ctx, `SELECT DISTINCT s.exam_id,COALESCE(e.policy_json,'{}'::jsonb) FROM byod_sessions s LEFT JOIN byod_exams e ON e.exam_id=s.exam_id WHERE s.state IN ('pending','authenticating','authenticated','active','suspended')`)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var id string
			var raw []byte
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return 0, err
			}
			var policy map[string]any
			if err := json.Unmarshal(raw, &policy); err != nil {
				rows.Close()
				return 0, err
			}
			policies[id] = policy
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return 0, err
		}
	} else {
		s.mu.RLock()
		for _, session := range s.sessions {
			policies[session.ExamID] = nil
		}
		s.mu.RUnlock()
	}
	count := 0
	for examID, policy := range policies {
		_, idle := s.effectiveSessionLimits(examID, policy)
		s.mu.Lock()
		n, err := s.endIdleSessionsLocked(ctx, examID, "", now, idle)
		s.mu.Unlock()
		count += n
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

func (s *Service) RunSessionExpiry(ctx context.Context) {
	s.runSessionExpiry(ctx, sessionExpiryInterval)
}

func (s *Service) runSessionExpiry(ctx context.Context, interval time.Duration) {
	if s.Role == "data" {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		check, cancel := context.WithTimeout(ctx, 30*time.Second)
		count, err := s.ExpireIdleSessions(check, time.Now())
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Error("session_expiry_failed", "error", err)
		}
		if count > 0 {
			slog.Info("idle_sessions_ended", "count", count)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Synchronize durable state, expire before accepting a late heartbeat, and
// update liveness without letting stale control state overwrite data liveness.
func (s *Service) refreshSession(ctx context.Context, session *Session, heartbeat bool) (state string, lastSeen int64, interval, maxIdle int64, err error) {
	interval, maxIdle, err = s.sessionLimitsContext(ctx, session.ExamID)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Truncate(time.Second)
	if _, err = s.endIdleSessionsLocked(ctx, session.ExamID, session.ID, now, maxIdle); err != nil {
		return
	}
	if s.ExamStore != nil {
		if heartbeat {
			_, err = s.ExamStore.db.ExecContext(ctx, `UPDATE byod_sessions SET last_seen_at=GREATEST(last_seen_at,$2) WHERE id=$1 AND state IN ('pending','authenticating','authenticated','active','suspended')`, session.ID, now)
			if err != nil {
				return
			}
		}
		var stored *StoredSession
		stored, err = s.ExamStore.GetSession(ctx, session.ID)
		if err != nil {
			return
		}
		session.State, session.LastSeenAt = stored.State, stored.LastSeenAt.Unix()
	} else if heartbeat && expirableSession(session.State) {
		session.LastSeenAt = now.Unix()
	}
	state, lastSeen = session.State, session.LastSeenAt
	return
}
