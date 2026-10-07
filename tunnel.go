package byodserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const tunnelTicketTTL = 8 * time.Hour

var (
	errTunnelMalformed = errors.New("malformed VLESS request")
	errTunnelDenied    = errors.New("VLESS authentication denied")
)

type tunnelTicket struct {
	SessionID  string
	ExamID     string
	EndpointID string
	ExpiresAt  time.Time
}

type TunnelTicketInfo struct {
	SessionID  string
	ExamID     string
	EndpointID string
	ExpiresAt  time.Time
}

func hashTunnelTicket(ticket string) string {
	digest := sha256.Sum256([]byte(ticket))
	return hex.EncodeToString(digest[:])
}

// IssueTunnelTicket creates a ticket for one active exam session. The ticket
// remains valid for the exam window while every lookup still requires that
// the session is active.
func (s *Service) IssueTunnelTicket(ctx context.Context, sessionID string) (string, TunnelTicketInfo, error) {
	now := time.Now()
	s.mu.RLock()
	session := s.sessions[sessionID]
	if session == nil || session.State != "active" {
		s.mu.RUnlock()
		return "", TunnelTicketInfo{}, errTunnelDenied
	}
	info := TunnelTicketInfo{SessionID: session.ID, ExamID: session.ExamID, EndpointID: session.ExamID, ExpiresAt: now.Add(tunnelTicketTTL)}
	s.mu.RUnlock()
	if _, err := s.examWindow(ctx, info.ExamID, gateActive); err != nil {
		return "", TunnelTicketInfo{}, err
	}

	ticket, err := newExamUUID()
	if err != nil {
		return "", TunnelTicketInfo{}, err
	}
	record := &tunnelTicket{SessionID: info.SessionID, ExamID: info.ExamID, EndpointID: info.EndpointID, ExpiresAt: info.ExpiresAt}
	hash := hashTunnelTicket(ticket)
	s.mu.Lock()
	if s.tunnelTickets == nil {
		s.tunnelTickets = make(map[string]*tunnelTicket)
	}
	s.tunnelTickets[hash] = record
	s.mu.Unlock()
	if s.ExamStore != nil {
		if err := s.ExamStore.CreateTunnelTicket(ctx, []byte(hash), record.SessionID, record.ExamID, record.EndpointID, record.ExpiresAt); err != nil {
			s.mu.Lock()
			delete(s.tunnelTickets, hash)
			s.mu.Unlock()
			return "", TunnelTicketInfo{}, err
		}
	}
	return ticket, info, nil
}

func (s *Service) revokeTunnelTicketsLocked(sessionID string) {
	for hash, ticket := range s.tunnelTickets {
		if ticket.SessionID == sessionID {
			delete(s.tunnelTickets, hash)
		}
	}
}

// Only explicitly allowlisted destinations are dialed; no Base URL fallback.
func (s *Service) tunnelAddress(ctx context.Context, examID, target string) (string, error) {
	host, port, err := net.SplitHostPort(target)
	host = strings.ToLower(host)
	p, portErr := strconv.Atoi(port)
	if err != nil || !validTunnelHost(host) || portErr != nil || p < 1 || p > 65535 || strconv.Itoa(p) != port {
		return "", errTunnelDenied
	}
	// The data plane needs only the destination allowlist, not signed browser
	// policy or the control-plane signing key. Fail closed on database errors.
	var value any
	if override := s.PolicyOverrides[examID]; override != nil {
		value = override["tunnel_hosts"]
	}
	if s.ExamStore != nil {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		document, err := s.ExamStore.Policy(ctx, examID)
		if err != nil {
			return "", err
		}
		if stored, ok := document["tunnel_hosts"]; ok {
			value = stored
		}
	}
	hosts, err := parseTunnelHosts(value)
	if err != nil {
		return "", errTunnelDenied
	}
	for _, allowed := range hosts {
		if host == allowed {
			return net.JoinHostPort(host, port), nil
		}
	}
	return "", errTunnelDenied
}

func (s *Service) tunnelSessionActive(sessionID string) bool {
	s.mu.RLock()
	session := s.sessions[sessionID]
	active := session != nil && session.State == "active"
	examID := ""
	if session != nil {
		examID = session.ExamID
	}
	s.mu.RUnlock()
	if !active {
		return false
	}
	if _, err := s.examWindow(context.Background(), examID, gateActive); err != nil {
		_ = s.endWithReason(context.Background(), session, "ends_at")
		return false
	}
	// The source page replaces the grips:// bootstrap document, so its
	// JavaScript heartbeat timer no longer exists. An authenticated tunnel is
	// itself the liveness signal: refresh last_seen_at while the stream is
	// alive, otherwise the regular max-idle suspension still applies.
	now := time.Now().Unix()
	s.mu.Lock()
	if live := s.sessions[sessionID]; live == nil || live.State != "active" {
		s.mu.Unlock()
		return false
	} else {
		live.LastSeenAt = now
		session = live
	}
	s.mu.Unlock()
	return true
}

func parseTunnelUpstream(u *url.URL) (string, error) {
	if u == nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("tunnel upstream must be an HTTPS URL without credentials or fragment")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}
