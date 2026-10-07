package byodserver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func vlessRequest(uuid, target string) []byte {
	id, _ := hex.DecodeString(strings.ReplaceAll(uuid, "-", ""))
	host, portText, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portText)
	frame := append([]byte{0}, id...)
	frame = append(frame, 0, 1, byte(port>>8), byte(port), 2, byte(len(host)))
	return append(frame, []byte(host)...)
}

func xhttpTestService(t *testing.T, upstream string) (*Service, *httptest.Server, map[string]string, string) {
	t.Helper()
	s, err := NewService("https://exam.cs.ac.cn", upstream, []byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	s.PolicyOverrides = map[string]map[string]any{"course-101": {"tunnel_hosts": []string{"127.0.0.1"}}}
	front := httptest.NewUnstartedServer(s)
	front.Config.WriteTimeout = 250 * time.Millisecond // GET must override API timeout.
	front.EnableHTTP2 = true
	front.StartTLS()
	t.Cleanup(front.Close)
	t.Cleanup(s.CloseTunnels)
	s.ExamOrigin = front.URL
	session := activateTestSession(t, s, "course-101")
	ticket, _, err := s.IssueTunnelTicket(context.Background(), session["session_id"])
	if err != nil {
		t.Fatal(err)
	}
	return s, front, session, ticket
}

type xhttpTestConn struct {
	client     *http.Client
	base       string
	ctx        context.Context
	cancel     context.CancelFunc
	down       io.ReadCloser
	seq        int
	headerRead bool
}

func dialTestXHTTP(t *testing.T, front *httptest.Server) *xhttpTestConn {
	t.Helper()
	id, _ := newExamUUID()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	x := &xhttpTestConn{client: front.Client(), base: front.URL + xhttpPath + id, ctx: ctx, cancel: cancel}
	r, _ := http.NewRequestWithContext(ctx, "GET", x.base, nil)
	response, err := x.client.Do(r)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		cancel()
		response.Body.Close()
		t.Fatal(response.Status)
	}
	x.down = response.Body
	t.Cleanup(func() { x.Close() })
	return x
}
func (x *xhttpTestConn) post(seq int, payload []byte) error {
	r, _ := http.NewRequestWithContext(x.ctx, "POST", x.base+"/"+strconv.Itoa(seq), bytes.NewReader(payload))
	response, err := x.client.Do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != 200 {
		return fmt.Errorf("POST: %s", response.Status)
	}
	return nil
}
func (x *xhttpTestConn) Write(p []byte) (int, error) {
	n := min(len(p), 65536)
	if err := x.post(x.seq, p[:n]); err != nil {
		return 0, err
	}
	x.seq++
	return n, nil
}
func (x *xhttpTestConn) Read(p []byte) (int, error) {
	if !x.headerRead {
		var response [2]byte
		if _, err := io.ReadFull(x.down, response[:]); err != nil {
			return 0, err
		}
		if response != [2]byte{} {
			return 0, fmt.Errorf("VLESS response: %x", response)
		}
		x.headerRead = true
	}
	return x.down.Read(p)
}
func (x *xhttpTestConn) Close() error                   { x.cancel(); return x.down.Close() }
func (*xhttpTestConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (*xhttpTestConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (*xhttpTestConn) SetDeadline(time.Time) error      { return nil }
func (*xhttpTestConn) SetReadDeadline(time.Time) error  { return nil }
func (*xhttpTestConn) SetWriteDeadline(time.Time) error { return nil }

func TestXHTTPVerifiedInnerTLSLargePayloadAndHTTPTimeout(t *testing.T) {
	payload := bytes.Repeat([]byte("exam\x00\xff"), 300000)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !bytes.Equal(body, payload) {
			http.Error(w, "mismatch", 500)
			return
		}
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write(body)
	}))
	defer upstream.Close()
	_, front, _, uuid := xhttpTestService(t, upstream.URL)
	conn := dialTestXHTTP(t, front)
	if _, err := conn.Write(vlessRequest(uuid, strings.TrimPrefix(upstream.URL, "https://"))); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	inner := tls.Client(conn, &tls.Config{RootCAs: roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12})
	if err := inner.Handshake(); err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest("POST", upstream.URL+"/answer", bytes.NewReader(payload))
	if err := r.Write(inner); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(inner), r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !bytes.Equal(got, payload) {
		t.Fatalf("echo: %d, %d bytes, %v", response.StatusCode, len(got), err)
	}
}

func TestXHTTPRejectsUnauthenticatedAndUnlistedDestinations(t *testing.T) {
	_, front, _, uuid := xhttpTestService(t, "https://127.0.0.1:9")
	for _, test := range []struct{ uuid, target string }{
		{"00000000-0000-4000-8000-000000000000", "127.0.0.1:9"}, {uuid, "unlisted.example:443"},
	} {
		conn := dialTestXHTTP(t, front)
		_, _ = conn.Write(vlessRequest(test.uuid, test.target))
		if _, err := conn.Read(make([]byte, 1)); err == nil {
			t.Fatal("unauthorized VLESS accepted")
		}
	}
}

func TestXHTTPReordersFragmentsAndRevokesOpenStreams(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		c, e := upstream.Accept()
		if e == nil {
			defer c.Close()
			_, _ = io.Copy(c, c)
		}
	}()
	s, front, session, uuid := xhttpTestService(t, "https://"+upstream.Addr().String())
	conn := dialTestXHTTP(t, front)
	header := vlessRequest(uuid, upstream.Addr().String())
	if err := conn.post(1, append(header[5:], []byte("echo")...)); err != nil {
		t.Fatal(err)
	}
	if err := conn.post(0, header[:5]); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "echo" {
		t.Fatalf("fragmented stream: %q %v", got, err)
	}
	r := httptest.NewRequest("POST", "/v1/exams/course-101/complete", nil)
	r.Header.Set("Authorization", "Bearer "+session["browser_token"])
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	start := time.Now()
	if _, err := conn.Read(got); err == nil {
		t.Fatal("ended exam still open")
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("revocation too slow")
	}
	if _, err := s.lookupVLESSCredential(context.Background(), uuid); err == nil {
		t.Fatal("revoked UUID reusable")
	}
}

func TestVLESSHeaderRejectsUnsupportedProtocols(t *testing.T) {
	valid := vlessRequest("12345678-1234-4321-8123-123456789abc", "example.test:443")
	for name, index := range map[string]int{"version": 0, "addons": 17, "UDP": 18} {
		t.Run(name, func(t *testing.T) {
			p := bytes.Clone(valid)
			p[index] = 2
			if _, _, err := readVLESSRequest(bytes.NewReader(p)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	for i := 0; i < len(valid); i++ {
		if _, _, err := readVLESSRequest(bytes.NewReader(valid[:i])); err == nil {
			t.Fatalf("accepted truncation %d", i)
		}
	}
	if _, _, err := readVLESSRequest(strings.NewReader("CONNECT source:443 HTTP/1.1\r\n\r\n")); err == nil {
		t.Fatal("CONNECT accepted")
	}
}

func TestVLESSRevokesOpenStreamWhileSessionRemainsActive(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		conn, err := upstream.Accept()
		if err == nil {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		}
	}()
	s, _, session, uuid := xhttpTestService(t, "https://"+upstream.Addr().String())
	client, server := net.Pipe()
	defer client.Close()
	go s.serveVLESS(context.Background(), server)
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.Write(vlessRequest(uuid, upstream.Addr().String())); err != nil {
		t.Fatal(err)
	}
	var response [2]byte
	if _, err := io.ReadFull(client, response[:]); err != nil || response != [2]byte{} {
		t.Fatalf("handshake: %x %v", response, err)
	}
	s.mu.Lock()
	s.revokeTunnelTicketsLocked(session["session_id"])
	stillActive := s.sessions[session["session_id"]].State == "active"
	s.mu.Unlock()
	if !stillActive {
		t.Fatal("fixture must leave session active")
	}
	if _, err := client.Read(response[:]); err != io.EOF {
		t.Fatalf("revoked credential stream must close, got %v", err)
	}
}

func TestXHTTPRejectsInvalidHTTPMetadata(t *testing.T) {
	s, front, _, _ := xhttpTestService(t, "https://127.0.0.1:9")
	id, _ := newExamUUID()
	for _, test := range []struct {
		name, method, path, origin, encoding string
		body                                 []byte
		status                               int
	}{
		{name: "foreign origin", method: "GET", path: id, origin: "https://evil.example", status: 403},
		{name: "credential in query", method: "GET", path: id + "?ticket=secret", status: 400},
		{name: "invalid session", method: "GET", path: "not-a-uuid", status: 400},
		{name: "negative sequence", method: "POST", path: id + "/-1", status: 400},
		{name: "overflow sequence", method: "POST", path: id + "/18446744073709551616", status: 400},
		{name: "extra path", method: "POST", path: id + "/0/extra", status: 400},
		{name: "unsupported method", method: "PUT", path: id, status: 405},
		{name: "compressed upload", method: "POST", path: id + "/0", encoding: "gzip", status: 400},
		{name: "oversized upload", method: "POST", path: id + "/0", body: make([]byte, xhttpMaxPost+1), status: 413},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, _ := http.NewRequest(test.method, front.URL+xhttpPath+test.path, bytes.NewReader(test.body))
			r.Header.Set("Origin", test.origin)
			r.Header.Set("Content-Encoding", test.encoding)
			response, err := front.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.status {
				t.Fatalf("got %d, want %d", response.StatusCode, test.status)
			}
		})
	}
	s.xhttp.mu.Lock()
	defer s.xhttp.mu.Unlock()
	if len(s.xhttp.sessions) != 0 {
		t.Fatal("rejected HTTP request created a transport session")
	}
}

func TestXHTTPDuplicateDownloadAndShutdown(t *testing.T) {
	s, front, _, _ := xhttpTestService(t, "https://127.0.0.1:9")
	conn := dialTestXHTTP(t, front)
	response, err := front.Client().Get(conn.base)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 409 {
		t.Fatalf("duplicate GET: %s", response.Status)
	}
	s.CloseTunnels()
	if _, err := conn.down.Read(make([]byte, 1)); err == nil {
		t.Fatal("download survived shutdown")
	}
	s.xhttp.mu.Lock()
	count := len(s.xhttp.sessions)
	s.xhttp.mu.Unlock()
	if count != 0 || s.xhttp.buffered.Load() != 0 {
		t.Fatal("shutdown leaked transport state")
	}
	id, _ := newExamUUID()
	response, err = front.Client().Get(front.URL + xhttpPath + id)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatalf("new session after shutdown: %s", response.Status)
	}
}

func TestXHTTPQueueBoundsAndAccounting(t *testing.T) {
	newQueue := func(t *testing.T) *xhttpSession {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		a, b := net.Pipe()
		x := &xhttpSession{state: &xhttpState{}, ctx: ctx, cancel: cancel, server: a, transport: b,
			packets: make(map[uint64][]byte), notify: make(chan struct{}, 1)}
		t.Cleanup(x.close)
		return x
	}
	t.Run("ordering duplicates and cleanup", func(t *testing.T) {
		x := newQueue(t)
		if err := x.push(1, []byte("second")); err != nil {
			t.Fatal(err)
		}
		if err := x.push(1, []byte("second")); err != nil || x.state.buffered.Load() != 6 {
			t.Fatal("duplicate packet changed accounting", err)
		}
		if err := x.push(1, []byte("conflict")); err == nil {
			t.Fatal("conflicting packet accepted")
		}
		if err := x.push(0, []byte("first")); err != nil {
			t.Fatal(err)
		}
		if n, err := x.Read(nil); n != 0 || err != nil {
			t.Fatal("zero-length Read must not consume a packet")
		}
		got := make([]byte, 11)
		if _, err := io.ReadFull(x, got); err != nil || string(got) != "firstsecond" || x.state.buffered.Load() != 0 {
			t.Fatalf("read=%q, buffered=%d, err=%v", got, x.state.buffered.Load(), err)
		}
		if err := x.push(2, []byte("pending")); err != nil {
			t.Fatal(err)
		}
		x.close()
		if x.state.buffered.Load() != 0 {
			t.Fatal("close leaked queue accounting")
		}
		if err := x.push(3, []byte("late")); err == nil {
			t.Fatal("closed queue accepted upload")
		}
	})
	t.Run("sequence window", func(t *testing.T) {
		x := newQueue(t)
		if err := x.push(33, []byte{1}); err == nil {
			t.Fatal("unbounded reordering accepted")
		}
		for i := uint64(0); i < 32; i++ {
			if err := x.push(i, nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := x.push(32, nil); err == nil {
			t.Fatal("packet count limit not enforced")
		}
	})
	t.Run("per-session bytes", func(t *testing.T) {
		x := newQueue(t)
		if err := x.push(0, make([]byte, 2<<20)); err != nil {
			t.Fatal(err)
		}
		if err := x.push(1, []byte{1}); err == nil {
			t.Fatal("per-session byte limit not enforced")
		}
	})
	t.Run("global bytes", func(t *testing.T) {
		x := newQueue(t)
		x.state.buffered.Store(xhttpMaxBuffered)
		if err := x.push(0, []byte{1}); err == nil || x.state.buffered.Load() != xhttpMaxBuffered {
			t.Fatal("global limit or rollback failed")
		}
		x.state.buffered.Store(0)
	})
}
