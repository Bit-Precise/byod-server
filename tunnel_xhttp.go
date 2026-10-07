package byodserver

// Xray-compatible XHTTP packet-up profile: path session/sequence metadata,
// POST body uploads, one streaming GET download, no additional wire framing.
import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const xhttpPath = "/v1/xhttp/"
const xhttpMaxPost = 1_000_000 // Xray's default scMaxEachPostBytes.
const xhttpMaxBuffered = 64 << 20

type xhttpState struct {
	mu       sync.Mutex
	sessions map[string]*xhttpSession
	buffered atomic.Int64
	closed   bool
}

type xhttpSession struct {
	state             *xhttpState
	id                string
	ctx               context.Context
	cancel            context.CancelFunc
	server, transport net.Conn
	once              sync.Once
	mu                sync.Mutex
	packets           map[uint64][]byte
	next              uint64
	buffered          int
	getAttached       bool
	closed            bool
	notify            chan struct{}
}

func (s *Service) xhttpEndpoint() string {
	u, _ := url.Parse(s.ExamOrigin)
	u.Path, u.RawPath, u.RawQuery, u.Fragment = xhttpPath, "", "", ""
	return u.String()
}

func (s *Service) xhttpSession(id string) *xhttpSession {
	state := &s.xhttp
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil
	}
	if current := state.sessions[id]; current != nil {
		return current
	}
	if len(state.sessions) >= 4096 {
		return nil
	}
	if state.sessions == nil {
		state.sessions = make(map[string]*xhttpSession)
	}
	ctx, cancel := context.WithCancel(context.Background())
	server, transport := net.Pipe()
	x := &xhttpSession{state: state, id: id, ctx: ctx, cancel: cancel, server: server, transport: transport,
		packets: make(map[uint64][]byte), notify: make(chan struct{}, 1)}
	state.sessions[id] = x
	go func() { defer x.close(); s.serveVLESS(ctx, server) }()
	go func() { defer x.close(); _, _ = io.Copy(transport, x) }()
	return x
}

func (x *xhttpSession) close() {
	x.once.Do(func() {
		x.cancel()
		_ = x.server.Close()
		_ = x.transport.Close()
		x.mu.Lock()
		x.closed = true
		x.state.buffered.Add(-int64(x.buffered))
		x.buffered = 0
		x.packets = nil
		x.mu.Unlock()
		x.state.mu.Lock()
		if x.state.sessions[x.id] == x {
			delete(x.state.sessions, x.id)
		}
		x.state.mu.Unlock()
	})
}

func (x *xhttpSession) push(seq uint64, payload []byte) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.closed {
		return io.ErrClosedPipe
	}
	if seq < x.next {
		return nil
	} // An acknowledged packet may be retried.
	if old, exists := x.packets[seq]; exists {
		if bytes.Equal(old, payload) {
			return nil
		}
		return errors.New("conflicting sequence")
	}
	if seq-x.next > 32 || len(x.packets) >= 32 || x.buffered+len(payload) > 2<<20 {
		return errors.New("upload queue full")
	}
	if x.state.buffered.Add(int64(len(payload))) > xhttpMaxBuffered {
		x.state.buffered.Add(-int64(len(payload)))
		return errors.New("global upload queue full")
	}
	x.packets[seq] = payload
	x.buffered += len(payload)
	select {
	case x.notify <- struct{}{}:
	default:
	}
	return nil
}

func (x *xhttpSession) Read(dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	for {
		x.mu.Lock()
		if x.closed {
			x.mu.Unlock()
			return 0, io.EOF
		}
		if payload, ok := x.packets[x.next]; ok {
			n := copy(dst, payload)
			x.buffered -= n
			x.state.buffered.Add(-int64(n))
			if n == len(payload) {
				delete(x.packets, x.next)
				x.next++
			} else {
				x.packets[x.next] = payload[n:]
			}
			x.mu.Unlock()
			if n > 0 {
				return n, nil
			}
			continue
		}
		x.mu.Unlock()
		select {
		case <-x.ctx.Done():
			return 0, io.EOF
		case <-x.notify:
		}
	}
}

func (s *Service) CloseTunnels() {
	s.xhttp.mu.Lock()
	s.xhttp.closed = true
	connections := make([]*xhttpSession, 0, len(s.xhttp.sessions))
	for _, x := range s.xhttp.sessions {
		connections = append(connections, x)
	}
	s.xhttp.mu.Unlock()
	for _, x := range connections {
		x.close()
	}
}

func (s *Service) serveXHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Padding", strings.Repeat("X", 128))
	// Standard native Xray clients omit Origin. Web-origin requests must be
	// from the configured control plane, but Origin is never authentication.
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.ExamOrigin {
		http.Error(w, "origin denied", http.StatusForbidden)
		return
	}
	// Only transport padding belongs in the query; never accept credentials.
	for key := range r.URL.Query() {
		if key != "x_padding" {
			http.Error(w, "invalid query", 400)
			return
		}
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, xhttpPath), "/")
	if len(parts) < 1 || len(parts) > 2 || !validExamUUID(parts[0]) || r.Header.Get("Content-Encoding") != "" {
		http.Error(w, "invalid XHTTP path", 400)
		return
	}
	isGet := r.Method == http.MethodGet && len(parts) == 1
	isPost := r.Method == http.MethodPost && len(parts) == 2
	if !isGet && !isPost {
		http.Error(w, "packet-up only", 405)
		return
	}
	var seq uint64
	if isPost {
		var err error
		seq, err = strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			http.Error(w, "invalid sequence", 400)
			return
		}
	}
	var payload []byte
	if isPost {
		var err error
		payload, err = io.ReadAll(http.MaxBytesReader(w, r.Body, xhttpMaxPost))
		if err != nil {
			http.Error(w, "invalid upload", 413)
			return
		}
	}
	x := s.xhttpSession(strings.ToLower(parts[0]))
	if x == nil {
		http.Error(w, "tunnel capacity exceeded", 503)
		return
	}
	if isPost {
		if err := x.push(seq, payload); err != nil {
			x.close()
			http.Error(w, "upload rejected", 409)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	x.mu.Lock()
	duplicate := x.getAttached || x.closed
	x.getAttached = true
	x.mu.Unlock()
	if duplicate {
		http.Error(w, "duplicate download", 409)
		return
	}
	defer x.close()
	stopCancel := context.AfterFunc(r.Context(), x.close)
	defer stopCancel()
	control := http.NewResponseController(w)
	// Override the HTTP API's short write timeout only on this streaming GET.
	_ = control.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if err := control.Flush(); err != nil {
		return
	}
	buffer := make([]byte, 32<<10)
	for {
		n, err := x.transport.Read(buffer)
		if n > 0 {
			_ = control.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				return
			}
			if flushErr := control.Flush(); flushErr != nil {
				return
			}
			_ = control.SetWriteDeadline(time.Time{})
		}
		if err != nil {
			return
		}
	}
}
