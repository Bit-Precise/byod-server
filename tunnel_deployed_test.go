package byodserver

// Opt-in deployment smoke. Uses only freshly created synthetic records, does
// not run migrations, and deletes its exact fixture IDs on completion.
import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDeployedXHTTP(t *testing.T) {
	dsn, endpoint := os.Getenv("BYOD_LIVE_SMOKE_DATABASE_URL"), os.Getenv("BYOD_LIVE_SMOKE_ENDPOINT")
	if dsn == "" || endpoint == "" {
		t.Skip("explicit live smoke configuration required")
	}
	if !strings.HasPrefix(endpoint, "https://") {
		t.Fatal("live smoke requires verified HTTPS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	store, err := OpenPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	echoHost := "byod-xhttp-smoke-20261007.byod.svc.cluster.local"
	sourceHost := "cs101.gbu.edu.cn"
	control, _ := NewService(strings.TrimSuffix(endpoint, "/"), "https://"+sourceHost, nil)
	control.IdentityIssuer, control.ExamStore, control.Role = "https://connect.cs.ac.cn", store, "control"
	exam, err := store.CreateExamNamed(ctx, "Temporary XHTTP deployment check", "xhttp-smoke-"+randomToken(8), "https://"+sourceHost, nil, nil, map[string]any{"tunnel_hosts": []string{echoHost, sourceHost}})
	if err != nil {
		t.Fatal(err)
	}
	var userID, sessionID string
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		// Exact IDs from this invocation only; never touch student data.
		if _, err := store.db.ExecContext(cleanup, `DELETE FROM byod_sessions WHERE id=$1 AND id<>''`, sessionID); err != nil {
			t.Error("cleanup session", err)
		}
		if err := store.DeleteExam(cleanup, exam.ID); err != nil {
			t.Error("cleanup exam", err)
		}
		if _, err := store.db.ExecContext(cleanup, `DELETE FROM byod_user_audit WHERE user_id=$1 AND actor_id=$1 AND user_id<>''`, userID); err != nil {
			t.Error("cleanup synthetic audit", err)
		}
		if _, err := store.db.ExecContext(cleanup, `DELETE FROM byod_users WHERE id=$1 AND id<>''`, userID); err != nil {
			t.Error("cleanup user", err)
		}
		t.Log("synthetic exam/session/user removed")
	}()
	if err := store.SetExamState(ctx, exam.ID, "active"); err != nil {
		t.Fatal(err)
	}
	user, err := store.ResolveIdentity(ctx, OIDCIdentity{Issuer: control.IdentityIssuer, Subject: "deployment-smoke-" + randomToken(16), Name: "Temporary XHTTP check"})
	if err != nil {
		t.Fatal(err)
	}
	userID = user.ID
	if err := store.SetParticipant(ctx, exam.ID, user.ID, true, user.ID); err != nil {
		t.Fatal(err)
	}
	session := &Session{ID: randomToken(18), ExamID: exam.ID, Subject: *user.Subject, State: "active", CreatedAt: time.Now().Unix(), LastSeenAt: time.Now().Unix()}
	sessionID = session.ID
	if err := store.SaveSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	control.sessions[session.ID] = session
	uuid, _, err := control.IssueTunnelTicket(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{ForceAttemptHTTP2: true}
	if address := os.Getenv("BYOD_LIVE_SMOKE_DIRECT_ADDRESS"); address != "" {
		transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, address)
		}
		t.Log("comparison: direct ingress dial, unchanged TLS hostname and certificate verification")
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	dial := func(target string) *xhttpTestConn {
		id, _ := newExamUUID()
		connectionCtx, closeConnection := context.WithCancel(ctx)
		x := &xhttpTestConn{client: client, base: strings.TrimSuffix(endpoint, "/") + xhttpPath + id, ctx: connectionCtx, cancel: closeConnection}
		r, _ := http.NewRequestWithContext(connectionCtx, "GET", x.base, nil)
		response, err := client.Do(r)
		if err != nil {
			closeConnection()
			t.Fatal(err)
		}
		if response.StatusCode != 200 {
			response.Body.Close()
			closeConnection()
			t.Fatal(response.Status)
		}
		t.Log("outer transport", response.Proto, "verified system-root HTTPS")
		x.down = response.Body
		t.Cleanup(func() { _ = x.Close() })
		if _, err := x.Write(vlessRequest(uuid, target)); err != nil {
			t.Fatal(err)
		}
		return x
	}
	conn := dial(echoHost + ":9000")
	defer conn.Close()
	payload := bytes.Repeat([]byte("live-xhttp\x00\xff"), 180000)
	wrote := make(chan error, 1)
	go func() {
		for offset := 0; offset < len(payload); {
			n, err := conn.Write(payload[offset:])
			if err != nil {
				wrote <- err
				return
			}
			offset += n
		}
		wrote <- nil
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatal("large echo failed", err)
	}
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	t.Logf("streamed %d bytes through HTTPS ingress", len(payload))
	// Cross both the former 30s request timeout and 45s idle policy. Leave
	// the stream genuinely idle for 65 seconds; data-plane liveness must hold.
	select {
	case <-time.After(65 * time.Second):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := conn.Write([]byte("idle-alive")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, got[:10]); err != nil || string(got[:10]) != "idle-alive" {
		t.Fatal("65s idle stream closed", err)
	}
	if !control.enforceIdleTimeout(session) {
		t.Fatal("control rejected data-plane liveness")
	}
	t.Log("65-second idle stream and durable heartbeat passed")
	innerConn := dial(sourceHost + ":443")
	inner := tls.Client(innerConn, &tls.Config{ServerName: sourceHost, MinVersion: tls.VersionTLS12})
	defer inner.Close()
	if err := inner.HandshakeContext(ctx); err != nil {
		t.Fatal("source TLS", err)
	}
	request, _ := http.NewRequest("GET", "https://"+sourceHost+"/", nil)
	if err := request.Write(inner); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(inner), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode >= 500 {
		t.Fatal("source response", response.Status)
	}
	_, err = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Log("source HTTPS verified", response.Status)
	inner.Close()
	if err := store.RevokeTunnelTickets(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := conn.Read(got[:1]); err == nil {
		t.Fatal("revoked stream still open")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("revocation took longer than 5s")
	}
	t.Log("public stream closed after credential revocation")
}
