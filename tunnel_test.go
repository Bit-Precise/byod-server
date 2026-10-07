package byodserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestVLESSCredentialReusableWhileActive(t *testing.T) {
	s, _ := NewService("https://exam.cs.ac.cn", "https://example.test", []byte("test-secret"))
	session := activateTestSession(t, s, "course-101")
	uuid, _, err := s.IssueTunnelTicket(context.Background(), session["session_id"])
	if err != nil || !validExamUUID(uuid) {
		t.Fatalf("invalid UUID: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.lookupVLESSCredential(context.Background(), uuid); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVLESSCredentialExpiry(t *testing.T) {
	s, _ := NewService("https://exam.cs.ac.cn", "https://example.test", []byte("test-secret"))
	session := activateTestSession(t, s, "course-101")
	uuid, _, err := s.IssueTunnelTicket(context.Background(), session["session_id"])
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.tunnelTickets[hashTunnelTicket(uuid)].ExpiresAt = time.Now().Add(-time.Second)
	s.mu.Unlock()
	if _, err := s.lookupVLESSCredential(context.Background(), uuid); err == nil {
		t.Fatal("expired VLESS UUID accepted")
	}
}
func TestTunnelTicketRevokedWhenSessionSuspends(t *testing.T) {
	service, err := NewService("https://exam.cs.ac.cn", "https://example.test", []byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	session := activateTestSession(t, service, "course-101")
	ticket, _, err := service.IssueTunnelTicket(context.Background(), session["session_id"])
	if err != nil {
		t.Fatal(err)
	}
	violation := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+session["session_id"]+"/violations", strings.NewReader(`{"type":"background"}`))
	request.Header.Set("Authorization", "Bearer "+session["browser_token"])
	service.ServeHTTP(violation, request)
	if violation.Code != http.StatusOK || !strings.Contains(violation.Body.String(), `"state":"suspended"`) {
		t.Fatalf("suspension failed: %d %s", violation.Code, violation.Body.String())
	}
	if _, err := service.lookupVLESSCredential(context.Background(), ticket); err == nil {
		t.Fatal("suspended session retained a reusable tunnel ticket")
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

func TestTunnelAddressRequiresExplicitTunnelHost(t *testing.T) {
	service, err := NewService("https://exam.cs.ac.cn", "https://cs101.gbu.edu.cn", []byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.tunnelAddress(context.Background(), "course-101", "cs101.gbu.edu.cn:443"); err == nil {
		t.Fatal("Base URL was implicitly allowlisted with empty tunnel_hosts")
	}
	service.PolicyOverrides = map[string]map[string]any{
		"course-101": {"tunnel_hosts": []string{"cs101.gbu.edu.cn", "minio.cs101.gbu.edu.cn"}},
	}
	if got, err := service.tunnelAddress(context.Background(), "course-101", "minio.cs101.gbu.edu.cn:443"); err != nil || got != "minio.cs101.gbu.edu.cn:443" {
		t.Fatalf("allowlisted tunnel address = %q, %v", got, err)
	}
	if _, err := service.tunnelAddress(context.Background(), "course-101", "other.example:443"); err == nil {
		t.Fatal("unlisted tunnel host was accepted")
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
