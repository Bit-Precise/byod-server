package byodserver

// Opt-in loopback-only fixture for the actual Windows Chromium client.
// BYOD_NATIVE_FIXTURE must name a new JSON file in a private temporary dir.
// No production credentials, system trust changes or public listeners.
import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeXHTTPFixture(t *testing.T) {
	path := os.Getenv("BYOD_NATIVE_FIXTURE")
	if path == "" {
		t.Skip("set BYOD_NATIVE_FIXTURE to run the manual native browser fixture")
	}
	var inner, downloads, uploads atomic.Int64
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.Add(1)
		if r.Method == "POST" {
			time.Sleep(200 * time.Millisecond) // Exercise concurrent source connections.
			// HTTP/1.1's default server mode drains unread request bytes when
			// writing a response. Read the entire upload before echoing it.
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			_, _ = w.Write(body)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<!doctype html><title>BYOD native XHTTP OK</title><p>Source TLS over VLESS/XHTTP</p>")
	}))
	defer upstream.Close()
	s, front, _, uuid := xhttpTestService(t, upstream.URL)
	// xhttpTestService already started a verified HTTPS endpoint. Install a
	// wrapper before clients connect, retaining its TLS/HTTP2 configuration.
	stop := make(chan struct{})
	var stopOnce sync.Once
	var protoMu sync.Mutex
	protocols := map[string]int{}
	front.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, xhttpPath) {
			if r.Method == "GET" {
				downloads.Add(1)
			} else if r.Method == "POST" {
				uploads.Add(1)
			}
			protoMu.Lock()
			protocols[r.Proto]++
			protoMu.Unlock()
			s.serveXHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/fixture/") {
			if r.Header.Get("Authorization") != "Bearer "+uuid {
				w.WriteHeader(403)
				return
			}
			protoMu.Lock()
			s.xhttp.mu.Lock()
			active := len(s.xhttp.sessions)
			s.xhttp.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"inner": inner.Load(), "downloads": downloads.Load(), "uploads": uploads.Load(), "protocols": protocols, "active": active,
			})
			protoMu.Unlock()
			if r.URL.Path == "/fixture/stop" {
				stopOnce.Do(func() { close(stop) })
			}
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<!doctype html><title>BYOD isolated control fixture</title>")
	})
	digest := sha256.Sum256(front.Certificate().RawSubjectPublicKeyInfo)
	metadata := map[string]any{
		"front": front.URL, "source": upstream.URL, "ticket": uuid,
		"spki":        base64.StdEncoding.EncodeToString(digest[:]),
		"certificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: front.Certificate().Raw})),
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(f).Encode(metadata); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	t.Log("Native fixture ready; expires in 10 minutes")
	select {
	case <-stop:
	case <-time.After(10 * time.Minute):
		t.Fatal("native fixture timed out")
	}
}
