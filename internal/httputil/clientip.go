package httputil

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP extracts the client IP address from the request.
// When trustProxy is true, X-Forwarded-For (first entry) and X-Real-IP
// headers are checked before falling back to RemoteAddr. Only enable
// trustProxy when the server is behind a trusted reverse proxy.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Take the first (leftmost) IP — the original client.
			if i := strings.IndexByte(xff, ','); i > 0 {
				xff = xff[:i]
			}
			if ip := strings.TrimSpace(xff); ip != "" {
				return ip
			}
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
