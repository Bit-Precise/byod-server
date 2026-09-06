package byodserver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestReadConnectTunnelAuth(t *testing.T) {
	ticket := "ticket-value"
	endpointID := "course-101"
	nonce := bytes.Repeat([]byte{0x7a}, tunnelNonceSize)
	proof := TunnelAuthProof(ticket, endpointID, nonce)
	request := "CONNECT source.example:443 HTTP/1.1\r\n" +
		"Host: source.example:443\r\n" +
		"X-BYOD-Ticket: " + ticket + "\r\n" +
		"x-byod-endpoint: " + endpointID + "\r\n" +
		"X-BYOD-Nonce: " + fmt.Sprintf("%x", nonce) + "\r\n" +
		"X-BYOD-Proof: " + fmt.Sprintf("%x", proof) + "\r\n\r\n"

	auth, err := readConnectTunnelAuth(bufio.NewReader(strings.NewReader(request)))
	if err != nil {
		t.Fatal(err)
	}
	if auth.Ticket != ticket || auth.EndpointID != endpointID ||
		!bytes.Equal(auth.Nonce, nonce) || !bytes.Equal(auth.Proof, proof) {
		t.Fatalf("unexpected CONNECT auth: %#v", auth)
	}
}

func TestReadConnectTunnelAuthRejectsMalformedRequests(t *testing.T) {
	validNonce := strings.Repeat("00", tunnelNonceSize)
	validProof := strings.Repeat("00", tunnelProofSize)
	tests := map[string]string{
		"wrong method":         "GET source.example:443 HTTP/1.1\r\n\r\n",
		"missing target":       "CONNECT HTTP/1.1\r\n\r\n",
		"missing ticket":       "CONNECT source.example:443 HTTP/1.1\r\nX-BYOD-Endpoint: exam\r\nX-BYOD-Nonce: " + validNonce + "\r\nX-BYOD-Proof: " + validProof + "\r\n\r\n",
		"short nonce":          "CONNECT source.example:443 HTTP/1.1\r\nX-BYOD-Ticket: ticket\r\nX-BYOD-Endpoint: exam\r\nX-BYOD-Nonce: 00\r\nX-BYOD-Proof: " + validProof + "\r\n\r\n",
		"duplicate credential": "CONNECT source.example:443 HTTP/1.1\r\nX-BYOD-Ticket: first\r\nX-BYOD-Ticket: second\r\nX-BYOD-Endpoint: exam\r\nX-BYOD-Nonce: " + validNonce + "\r\nX-BYOD-Proof: " + validProof + "\r\n\r\n",
	}
	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := readConnectTunnelAuth(bufio.NewReader(strings.NewReader(request))); err == nil {
				t.Fatal("malformed CONNECT request was accepted")
			}
		})
	}
}

func TestConnectTunnelTicketCanBeReusedWithinTTL(t *testing.T) {
	service, err := NewService("https://exam.cs.ac.cn", "https://example.test", []byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	session := activateTestSession(t, service, "course-101")
	ticket, info, err := service.IssueTunnelTicket(context.Background(), session["session_id"])
	if err != nil {
		t.Fatal(err)
	}
	nonce := bytes.Repeat([]byte{0x55}, tunnelNonceSize)
	auth := &TunnelAuth{Ticket: ticket, EndpointID: info.EndpointID, Nonce: nonce,
		Proof: TunnelAuthProof(ticket, info.EndpointID, nonce)}
	for i := 0; i < 2; i++ {
		validated, err := service.consumeTunnelTicketMode(context.Background(), auth, true)
		if err != nil || validated.SessionID != session["session_id"] {
			t.Fatalf("CONNECT validation %d failed: %#v %v", i, validated, err)
		}
	}
	if remaining := time.Until(info.ExpiresAt); remaining < 7*time.Hour {
		t.Fatalf("ticket expires too soon for an exam window: %s", remaining)
	}
}

func TestTunnelTicketRevokedWhenSessionSuspends(t *testing.T) {
	service, err := NewService("https://exam.cs.ac.cn", "https://example.test", []byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	session := activateTestSession(t, service, "course-101")
	ticket, info, err := service.IssueTunnelTicket(context.Background(), session["session_id"])
	if err != nil {
		t.Fatal(err)
	}
	nonce := bytes.Repeat([]byte{0x66}, tunnelNonceSize)
	auth := &TunnelAuth{Ticket: ticket, EndpointID: info.EndpointID, Nonce: nonce,
		Proof: TunnelAuthProof(ticket, info.EndpointID, nonce)}
	violation := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+session["session_id"]+"/violations", strings.NewReader(`{"type":"background"}`))
	request.Header.Set("Authorization", "Bearer "+session["browser_token"])
	service.ServeHTTP(violation, request)
	if violation.Code != http.StatusOK || !strings.Contains(violation.Body.String(), `"state":"suspended"`) {
		t.Fatalf("suspension failed: %d %s", violation.Code, violation.Body.String())
	}
	if _, err := service.consumeTunnelTicketMode(context.Background(), auth, true); err == nil {
		t.Fatal("suspended session retained a reusable tunnel ticket")
	}
}

func TestTunnelAuthRoundTrip(t *testing.T) {
	nonce := bytes.Repeat([]byte{0x42}, tunnelNonceSize)
	frame, err := BuildTunnelAuth("ticket-value", "course-101", nonce)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := readTunnelAuth(bytes.NewReader(frame))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Ticket != "ticket-value" || parsed.EndpointID != "course-101" || !bytes.Equal(parsed.Nonce, nonce) {
		t.Fatalf("unexpected auth: %#v", parsed)
	}
	if !bytes.Equal(parsed.Proof, TunnelAuthProof(parsed.Ticket, parsed.EndpointID, parsed.Nonce)) {
		t.Fatal("proof did not round-trip")
	}
	if _, err := BuildTunnelAuth("ticket-value", "course-101", []byte{1}); err == nil {
		t.Fatal("short nonce was accepted")
	}
}

func TestTunnelTicketIsSingleUse(t *testing.T) {
	service, err := NewService("https://exam.cs.ac.cn", "https://example.test", []byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	session := activateTestSession(t, service, "course-101")
	ticket, info, err := service.IssueTunnelTicket(context.Background(), session["session_id"])
	if err != nil {
		t.Fatal(err)
	}
	if info.EndpointID != "course-101" || ticket == "" {
		t.Fatalf("unexpected ticket: %q %#v", ticket, info)
	}
	nonce := bytes.Repeat([]byte{0x11}, tunnelNonceSize)
	frame, err := BuildTunnelAuth(ticket, info.EndpointID, nonce)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := readTunnelAuth(bytes.NewReader(frame))
	if err != nil {
		t.Fatal(err)
	}
	consumed, err := service.consumeTunnelTicket(context.Background(), auth)
	if err != nil || consumed.SessionID != session["session_id"] {
		t.Fatalf("consume failed: %#v %v", consumed, err)
	}
	if _, err := service.consumeTunnelTicket(context.Background(), auth); err == nil {
		t.Fatal("ticket was reusable")
	}
}

func TestTunnelTicketEndpointRequiresActiveSession(t *testing.T) {
	service, err := NewService("https://exam.cs.ac.cn", "https://example.test", []byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	create := httptest.NewRecorder()
	service.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"exam_id":"course-101"}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", create.Code, create.Body.String())
	}
	var session map[string]string
	if err := decodeJSON(create.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	ticketRequest := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+session["session_id"]+"/tunnel-ticket", nil)
	ticketRequest.Header.Set("Authorization", "Bearer "+session["browser_token"])
	ticketResponse := httptest.NewRecorder()
	service.ServeHTTP(ticketResponse, ticketRequest)
	if ticketResponse.Code != http.StatusConflict {
		t.Fatalf("inactive session ticket status: %d %s", ticketResponse.Code, ticketResponse.Body.String())
	}
}

func TestServeTunnelForwardsInnerTLS(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/answer" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		w.Header().Set("X-Tunnel-Test", "ok")
		_, _ = io.WriteString(w, "accepted")
	}))
	defer upstream.Close()
	service, err := NewService("https://exam.cs.ac.cn", upstream.URL, []byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	session := activateTestSession(t, service, "course-101")
	ticket, info, err := service.IssueTunnelTicket(context.Background(), session["session_id"])
	if err != nil {
		t.Fatal(err)
	}
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	go service.ServeTunnel(context.Background(), serverConn)

	nonce := bytes.Repeat([]byte{0x33}, tunnelNonceSize)
	frame, err := BuildTunnelAuth(ticket, info.EndpointID, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clientConn.Write(frame); err != nil {
		t.Fatal(err)
	}
	ack := make([]byte, 7)
	if _, err := io.ReadFull(clientConn, ack); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ack[:6], []byte{'B', 'Y', 'O', 'D', tunnelVersion, tunnelAckFrame}) || ack[6] != 0 {
		t.Fatalf("unexpected tunnel ack: %v", ack)
	}

	inner := tls.Client(clientConn, &tls.Config{InsecureSkipVerify: true}) // test server certificate is ephemeral
	if err := inner.Handshake(); err != nil {
		t.Fatal(err)
	}
	request := "GET /answer HTTP/1.1\r\nHost: source.example\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(inner, request); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(inner)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(response, []byte("200 OK")) || !bytes.Contains(response, []byte("accepted")) {
		t.Fatalf("inner TLS was not forwarded: %q", response)
	}
	_ = inner.Close()
	// Keep the test deterministic when running with a slow race detector.
	service.mu.Lock()
	service.sessions[session["session_id"]].LastSeenAt = time.Now().Unix()
	service.mu.Unlock()
}

func TestParseTunnelUpstream(t *testing.T) {
	if got, err := parseTunnelUpstream(mustURL("https://example.test")); err != nil || got != "example.test:443" {
		t.Fatalf("default port: %q %v", got, err)
	}
	if got, err := parseTunnelUpstream(mustURL("https://example.test:8443/path")); err != nil || got != "example.test:8443" {
		t.Fatalf("explicit port: %q %v", got, err)
	}
	if _, err := parseTunnelUpstream(mustURL("http://example.test")); err == nil {
		t.Fatal("HTTP upstream accepted")
	}
}

func mustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

func decodeJSON(data []byte, dst any) error {
	return json.Unmarshal(data, dst)
}
