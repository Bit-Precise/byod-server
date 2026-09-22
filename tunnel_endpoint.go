package byodserver

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// ParseTunnelCIDRs parses a comma-separated list of client address ranges.
// Empty input intentionally means that the public endpoint is always used.
func ParseTunnelCIDRs(raw string) ([]*net.IPNet, error) {
	var result []*net.IPNet
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		_, network, err := net.ParseCIDR(item)
		if err != nil {
			return nil, fmt.Errorf("invalid tunnel client CIDR %q: %w", item, err)
		}
		result = append(result, network)
	}
	return result, nil
}

// tunnelEndpointForRequest keeps the wire protocol unchanged while allowing
// clients that cannot hairpin through the public DNAT address to receive a
// private endpoint. X-Forwarded-For is supplied by the Gateway in production;
// the first address is the original client address in the proxy chain. If the
// request is direct, RemoteAddr remains a useful fallback for local testing.
func (s *Service) tunnelEndpointForRequest(request *http.Request) string {
	public := s.TunnelEndpoint
	if request == nil || s.TunnelPrivateEndpoint == "" || len(s.TunnelPrivateCIDRs) == 0 {
		return public
	}
	clientIP := requestClientIP(request)
	if clientIP == nil {
		return public
	}
	for _, network := range s.TunnelPrivateCIDRs {
		if network.Contains(clientIP) {
			return s.TunnelPrivateEndpoint
		}
	}
	return public
}

func requestClientIP(request *http.Request) net.IP {
	if forwarded := request.Header.Get("X-Forwarded-For"); forwarded != "" {
		for _, item := range strings.Split(forwarded, ",") {
			if ip := parseForwardedIP(item); ip != nil {
				return ip
			}
		}
	}
	if realIP := parseForwardedIP(request.Header.Get("X-Real-IP")); realIP != nil {
		return realIP
	}
	if host, _, err := net.SplitHostPort(strings.TrimSpace(request.RemoteAddr)); err == nil {
		return net.ParseIP(host)
	}
	return net.ParseIP(strings.TrimSpace(request.RemoteAddr))
}

func parseForwardedIP(raw string) net.IP {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	return net.ParseIP(strings.Trim(raw, "[]"))
}
