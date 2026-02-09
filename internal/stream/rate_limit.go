package stream

import (
	"net"
	"net/http"
	"sync"
)

// streamLimiter tracks concurrent SSE connections per IP.
type streamLimiter struct {
	mu          sync.Mutex
	connections map[string]int
	maxPerIP    int
}

func newStreamLimiter(maxPerIP int) *streamLimiter {
	return &streamLimiter{
		connections: make(map[string]int),
		maxPerIP:    maxPerIP,
	}
}

// acquire attempts to register a new connection for the given IP.
// Returns false if the IP has reached its concurrent connection limit.
func (l *streamLimiter) acquire(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.connections[ip] >= l.maxPerIP {
		return false
	}

	l.connections[ip]++
	return true
}

// release decrements the connection count for the given IP.
func (l *streamLimiter) release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.connections[ip]--
	if l.connections[ip] <= 0 {
		delete(l.connections, ip)
	}
}

// count returns the number of active connections for the given IP.
func (l *streamLimiter) count(ip string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.connections[ip]
}

// clientIP extracts the client IP from the request.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
