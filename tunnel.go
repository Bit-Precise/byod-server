package byodserver

// The BYOD tunnel is deliberately a small, versioned application protocol.
// It borrows the Xray/VLESS shape (authenticate and select an endpoint before
// forwarding a stream), but it is not VLESS or Trojan and does not expose an
// arbitrary destination chosen by the browser.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	tunnelMagic         = "BYOD"
	tunnelVersion       = byte(1)
	tunnelAuthFrame     = byte(1)
	tunnelAckFrame      = byte(2)
	tunnelNonceSize     = 16
	tunnelProofSize     = 32
	maxTunnelTicket     = 256
	maxTunnelEndpoint   = 128
	tunnelTicketTTL     = 30 * time.Second
	tunnelHandshakeTime = 5 * time.Second
)

var (
	errTunnelMalformed = errors.New("malformed tunnel authentication frame")
	errTunnelDenied    = errors.New("tunnel authentication denied")
)

// tunnelTicket is kept in memory when no database is configured. With a
// database, the same fields are persisted in byod_tunnel_tickets and the
// database operation atomically consumes a ticket.
type tunnelTicket struct {
	SessionID  string
	ExamID     string
	EndpointID string
	ExpiresAt  time.Time
	Used       bool
}

// TunnelAuth is the wire-level authentication data sent before the inner TLS
// stream. Ticket is opaque to clients; EndpointID is checked against the
// server-side session binding and is never used as a dial target by itself.
type TunnelAuth struct {
	Ticket     string
	EndpointID string
	Nonce      []byte
	Proof      []byte
}

// TunnelTicketInfo is returned only internally after a ticket has been
// validated. Binary-preface clients consume a ticket once; HTTP CONNECT
// clients may reuse it during its short lifetime because Chromium opens
// several proxy sockets for one page.
type TunnelTicketInfo struct {
	SessionID  string
	ExamID     string
	EndpointID string
	ExpiresAt  time.Time
}

// BuildTunnelAuth encodes the client preface used by the Chromium network
// layer. It is exported so the protocol can be tested by an independent
// client implementation without exposing server state.
func BuildTunnelAuth(ticket, endpointID string, nonce []byte) ([]byte, error) {
	if ticket == "" || len(ticket) > maxTunnelTicket || endpointID == "" || len(endpointID) > maxTunnelEndpoint || len(nonce) != tunnelNonceSize {
		return nil, errTunnelMalformed
	}
	proof := tunnelProof(ticket, endpointID, nonce)
	var frame bytes.Buffer
	frame.WriteString(tunnelMagic)
	frame.WriteByte(tunnelVersion)
	frame.WriteByte(tunnelAuthFrame)
	_ = binary.Write(&frame, binary.BigEndian, uint16(len(ticket)))
	_ = binary.Write(&frame, binary.BigEndian, uint16(len(endpointID)))
	frame.WriteByte(byte(len(nonce)))
	frame.WriteByte(byte(len(proof)))
	frame.WriteString(ticket)
	frame.WriteString(endpointID)
	frame.Write(nonce)
	frame.Write(proof)
	return frame.Bytes(), nil
}

func tunnelProof(ticket, endpointID string, nonce []byte) []byte {
	mac := hmac.New(sha256.New, []byte(ticket))
	mac.Write([]byte(tunnelMagic))
	mac.Write([]byte{tunnelVersion})
	mac.Write([]byte(endpointID))
	mac.Write(nonce)
	return mac.Sum(nil)
}

func readTunnelAuth(r io.Reader) (*TunnelAuth, error) {
	header := make([]byte, 12)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, errTunnelMalformed
	}
	if string(header[:4]) != tunnelMagic || header[4] != tunnelVersion || header[5] != tunnelAuthFrame {
		return nil, errTunnelMalformed
	}
	ticketLen := int(binary.BigEndian.Uint16(header[6:8]))
	endpointLen := int(binary.BigEndian.Uint16(header[8:10]))
	nonceLen := int(header[10])
	proofLen := int(header[11])
	if ticketLen == 0 || ticketLen > maxTunnelTicket || endpointLen == 0 || endpointLen > maxTunnelEndpoint || nonceLen != tunnelNonceSize || proofLen != tunnelProofSize {
		return nil, errTunnelMalformed
	}
	data := make([]byte, ticketLen+endpointLen+nonceLen+proofLen)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, errTunnelMalformed
	}
	offset := 0
	auth := &TunnelAuth{}
	auth.Ticket = string(data[offset : offset+ticketLen])
	offset += ticketLen
	auth.EndpointID = string(data[offset : offset+endpointLen])
	offset += endpointLen
	auth.Nonce = append([]byte(nil), data[offset:offset+nonceLen]...)
	offset += nonceLen
	auth.Proof = append([]byte(nil), data[offset:offset+proofLen]...)
	return auth, nil
}

func writeTunnelAck(w io.Writer, code byte) error {
	_, err := w.Write([]byte{tunnelMagic[0], tunnelMagic[1], tunnelMagic[2], tunnelMagic[3], tunnelVersion, tunnelAckFrame, code})
	return err
}

func hashTunnelTicket(ticket string) string {
	digest := sha256.Sum256([]byte(ticket))
	return hex.EncodeToString(digest[:])
}

// IssueTunnelTicket creates a short-lived ticket for one active exam session.
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

	ticket := randomToken(32)
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

func (s *Service) consumeTunnelTicket(ctx context.Context, auth *TunnelAuth) (TunnelTicketInfo, error) {
	return s.consumeTunnelTicketMode(ctx, auth, false)
}

func (s *Service) consumeTunnelTicketMode(ctx context.Context, auth *TunnelAuth, reusable bool) (TunnelTicketInfo, error) {
	if auth == nil || len(auth.Nonce) != tunnelNonceSize || len(auth.Proof) != tunnelProofSize {
		return TunnelTicketInfo{}, errTunnelMalformed
	}
	expected := tunnelProof(auth.Ticket, auth.EndpointID, auth.Nonce)
	if !hmac.Equal(expected, auth.Proof) {
		return TunnelTicketInfo{}, errTunnelDenied
	}
	hash := hashTunnelTicket(auth.Ticket)
	if s.ExamStore != nil {
		var stored StoredTunnelTicket
		var ok bool
		var err error
		if reusable {
			stored, ok, err = s.ExamStore.LookupTunnelTicket(ctx, []byte(hash), auth.EndpointID, time.Now())
		} else {
			stored, ok, err = s.ExamStore.ConsumeTunnelTicket(ctx, []byte(hash), auth.EndpointID, time.Now())
		}
		if err != nil {
			return TunnelTicketInfo{}, err
		}
		if !ok {
			return TunnelTicketInfo{}, errTunnelDenied
		}
		return TunnelTicketInfo{SessionID: stored.SessionID, ExamID: stored.ExamID, EndpointID: stored.EndpointID, ExpiresAt: stored.ExpiresAt}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.tunnelTickets[hash]
	if record == nil || record.Used || time.Now().After(record.ExpiresAt) || record.EndpointID != auth.EndpointID {
		return TunnelTicketInfo{}, errTunnelDenied
	}
	session := s.sessions[record.SessionID]
	if session == nil || session.State != "active" {
		return TunnelTicketInfo{}, errTunnelDenied
	}
	if !reusable {
		record.Used = true
	}
	return TunnelTicketInfo{SessionID: record.SessionID, ExamID: record.ExamID, EndpointID: record.EndpointID, ExpiresAt: record.ExpiresAt}, nil
}

func (s *Service) revokeTunnelTicketsLocked(sessionID string) {
	for hash, ticket := range s.tunnelTickets {
		if ticket.SessionID == sessionID {
			delete(s.tunnelTickets, hash)
		}
	}
}

// readConnectTunnelAuth parses a bounded HTTP CONNECT request. The request
// target is deliberately ignored: the endpoint is selected by the signed
// session credential, never by an arbitrary host supplied by the browser.
// This lets Chromium use its normal HTTPS proxy socket while preserving the
// opaque source-site TLS stream after the 200 response.
func readConnectTunnelAuth(r *bufio.Reader) (*TunnelAuth, error) {
	line, err := r.ReadString('\n')
	if err != nil || len(line) > 4096 {
		return nil, errTunnelMalformed
	}
	requestLine := strings.Fields(strings.TrimRight(line, "\r\n"))
	if len(requestLine) != 3 || requestLine[0] != "CONNECT" ||
		requestLine[1] == "" || requestLine[2] != "HTTP/1.1" {
		return nil, errTunnelMalformed
	}
	var total int
	headers := make(map[string]string)
	for {
		line, err = r.ReadString('\n')
		if err != nil || len(line) > 4096 {
			return nil, errTunnelMalformed
		}
		total += len(line)
		if total > 16*1024 {
			return nil, errTunnelMalformed
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return nil, errTunnelMalformed
		}
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.TrimSpace(parts[1])
		switch name {
		case "x-byod-ticket", "x-byod-endpoint", "x-byod-nonce", "x-byod-proof":
			if _, duplicate := headers[name]; duplicate {
				return nil, errTunnelMalformed
			}
			headers[name] = value
		}
	}
	ticket := headers["x-byod-ticket"]
	endpointID := headers["x-byod-endpoint"]
	if ticket == "" || len(ticket) > maxTunnelTicket || endpointID == "" ||
		len(endpointID) > maxTunnelEndpoint {
		return nil, errTunnelMalformed
	}
	nonce, err := hex.DecodeString(headers["x-byod-nonce"])
	if err != nil || len(nonce) != tunnelNonceSize {
		return nil, errTunnelMalformed
	}
	proof, err := hex.DecodeString(headers["x-byod-proof"])
	if err != nil || len(proof) != tunnelProofSize {
		return nil, errTunnelMalformed
	}
	return &TunnelAuth{Ticket: ticket, EndpointID: endpointID, Nonce: nonce, Proof: proof}, nil
}

// ServeTunnel handles one authenticated raw TCP stream. The stream after the
// preface is opaque to BYOD: it is the browser's TLS connection to the source.
func (s *Service) ServeTunnel(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(tunnelHandshakeTime))
	// Accept both the binary BYOD preface and an HTTP CONNECT request. The
	// latter is what Chromium's built-in HTTPS proxy stack emits; bytes after
	// the successful handshake are still opaque source TLS.
	peek := make([]byte, 8)
	if _, err := io.ReadFull(conn, peek); err != nil {
		_ = writeTunnelAck(conn, 1)
		return
	}
	connectMode := bytes.HasPrefix(peek, []byte("CONNECT "))
	var auth *TunnelAuth
	var err error
	if connectMode {
		auth, err = readConnectTunnelAuth(bufio.NewReader(io.MultiReader(bytes.NewReader(peek), conn)))
	} else {
		auth, err = readTunnelAuth(io.MultiReader(bytes.NewReader(peek), conn))
	}
	if err != nil {
		if connectMode {
			_, _ = io.WriteString(conn, "HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n")
		} else {
			_ = writeTunnelAck(conn, 1)
		}
		return
	}
	info, err := s.consumeTunnelTicketMode(ctx, auth, connectMode)
	if err != nil {
		if connectMode {
			_, _ = io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n")
		} else {
			_ = writeTunnelAck(conn, 2)
		}
		return
	}
	upstream, err := s.upstreamForExam(ctx, info.ExamID)
	address, addressErr := parseTunnelUpstream(upstream)
	if err != nil || addressErr != nil {
		if connectMode {
			_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n")
		} else {
			_ = writeTunnelAck(conn, 3)
		}
		return
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	upstreamConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		if connectMode {
			_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n")
		} else {
			_ = writeTunnelAck(conn, 3)
		}
		return
	}
	defer upstreamConn.Close()
	_ = conn.SetReadDeadline(time.Time{})
	if connectMode {
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\nProxy-Agent: byod-server\r\n\r\n"); err != nil {
			return
		}
	} else if err := writeTunnelAck(conn, 0); err != nil {
		return
	}

	// A revoked/suspended session must close already-established tunnels too.
	stop := make(chan struct{})
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			close(stop)
			_ = conn.Close()
			_ = upstreamConn.Close()
		})
	}
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if !s.tunnelSessionActive(info.SessionID) {
					closeBoth()
					return
				}
			case <-stop:
				return
			}
		}
	}()

	copyDone := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstreamConn, conn)
		closeBoth()
		copyDone <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, upstreamConn)
		closeBoth()
		copyDone <- struct{}{}
	}()
	<-copyDone
}

func (s *Service) tunnelSessionActive(sessionID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if session := s.sessions[sessionID]; session != nil {
		return session.State == "active"
	}
	return false
}

// TunnelAuthProof is useful to independent client implementations and tests.
func TunnelAuthProof(ticket, endpointID string, nonce []byte) []byte {
	return tunnelProof(ticket, endpointID, nonce)
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
