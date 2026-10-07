package byodserver

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Run with BYOD_XRAY_BINARY=/path/to/xray go test -race -run XrayInterop -v .
// Uses real Xray VLESS/XHTTP and verifies BOTH TLS layers against test CAs.
func TestXrayInteropClientToBYODInbound(t *testing.T) {
	binary := os.Getenv("BYOD_XRAY_BINARY")
	if binary == "" {
		t.Skip("set BYOD_XRAY_BINARY for independent protocol interop")
	}
	payload := bytes.Repeat([]byte("xray-to-byod\x00\xff"), 100000)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, r.Body)
	}))
	defer upstream.Close()
	s, front, session, uuid := xhttpTestService(t, upstream.URL)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	socksPort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	endpoint, _ := url.Parse(front.URL)
	port, _ := strconv.Atoi(endpoint.Port())
	cert := strings.Split(strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: front.Certificate().Raw}))), "\n")
	config := map[string]any{
		"log":      map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": socksPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}},
		"outbounds": []any{map[string]any{
			"protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": port, "users": []any{map[string]any{"id": uuid, "encryption": "none"}}}}},
			"streamSettings": map[string]any{"network": "xhttp", "security": "tls",
				"tlsSettings":   map[string]any{"serverName": "127.0.0.1", "disableSystemRoot": true, "certificates": []any{map[string]any{"usage": "verify", "certificate": cert}}},
				"xhttpSettings": map[string]any{"path": xhttpPath, "mode": "packet-up"}},
		}},
	}
	data, _ := json.Marshal(config)
	path := filepath.Join(t.TempDir(), "xray.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, binary, "run", "-c", path)
	var log bytes.Buffer
	command.Stdout = &log
	command.Stderr = &log
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = command.Wait()
		if t.Failed() {
			t.Log(log.String())
		}
	})
	socksAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(socksPort))
	ready := false
	for i := 0; i < 150; i++ {
		c, err := net.DialTimeout("tcp", socksAddr, 50*time.Millisecond)
		if err == nil {
			c.Close()
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("Xray SOCKS listener did not start")
	}
	proxy, _ := url.Parse("socks5://" + socksAddr)
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	response, err := client.Post(upstream.URL+"/answer", "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !bytes.Equal(body, payload) {
		t.Fatalf("interop echo failed: status=%d bytes=%d err=%v", response.StatusCode, len(body), err)
	}
	// A real client must not retain access after the exam session ends.
	r := httptest.NewRequest("POST", "/v1/exams/course-101/complete", nil)
	r.Header.Set("Authorization", "Bearer "+session["browser_token"])
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if response, err := client.Get(upstream.URL); err == nil {
		response.Body.Close()
		t.Fatal("Xray retained revoked exam access")
	}
}
