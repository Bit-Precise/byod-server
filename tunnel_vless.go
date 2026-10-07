package byodserver

// VLESS version 0, encryption=none, flow="", TCP only. XHTTP provides the
// stream; HTTPS protects the UUID on the wire. Source TLS stays end-to-end.
import (
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
)

func readVLESSRequest(r io.Reader) (credential, target string, err error) {
	var header [18]byte
	if _, err = io.ReadFull(r, header[:]); err != nil {
		return
	}
	// No Vision/addons. Do not silently interpret another protocol or flow.
	if header[0] != 0 || header[17] != 0 {
		return "", "", errTunnelMalformed
	}
	h := hex.EncodeToString(header[1:17])
	credential = h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
	var destination [4]byte
	if _, err = io.ReadFull(r, destination[:]); err != nil {
		return
	}
	port := int(destination[1])<<8 | int(destination[2])
	if destination[0] != 1 || port == 0 {
		return "", "", errTunnelMalformed
	}
	var address []byte
	switch destination[3] {
	case 1:
		address = make([]byte, 4)
	case 3:
		address = make([]byte, 16)
	case 2:
		var length [1]byte
		if _, err = io.ReadFull(r, length[:]); err != nil {
			return
		}
		if length[0] == 0 {
			return "", "", errTunnelMalformed
		}
		address = make([]byte, length[0])
	default:
		return "", "", errTunnelMalformed
	}
	if _, err = io.ReadFull(r, address); err != nil {
		return
	}
	host := string(address)
	if destination[3] != 2 {
		host = net.IP(address).String()
	}
	target = net.JoinHostPort(host, strconv.Itoa(port))
	return
}

func (s *Service) lookupVLESSCredential(ctx context.Context, credential string) (TunnelTicketInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if !validExamUUID(credential) {
		return TunnelTicketInfo{}, errTunnelDenied
	}
	hash := hashTunnelTicket(strings.ToLower(credential))
	var info TunnelTicketInfo
	if s.ExamStore != nil {
		x, ok, err := s.ExamStore.LookupVLESSCredential(ctx, []byte(hash), s.identityIssuer(), time.Now())
		if err != nil {
			return info, err
		}
		if !ok {
			return info, errTunnelDenied
		}
		info = TunnelTicketInfo{SessionID: x.SessionID, ExamID: x.ExamID, EndpointID: x.EndpointID, ExpiresAt: x.ExpiresAt}
		// No local session map is consulted or populated in the data plane.
		_, maxIdle, err := s.sessionLimitsContext(ctx, info.ExamID)
		if err != nil {
			return TunnelTicketInfo{}, err
		}
		active, err := s.ExamStore.TouchActiveSession(ctx, info.SessionID, time.Now().Unix(), maxIdle)
		if err != nil {
			return TunnelTicketInfo{}, err
		}
		if !active {
			return TunnelTicketInfo{}, errTunnelDenied
		}
		return info, nil
	} else {
		s.mu.RLock()
		x := s.tunnelTickets[hash]
		if x == nil || !time.Now().Before(x.ExpiresAt) {
			s.mu.RUnlock()
			return info, errTunnelDenied
		}
		info = TunnelTicketInfo{SessionID: x.SessionID, ExamID: x.ExamID, EndpointID: x.EndpointID, ExpiresAt: x.ExpiresAt}
		s.mu.RUnlock()
	}
	if !s.tunnelSessionActive(info.SessionID) {
		return TunnelTicketInfo{}, errTunnelDenied
	}
	return info, nil
}

func (s *Service) serveVLESS(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	credential, target, err := readVLESSRequest(conn)
	if err != nil {
		return
	}
	info, err := s.lookupVLESSCredential(ctx, credential)
	if err != nil {
		return
	}
	address, err := s.tunnelAddress(ctx, info.ExamID, target)
	if err != nil {
		return
	}
	upstream, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		slog.Warn("vless_dial_failed", "exam_id", info.ExamID, "target", address, "error", err.Error())
		return
	}
	defer upstream.Close()
	// VLESS response: version 0, addons length 0. This is not an HTTP response.
	if _, err = conn.Write([]byte{0, 0}); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		defer conn.Close()
		defer upstream.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !time.Now().Before(info.ExpiresAt) {
					return
				}
				// Recheck the credential itself, not only the session state:
				// suspension followed by a quick resume must not resurrect an
				// already revoked UUID's established streams.
				if _, err := s.lookupVLESSCredential(ctx, credential); err != nil {
					return
				}
				// Policy changes also revoke an already established destination.
				if _, err := s.tunnelAddress(ctx, info.ExamID, target); err != nil {
					return
				}
			}
		}
	}()
	done := make(chan struct{}, 2)
	copyStream := func(dst, src net.Conn) { _, _ = io.Copy(dst, src); cancel(); done <- struct{}{} }
	go copyStream(upstream, conn)
	go copyStream(conn, upstream)
	<-done
	<-done
}
